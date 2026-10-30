package middleware

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

// auth_public_ratelimit.go — sliding-window IP rate limiter for the
// unauthenticated share-link endpoints (/auth/invitations/lookup and
// /auth/register-by-invite). Both surfaces accept a plaintext token
// from the request and either reveal tenant context (lookup) or
// create an account (register-by-invite); without a limiter an
// attacker can brute-force token guesses and hammer registration.
//
// 本 feature 追加:同一限速器也挂到 /auth/login 与 /auth/register,
// 各端点独立计数(登录防密码猜测、注册防批量建号)。
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

// publicAuthRateLimitMaxBuckets 是单个限速器最多保留的 IP 桶数量。
// 清理协程固定每 2 分钟才跑一次,而 login/register 是公开入口,
// 大量不同来源 IP(IPv6 /64、伪造 X-Forwarded-For 等)会让 sync.Map
// 在两次清理之间无限增长,形成内存放大。这里设硬上限:新 IP 建立桶时
// 若超限,先清理已过期(窗口内没有请求)的桶,仍超限就淘汰最久未用的桶。
// 2 万个桶约占几 MB,远超正常部署的并发客户端数。
const publicAuthRateLimitMaxBuckets = 20000

type ipBucket struct {
	mu         sync.Mutex
	timestamps []time.Time
	// lastSeen 是最近一次请求的时间,用于桶数超限时挑最久未用的桶淘汰。
	// 读写在 mu 保护下进行。
	lastSeen time.Time
}

type ipRateLimiter struct {
	window     time.Duration
	max        int
	maxBuckets int
	buckets    sync.Map // string (IP) -> *ipBucket
	// bucketCount 记录当前桶数量。sync.Map 没有 len,单独计数才能
	// 在不遍历的情况下判断是否超限。
	bucketCount atomic.Int64
	// evictMu 只在超限淘汰时加锁,避免并发插入重复做全量扫描。
	evictMu sync.Mutex
}

func newIPRateLimiter(window time.Duration, max int) *ipRateLimiter {
	l := &ipRateLimiter{
		window:     window,
		max:        max,
		maxBuckets: publicAuthRateLimitMaxBuckets,
	}
	go l.cleanupLoop()
	return l
}

// bucketLimit 返回实际生效的桶数上限;直接构造(测试)没填 maxBuckets
// 时回落到默认常量。
func (l *ipRateLimiter) bucketLimit() int {
	if l.maxBuckets > 0 {
		return l.maxBuckets
	}
	return publicAuthRateLimitMaxBuckets
}

// bucketFor 取出 IP 对应的桶,不存在则新建并计入桶数。新建后若超过
// 上限,触发一次清理/淘汰。
func (l *ipRateLimiter) bucketFor(ip string, now time.Time) *ipBucket {
	// 新建时先写 lastSeen,避免刚建好的桶因为零值被当成"最久未用"
	// 而被本次淘汰选中。
	val, loaded := l.buckets.LoadOrStore(ip, &ipBucket{lastSeen: now})
	if !loaded {
		l.bucketCount.Add(1)
		l.enforceBucketLimit()
	}
	return val.(*ipBucket)
}

// enforceBucketLimit 保证桶数不超过上限:先清过期桶,再逐个淘汰最旧的桶。
// 淘汰会丢失该 IP 的计数,相当于给被淘汰 IP 重新计时;只在高基数攻击
// 下发生,可接受,总比内存无限增长好。
func (l *ipRateLimiter) enforceBucketLimit() {
	limit := int64(l.bucketLimit())
	if l.bucketCount.Load() <= limit {
		return
	}
	l.evictMu.Lock()
	defer l.evictMu.Unlock()
	if l.bucketCount.Load() <= limit {
		return
	}

	cutoff := time.Now().Add(-l.window)
	// 第一轮:只删已过期的桶(窗口内无请求),不牺牲仍在活跃的 IP。
	l.buckets.Range(func(k, v any) bool {
		b := v.(*ipBucket)
		b.mu.Lock()
		expired := b.lastSeen.IsZero() || b.lastSeen.Before(cutoff)
		b.mu.Unlock()
		if expired && l.buckets.CompareAndDelete(k, v) {
			l.bucketCount.Add(-1)
		}
		return l.bucketCount.Load() > limit
	})

	// 第二轮:仍然超限,每次挑 lastSeen 最早的桶淘汰。
	for l.bucketCount.Load() > limit {
		var oldestKey, oldestVal any
		var oldestSeen time.Time
		l.buckets.Range(func(k, v any) bool {
			b := v.(*ipBucket)
			b.mu.Lock()
			seen := b.lastSeen
			b.mu.Unlock()
			if oldestKey == nil || seen.Before(oldestSeen) {
				oldestKey, oldestVal, oldestSeen = k, v, seen
			}
			return true
		})
		if oldestKey == nil {
			return
		}
		if l.buckets.CompareAndDelete(oldestKey, oldestVal) {
			l.bucketCount.Add(-1)
		}
	}
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

	b := l.bucketFor(ip, now)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastSeen = now

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
			drop := b.lastSeen.IsZero() || b.lastSeen.Before(cutoff)
			b.mu.Unlock()
			if drop && l.buckets.CompareAndDelete(k, v) {
				l.bucketCount.Add(-1)
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
