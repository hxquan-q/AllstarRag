package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
)

// auth_public_ratelimit_test.go 覆盖 PublicAuthRateLimitWith 的滑窗语义:
// 边界(第 max 次通过、第 max+1 次 429)、窗口滑过恢复、不同 IP 独立桶、
// 空 IP 共用桶,以及旧 PublicAuthRateLimit 的 30 次/分钟默认值不变。

func newRateLimitTestServer(t *testing.T, rl gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// ErrorHandler 把 c.Error() 里的 AppError 渲染成项目统一错误包,
	// 这样测试断言的是真实响应的 429 与错误码,而不是中间件内部状态。
	r.Use(ErrorHandler())
	r.POST("/probe", rl, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	return r
}

func hitRateLimitProbe(r *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type rateLimitErrorBody struct {
	Success bool `json:"success"`
	Error   struct {
		Code    apperrors.ErrorCode `json:"code"`
		Message string              `json:"message"`
	} `json:"error"`
}

func TestPublicAuthRateLimitWith_SlidingWindowBoundary(t *testing.T) {
	srv := newRateLimitTestServer(t,
		PublicAuthRateLimitWith(time.Hour, 5, "test-boundary"))
	const ip = "203.0.113.10:4000"

	// 第 1-5 次应放行。
	for i := 1; i <= 5; i++ {
		if got := hitRateLimitProbe(srv, ip).Code; got != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, got)
		}
	}

	// 第 6 次应被拒,且返回项目统一错误包里的 1006。
	w := hitRateLimitProbe(srv, ip)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: status = %d, want 429", w.Code)
	}
	var body rateLimitErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 429 body: %v (body=%q)", err, w.Body.String())
	}
	if body.Success {
		t.Fatalf("429 body success = true, want false")
	}
	if body.Error.Code != apperrors.ErrTooManyRequests {
		t.Fatalf("429 error code = %d, want %d", body.Error.Code, apperrors.ErrTooManyRequests)
	}
	if !strings.Contains(body.Error.Message, "too many requests") {
		t.Fatalf("429 message = %q, want it to mention \"too many requests\"", body.Error.Message)
	}
}

func TestPublicAuthRateLimitWith_WindowRecovery(t *testing.T) {
	// 直接构造 limiter,避免依赖 gin 上下文;窗口取 50ms,睡 120ms 足够
	// 让旧时间戳全部滑出窗口,同时留有充足余量避免 CI 抖动。
	limiter := &ipRateLimiter{window: 50 * time.Millisecond, max: 2}
	const ip = "198.51.100.7"

	for i := 1; i <= 2; i++ {
		if !limiter.allow(ip) {
			t.Fatalf("request %d: want allowed", i)
		}
	}
	if limiter.allow(ip) {
		t.Fatal("3rd request within window: want denied")
	}

	time.Sleep(120 * time.Millisecond)

	if !limiter.allow(ip) {
		t.Fatal("request after window slipped: want allowed")
	}
}

func TestPublicAuthRateLimitWith_DistinctIPsIndependent(t *testing.T) {
	limiter := &ipRateLimiter{window: time.Hour, max: 2}
	const ipA = "192.0.2.1"
	const ipB = "192.0.2.2"

	for i := 1; i <= 2; i++ {
		if !limiter.allow(ipA) {
			t.Fatalf("ipA request %d: want allowed", i)
		}
		if !limiter.allow(ipB) {
			t.Fatalf("ipB request %d: want allowed", i)
		}
	}

	if limiter.allow(ipA) {
		t.Fatal("ipA 3rd request: want denied")
	}
	if limiter.allow(ipB) {
		t.Fatal("ipB 3rd request: want denied")
	}
}

func TestPublicAuthRateLimitWith_EmptyIPSharesBucket(t *testing.T) {
	// 空 IP(代理没传 X-Forwarded-For)按既有实现落到同一个共享桶,
	// 宁可略严也不放开限制。
	limiter := &ipRateLimiter{window: time.Hour, max: 2}

	if !limiter.allow("") {
		t.Fatal("1st empty-IP request: want allowed")
	}
	if !limiter.allow("") {
		t.Fatal("2nd empty-IP request: want allowed")
	}
	if limiter.allow("") {
		t.Fatal("3rd empty-IP request: want denied")
	}
}

func TestPublicAuthRateLimit_DefaultBudgetUnchanged(t *testing.T) {
	// 旧入口保持 30 次/分钟:第 30 次放行,第 31 次 429。
	srv := newRateLimitTestServer(t, PublicAuthRateLimit())
	const ip = "198.51.100.99:5000"

	for i := 1; i <= publicAuthRateLimitMax; i++ {
		if got := hitRateLimitProbe(srv, ip).Code; got != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, got)
		}
	}
	if got := hitRateLimitProbe(srv, ip).Code; got != http.StatusTooManyRequests {
		t.Fatalf("request %d: status = %d, want 429", publicAuthRateLimitMax+1, got)
	}
}
