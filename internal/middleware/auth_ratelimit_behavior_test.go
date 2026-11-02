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

// auth_ratelimit_behavior_test.go 是 T02 的独立行为测试:只通过导出的
// 构造函数 PublicAuthRateLimitWith / PublicAuthRateLimit 挂到真实 gin
// 引擎上发 HTTP 请求,不直接构造 ipRateLimiter,也不复用实现内部判定
// 逻辑。目的是独立复验 S1 的对外行为。

func newBehaviorProbeEngine(t *testing.T, rl gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ErrorHandler())
	r.POST("/probe", rl, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func postBehaviorProbe(srv *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

type behaviorErrorBody struct {
	Success bool `json:"success"`
	Error   struct {
		Code    apperrors.ErrorCode `json:"code"`
		Message string              `json:"message"`
	} `json:"error"`
}

// assertRateLimited 断言响应确为项目统一的 429 错误包(1006 + 明确 message)。
func assertRateLimited(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("%s: status = %d, want 429 (body=%q)", label, w.Code, w.Body.String())
	}
	var body behaviorErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode 429 body: %v (body=%q)", label, err, w.Body.String())
	}
	if body.Success {
		t.Fatalf("%s: 429 body success = true, want false", label)
	}
	if body.Error.Code != apperrors.ErrTooManyRequests {
		t.Fatalf("%s: error code = %d, want %d", label, body.Error.Code, apperrors.ErrTooManyRequests)
	}
	if !strings.Contains(strings.ToLower(body.Error.Message), "too many requests") {
		t.Fatalf("%s: message = %q, want explicit \"too many requests\"", label, body.Error.Message)
	}
}

// TestBehaviorLoginBudgetIs5PerMinute 覆盖 AC S1:login 5 次/60s/IP,
// 第 5 次放行、第 6 次 429 + 1006。
func TestBehaviorLoginBudgetIs5PerMinute(t *testing.T) {
	srv := newBehaviorProbeEngine(t, PublicAuthRateLimitWith(60*time.Second, 5, "behavior-login"))
	const ip = "203.0.113.40:5100"

	for i := 1; i <= 5; i++ {
		if got := postBehaviorProbe(srv, ip).Code; got != http.StatusOK {
			t.Fatalf("login request %d: status = %d, want 200", i, got)
		}
	}
	assertRateLimited(t, postBehaviorProbe(srv, ip), "login 6th request")
}

// TestBehaviorShortWindowExpiryRestoresBudget 覆盖 AC S1:窗口物理滑过后
// 自动恢复,不需要账号锁定。用 200ms 短窗口真实等待过期。
func TestBehaviorShortWindowExpiryRestoresBudget(t *testing.T) {
	srv := newBehaviorProbeEngine(t,
		PublicAuthRateLimitWith(200*time.Millisecond, 2, "behavior-short-window"))
	const ip = "198.51.100.20:5200"

	for i := 1; i <= 2; i++ {
		if got := postBehaviorProbe(srv, ip).Code; got != http.StatusOK {
			t.Fatalf("request %d before expiry: status = %d, want 200", i, got)
		}
	}
	assertRateLimited(t, postBehaviorProbe(srv, ip), "3rd request inside window")

	// 等窗口物理过期(200ms + 余量),不靠内部状态重置。
	time.Sleep(350 * time.Millisecond)

	if got := postBehaviorProbe(srv, ip).Code; got != http.StatusOK {
		t.Fatalf("request after window expiry: status = %d, want 200", got)
	}
}

// TestBehaviorIPBucketsAreIndependent 覆盖 AC S1:不同 IP 独立桶。
func TestBehaviorIPBucketsAreIndependent(t *testing.T) {
	srv := newBehaviorProbeEngine(t, PublicAuthRateLimitWith(time.Hour, 2, "behavior-per-ip"))
	const ipA = "192.0.2.71:5300"
	const ipB = "192.0.2.72:5300"

	for i := 1; i <= 2; i++ {
		if got := postBehaviorProbe(srv, ipA).Code; got != http.StatusOK {
			t.Fatalf("ipA request %d: status = %d, want 200", i, got)
		}
		if got := postBehaviorProbe(srv, ipB).Code; got != http.StatusOK {
			t.Fatalf("ipB request %d: status = %d, want 200", i, got)
		}
	}
	assertRateLimited(t, postBehaviorProbe(srv, ipA), "ipA 3rd request")
	assertRateLimited(t, postBehaviorProbe(srv, ipB), "ipB 3rd request")
}

// TestBehaviorEmptyIPSharesOneBucket 覆盖 AC S1:取不到客户端 IP 时,
// 所有请求落到同一个共享桶(宁可略严也不放开)。
func TestBehaviorEmptyIPSharesOneBucket(t *testing.T) {
	srv := newBehaviorProbeEngine(t, PublicAuthRateLimitWith(time.Hour, 2, "behavior-empty-ip"))

	// RemoteAddr 为空 → gin ClientIP() 返回空串,走 "_unknown_" 共享桶。
	if got := postBehaviorProbe(srv, "").Code; got != http.StatusOK {
		t.Fatalf("1st empty-IP request: status = %d, want 200", got)
	}
	if got := postBehaviorProbe(srv, "").Code; got != http.StatusOK {
		t.Fatalf("2nd empty-IP request: status = %d, want 200", got)
	}
	assertRateLimited(t, postBehaviorProbe(srv, ""), "3rd empty-IP request")
}

// TestBehaviorLoginAndRegisterBudgetsDoNotShare 覆盖 AC S1:login 与
// register 各自独立计数、不共享预算。这里用两个同窗口/不同上限的
// 导出构造模拟两类端点(60s/5 与 1h/3),同一 IP 分别打满。
func TestBehaviorLoginAndRegisterBudgetsDoNotShare(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := gin.New()
	srv.Use(ErrorHandler())
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	srv.POST("/login",
		PublicAuthRateLimitWith(60*time.Second, 5, "behavior-login-iso"), ok)
	srv.POST("/register",
		PublicAuthRateLimitWith(time.Hour, 3, "behavior-register-iso"), ok)

	const ip = "203.0.113.80:5400"
	post := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}

	// 先把 login 打满 6 次(5 放行 + 1 拒绝)。
	for i := 1; i <= 5; i++ {
		if got := post("/login"); got != http.StatusOK {
			t.Fatalf("login request %d: status = %d, want 200", i, got)
		}
	}
	if got := post("/login"); got != http.StatusTooManyRequests {
		t.Fatalf("login 6th request: status = %d, want 429", got)
	}

	// register 桶应完全未受影响:3 次放行,第 4 次才 429。
	for i := 1; i <= 3; i++ {
		if got := post("/register"); got != http.StatusOK {
			t.Fatalf("register request %d after login exhaustion: status = %d, want 200", i, got)
		}
	}
	if got := post("/register"); got != http.StatusTooManyRequests {
		t.Fatalf("register 4th request: status = %d, want 429", got)
	}
}

// TestBehaviorLegacyShareLinkBudgetStays30PerMinute 覆盖 AC S1:旧的
// PublicAuthRateLimit() 仍是 30 次/分钟,第 31 次 429。
func TestBehaviorLegacyShareLinkBudgetStays30PerMinute(t *testing.T) {
	srv := newBehaviorProbeEngine(t, PublicAuthRateLimit())
	const ip = "198.51.100.90:5500"

	for i := 1; i <= 30; i++ {
		if got := postBehaviorProbe(srv, ip).Code; got != http.StatusOK {
			t.Fatalf("legacy request %d: status = %d, want 200", i, got)
		}
	}
	assertRateLimited(t, postBehaviorProbe(srv, ip), "legacy 31st request")
}
