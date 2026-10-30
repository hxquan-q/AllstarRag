package middleware

import (
	"net/http"
	"sync"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

// auth_public_ratelimit.go — sliding-window IP rate limiter for the
// unauthenticated auth entry points (/auth/invitations/lookup,
// /auth/register-by-invite, /auth/login, /auth/register). The
// share-link surfaces accept a plaintext token from the request and
// either reveal tenant context (lookup) or create an account
// (register-by-invite); without a limiter an attacker can brute-force
// token guesses and hammer registration. Login/register are limited
// separately (password guessing / bulk account creation).
//
// The token is 256-bit so guessing the space is infeasible regardless,
// but the limiter still narrows the abuse window for partial-token
// leaks (e.g. via referrer / clipboard managers / accidental commits)
// and bounds the noise this endpoint can add to the user-create path.
//
// Local in-memory only — fine for typical deployments since both
// endpoints handle low absolute volumes; if/when WeKnora horizontally
// scales the auth surface, swap to the Redis-backed limiter in
// internal/ratelimit (shared with IM + embed surfaces).

// publicAuthRateLimitWindow is the rolling window length per IP.
const publicAuthRateLimitWindow = 60 * time.Second

// publicAuthRateLimitMax is the request budget per IP per window for
// each endpoint instance. 30/min comfortably covers a real user
// retrying a failed registration while clamping enumeration
// throughput to ~half a request per second.
const publicAuthRateLimitMax = 30

// publicAuthRateLimitCleanupInterval bounds map growth from one-off
// IPs that never come back.
const publicAuthRateLimitCleanupInterval = 2 * time.Minute

type ipBucket struct {
	mu         sync.Mutex
	timestamps []time.Time
}

type ipRateLimiter struct {
	window  time.Duration
	max     int
	buckets sync.Map // string (IP) -> *ipBucket
}

func newIPRateLimiter(window time.Duration, max int) *ipRateLimiter {
	l := &ipRateLimiter{window: window, max: max}
	go l.cleanupLoop()
	return l
}

// allow returns true if the IP is within budget for the current
// window. Empty IP (proxy stripped X-Forwarded-For) is treated as a
// shared bucket — slightly over-restrictive vs. dropping the limit
// entirely, which would let a misconfigured proxy bypass the gate.
func (l *ipRateLimiter) allow(ip string) bool {
	if ip == "" {
		ip = "_unknown_"
	}
	now := time.Now()
	cutoff := now.Add(-l.window)

	val, _ := l.buckets.LoadOrStore(ip, &ipBucket{})
	b := val.(*ipBucket)
	b.mu.Lock()
	defer b.mu.Unlock()

	kept := b.timestamps[:0]
	for _, t := range b.timestamps {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.timestamps = kept
	if len(b.timestamps) >= l.max {
		return false
	}
	b.timestamps = append(b.timestamps, now)
	return true
}

func (l *ipRateLimiter) cleanupLoop() {
	t := time.NewTicker(publicAuthRateLimitCleanupInterval)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-l.window)
		l.buckets.Range(func(k, v any) bool {
			b := v.(*ipBucket)
			b.mu.Lock()
			drop := len(b.timestamps) == 0 ||
				b.timestamps[len(b.timestamps)-1].Before(cutoff)
			b.mu.Unlock()
			if drop {
				l.buckets.Delete(k)
			}
			return true
		})
	}
}

// PublicAuthRateLimitWith 按端点参数化限速:window/max 决定预算,
// name 只用于日志区分。每次调用创建独立的计数桶,不同端点互不共享。
// 超限返回 429(apperrors.ErrTooManyRequests),并记一行 warn(端点+IP,
// 不记录请求体)。旧函数 PublicAuthRateLimit 委托到这里,行为保持不变。
func PublicAuthRateLimitWith(window time.Duration, max int, name string) gin.HandlerFunc {
	limiter := newIPRateLimiter(window, max)
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !limiter.allow(ip) {
			logger.Warnf(c.Request.Context(),
				"[auth-ratelimit] %s: ip=%s exceeded %d requests per %s",
				name, ip, max, window)
			c.Error(&apperrors.AppError{
				Code:     apperrors.ErrTooManyRequests,
				Message:  "too many requests; please retry shortly",
				HTTPCode: http.StatusTooManyRequests,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// PublicAuthRateLimit 保留旧行为:共享链接端点 30 次/分钟/IP。
// 调用方在路由注册时只调用一次,把返回的 handler 同时挂到
// register-by-invite 与 lookup 上,即可保持两个端点共用预算。
func PublicAuthRateLimit() gin.HandlerFunc {
	return PublicAuthRateLimitWith(
		publicAuthRateLimitWindow, publicAuthRateLimitMax, "public-auth")
}
