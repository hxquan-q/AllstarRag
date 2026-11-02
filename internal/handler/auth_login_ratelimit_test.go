package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// auth_login_ratelimit_test.go 覆盖 Login handler 的账号维度限速:
// 同一邮箱+IP 第 6 次尝试返回 429、不同邮箱不受影响、成功登录同样计数。
// 每个用例使用独立邮箱,避免包级 loginAccountLimiter 在多个测试间串扰。

type stubLoginUserService struct {
	interfaces.UserService
	calls atomic.Int32
	login func(ctx context.Context, req *types.LoginRequest) (*types.LoginResponse, error)
}

func (s *stubLoginUserService) Login(
	ctx context.Context, req *types.LoginRequest,
) (*types.LoginResponse, error) {
	s.calls.Add(1)
	return s.login(ctx, req)
}

func newLoginRateLimitTestRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 复用同包 errorCapture:把 c.Error() 里的 AppError 渲染成真实 HTTP 状态。
	r.Use(errorCapture())
	r.POST("/auth/login", h.Login)
	return r
}

func doLogin(t *testing.T, r *gin.Engine, remoteAddr, email string) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": "wrongpassword",
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newLoginTestHandler(
	login func(context.Context, *types.LoginRequest) (*types.LoginResponse, error),
) (*AuthHandler, *stubLoginUserService) {
	us := &stubLoginUserService{login: login}
	return &AuthHandler{userService: us}, us
}

func TestLogin_SameAccountAndIPThrottled(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)
	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded // 任意失败,模拟密码错误
	})
	srv := newLoginRateLimitTestRouter(h)
	const email = "t06-throttle@example.com"
	const ip = "203.0.113.61:1234"

	for i := 1; i <= 5; i++ {
		if got := doLogin(t, srv, ip, email).Code; got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, got)
		}
	}

	w := doLogin(t, srv, ip, email)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429", w.Code)
	}
	if !strings.Contains(w.Body.String(), "too many requests") {
		t.Fatalf("6th attempt body = %q, want it to mention \"too many requests\"", w.Body.String())
	}
	if got := us.calls.Load(); got != 5 {
		t.Fatalf("userService.Login calls = %d, want 5 (6th must be rejected before auth)", got)
	}
}

func TestLogin_DifferentAccountsSameIPIndependent(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)
	h, _ := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded
	})
	srv := newLoginRateLimitTestRouter(h)
	const ip = "203.0.113.62:1234"
	const emailA = "t06-iso-a@example.com"
	const emailB = "t06-iso-b@example.com"

	for i := 1; i <= 5; i++ {
		if got := doLogin(t, srv, ip, emailA).Code; got != http.StatusUnauthorized {
			t.Fatalf("emailA attempt %d: status = %d, want 401", i, got)
		}
	}
	if got := doLogin(t, srv, ip, emailA).Code; got != http.StatusTooManyRequests {
		t.Fatalf("emailA 6th attempt: status = %d, want 429", got)
	}

	// 同一 IP 上另一个账号仍然可以正常走到认证(401 而不是 429)。
	if got := doLogin(t, srv, ip, emailB).Code; got != http.StatusUnauthorized {
		t.Fatalf("emailB first attempt: status = %d, want 401", got)
	}
}

func TestLogin_SuccessfulLoginsAlsoCount(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)
	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return &types.LoginResponse{Success: true}, nil
	})
	srv := newLoginRateLimitTestRouter(h)
	const email = "t06-success@example.com"
	const ip = "203.0.113.63:1234"

	for i := 1; i <= 5; i++ {
		if got := doLogin(t, srv, ip, email).Code; got != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want 200", i, got)
		}
	}
	if got := doLogin(t, srv, ip, email).Code; got != http.StatusTooManyRequests {
		t.Fatalf("6th successful login: status = %d, want 429 (success must not reset the budget)", got)
	}
	if got := us.calls.Load(); got != 5 {
		t.Fatalf("userService.Login calls = %d, want 5", got)
	}
}
