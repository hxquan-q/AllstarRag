package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
)

// auth_login_ratelimit_t12_test.go 是 T12 对 R1-3 的独立复验:
//  1. 日志里的邮箱掩码形如 a***@domain|ip(只影响展示);
//  2. 计数 key 仍是完整邮箱|IP——两个「掩码后相同」的邮箱必须有独立预算,
//     否则说明计数也被掩码污染了。

// resetLoginAccountLimiterForTest 重置包级账号限速器。
// 包级 limiter 会跨用例、跨 `go test -count=N` 轮次保留状态;固定邮箱的
// 用例在第二轮就会直接 429。重置只清状态,不改变任何断言。
func resetLoginAccountLimiterForTest() {
	loginAccountLimiter = middleware.NewKeyedRateLimiter(
		loginAccountRateLimitWindow, loginAccountRateLimitMax)
}

func TestT12_R1_3_LogMaskShape(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"bob.smith@corp.example|10.1.2.3", "b***@corp.example|10.1.2.3"},
		{"Alice@Example.COM|198.51.100.7", "A***@Example.COM|198.51.100.7"},
		{"张三@example.com|127.0.0.1", "张***@example.com|127.0.0.1"},
		// 无 @ 时把整串当本地部分,仍然只留首字符。
		{"localonly|86.75.30.9", "l***|86.75.30.9"},
		// 空本地部分不泄露任何字符。
		{"@example.com|10.0.0.1", "***@example.com|10.0.0.1"},
		{"", "***"},
	}
	for _, tt := range tests {
		if got := maskLoginRateLimitKey(tt.key); got != tt.want {
			t.Fatalf("maskLoginRateLimitKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestT12_R1_3_MaskedCollisionKeepsDistinctBudgets(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)

	h, us := newLoginTestHandler(func(_ context.Context, _ *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded
	})
	srv := newLoginRateLimitTestRouter(h)
	const ip = "203.0.113.71:4321"
	// 两者掩码后都是 a***@example.com;若计数 key 被掩码,它们会共享预算。
	const alice = "alice@example.com"
	const amy = "amy@example.com"

	for i := 1; i <= 5; i++ {
		if got := doLogin(t, srv, ip, alice).Code; got != http.StatusUnauthorized {
			t.Fatalf("alice attempt %d: status = %d, want 401", i, got)
		}
	}
	if got := doLogin(t, srv, ip, alice).Code; got != http.StatusTooManyRequests {
		t.Fatalf("alice 6th attempt: status = %d, want 429", got)
	}

	// amy 与 alice 掩码相同但计数 key 不同:首次必须走到认证(401),不是 429。
	w := doLogin(t, srv, ip, amy)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("amy first attempt: status = %d, want 401 (masked key must not be the counting key); body=%s",
			w.Code, w.Body.String())
	}
	if got := us.calls.Load(); got != 6 {
		t.Fatalf("userService.Login calls = %d, want 6 (alice 5 + amy 1)", got)
	}
}
