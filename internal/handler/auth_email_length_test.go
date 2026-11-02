package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

// auth_email_length_test.go 覆盖 T14 的邮箱长度上限:
// LoginRequest / RegisterRequest 的 binding 增加 max=320,
// 超长邮箱直接 400,不进入限速桶表或用户服务。

func overlongEmail() string {
	// 321 字符以上且仍是合法邮箱形状(长本地部分),用于触发 max=320。
	return strings.Repeat("a", 400) + "@example.com"
}

func TestLogin_RejectsOverlongEmail(t *testing.T) {
	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return &types.LoginResponse{Success: true}, nil
	})
	srv := newLoginRateLimitTestRouter(h)

	w := doLogin(t, srv, "203.0.113.90:1234", overlongEmail())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if got := us.calls.Load(); got != 0 {
		t.Fatalf("userService.Login calls = %d, want 0 (binding must reject before auth)", got)
	}
}

func TestRegister_RejectsOverlongEmail(t *testing.T) {
	called := false
	us := &stubRegisterUserService{register: func(context.Context, *types.RegisterRequest) (*types.User, error) {
		called = true
		return &types.User{ID: "u1"}, nil
	}}
	h := &AuthHandler{
		configInfo: &config.Config{
			Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
		},
		userService: us,
	}
	srv := newRegisterTestRouter(h)

	body := validRegisterBody()
	body["email"] = overlongEmail()
	w := doRegister(t, srv, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("UserService.Register must not be called for an overlong email")
	}
}
