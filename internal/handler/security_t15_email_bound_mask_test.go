package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// security_t15_email_bound_mask_test.go 是 T15 对 T14 微修的独立复验。
// 不复用 T14 自带用例的结论,而是从票面 AC 重新构造断言:
//  1. 超长邮箱(>320)在 login / register / register-by-invite 均返回 400,
//     且在 login 上不进入账号维度限速、在三个入口都不调用 UserService;
//  2. 邮箱本地部分含 `|` 时,掩码按最后一个 `|` 切分,IP 段原样保留;
//  3. 计数 key 没有被掩码污染(两个掩码同形但原文不同的 `|` 邮箱预算独立);
//  4. 空 / 无 @ / 中文 / 纯竖线等掩码边界不 panic。

// t15OverlongEmail 生成 412 字符的合法邮箱形状(本地部分 400 字符),
// 超过 binding 的 max=320。
func t15OverlongEmail() string {
	return strings.Repeat("a", 400) + "@example.com"
}

// t15EmailOfLength 生成总长度恰为 n 的合法邮箱(域名固定 @example.com,12 字符)。
func t15EmailOfLength(n int) string {
	return strings.Repeat("a", n-len("@example.com")) + "@example.com"
}

func TestT15_Login_OverlongEmail400AndNoAccountLimit(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)

	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded
	})
	srv := newLoginRateLimitTestRouter(h)
	const ip = "192.0.2.201:4001"

	// 连打 7 次(超过账号维度预算 5):若 binding 之后才进限速,第 6 次会是 429。
	// 全部 400 说明超长邮箱在限速之前就被拒绝,不会创建限速桶。
	for i := 1; i <= 7; i++ {
		w := doLogin(t, srv, ip, t15OverlongEmail())
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status = %d, want 400 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	if got := us.calls.Load(); got != 0 {
		t.Fatalf("userService.Login calls = %d, want 0", got)
	}
}

func TestT15_Login_EmailLengthBoundary320(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)

	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded
	})
	srv := newLoginRateLimitTestRouter(h)
	const ip = "192.0.2.202:4002"

	// 恰好 320 是上限内:应通过 binding 进入认证(这里桩返回失败 → 401)。
	if w := doLogin(t, srv, ip, t15EmailOfLength(320)); w.Code != http.StatusUnauthorized {
		t.Fatalf("len=320: status = %d, want 401 (must pass binding); body=%s", w.Code, w.Body.String())
	}
	// 321 越过上限:400,且不再进入认证。
	if w := doLogin(t, srv, ip, t15EmailOfLength(321)); w.Code != http.StatusBadRequest {
		t.Fatalf("len=321: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := us.calls.Load(); got != 1 {
		t.Fatalf("userService.Login calls = %d, want 1 (only the 320-byte request)", got)
	}
}

func TestT15_Register_OverlongEmail400NoUserService(t *testing.T) {
	called := false
	us := &stubRegisterUserService{register: func(context.Context, *types.RegisterRequest) (*types.User, error) {
		called = true
		return &types.User{ID: "u1"}, nil
	}}
	h := &AuthHandler{
		configInfo:  &config.Config{Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe}},
		userService: us,
	}
	srv := newRegisterTestRouter(h)

	body := validRegisterBody()
	body["email"] = t15OverlongEmail()
	if w := doRegister(t, srv, body); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("UserService.Register must not be called for an overlong email")
	}
}

func TestT15_Register_EmailLengthBoundary320(t *testing.T) {
	calls := 0
	us := &stubRegisterUserService{register: func(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
		calls++
		return &types.User{ID: "u1", Email: req.Email}, nil
	}}
	h := &AuthHandler{
		configInfo:  &config.Config{Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe}},
		userService: us,
	}
	srv := newRegisterTestRouter(h)

	body := validRegisterBody()
	body["email"] = t15EmailOfLength(320)
	if w := doRegister(t, srv, body); w.Code != http.StatusCreated {
		t.Fatalf("len=320: status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	body["email"] = t15EmailOfLength(321)
	if w := doRegister(t, srv, body); w.Code != http.StatusBadRequest {
		t.Fatalf("len=321: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("UserService.Register calls = %d, want 1 (only the 320-byte request)", calls)
	}
}

// t15InviteUserService 统计 register-by-invite 分支是否触达 UserService。
type t15InviteUserService struct {
	interfaces.UserService
	registerCalls int
	lookupCalls   int
}

func (s *t15InviteUserService) GetUserByEmail(context.Context, string) (*types.User, error) {
	s.lookupCalls++
	return nil, nil
}

func (s *t15InviteUserService) Register(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
	s.registerCalls++
	return &types.User{ID: "u1", Email: req.Email}, nil
}

func (s *t15InviteUserService) UpdateUser(context.Context, *types.User) error { return nil }

func (s *t15InviteUserService) GenerateTokens(context.Context, *types.User) (string, string, error) {
	return "access", "refresh", nil
}

// t15InviteService 统计邀请 token 是否被查询。
type t15InviteService struct {
	interfaces.TenantInvitationService
	lookups int
}

func (s *t15InviteService) LookupByToken(context.Context, string) (*types.TenantInvitation, error) {
	s.lookups++
	return &types.TenantInvitation{TenantID: 42, Role: types.TenantRoleViewer}, nil
}

func (s *t15InviteService) AcceptByToken(context.Context, string, string) (*types.TenantMember, error) {
	return &types.TenantMember{TenantID: 42, Role: types.TenantRoleViewer}, nil
}

type t15TenantService struct{ interfaces.TenantService }

func (s *t15TenantService) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: 42, Name: "T15 Workspace"}, nil
}

func t15RegisterByInviteRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/auth/register-by-invite", h.RegisterByInvite)
	return r
}

func t15DoRegisterByInvite(t *testing.T, r *gin.Engine, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/register-by-invite", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestT15_RegisterByInvite_OverlongEmail400NoService(t *testing.T) {
	users := &t15InviteUserService{}
	invites := &t15InviteService{}
	h := &AuthHandler{
		configInfo:    &config.Config{Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe}},
		userService:   users,
		tenantService: &t15TenantService{},
		invitationSvc: invites,
	}
	srv := t15RegisterByInviteRouter(h)

	body := map[string]string{
		"token":    "invite-token",
		"email":    t15OverlongEmail(),
		"username": "alice",
		"password": "supersecret1",
	}
	if w := t15DoRegisterByInvite(t, srv, body); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	// binding 在邀请查询与用户服务之前失败。
	if invites.lookups != 0 {
		t.Fatalf("invitation lookups = %d, want 0", invites.lookups)
	}
	if users.lookupCalls != 0 || users.registerCalls != 0 {
		t.Fatalf("userService calls lookup=%d register=%d, want 0/0",
			users.lookupCalls, users.registerCalls)
	}
}

func TestT15_RegisterByInvite_EmailLengthBoundary320(t *testing.T) {
	users := &t15InviteUserService{}
	h := &AuthHandler{
		configInfo:    &config.Config{Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe}},
		userService:   users,
		tenantService: &t15TenantService{},
		invitationSvc: &t15InviteService{},
	}
	srv := t15RegisterByInviteRouter(h)

	body := map[string]string{
		"token":    "invite-token",
		"email":    t15EmailOfLength(320),
		"username": "alice",
		"password": "supersecret1",
	}
	if w := t15DoRegisterByInvite(t, srv, body); w.Code != http.StatusCreated {
		t.Fatalf("len=320: status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	body["email"] = t15EmailOfLength(321)
	if w := t15DoRegisterByInvite(t, srv, body); w.Code != http.StatusBadRequest {
		t.Fatalf("len=321: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if users.registerCalls != 1 {
		t.Fatalf("UserService.Register calls = %d, want 1 (only the 320-byte request)", users.registerCalls)
	}
}

func TestT15_MaskUsesLastPipe(t *testing.T) {
	// 票面要求:邮箱本地部分含 `|` 时按最后一个 `|` 切分,IP 段原样保留。
	cases := []struct{ key, want string }{
		{"a|b@example.com|1.2.3.4", "a***@example.com|1.2.3.4"},
		{"a|b|1.2.3.4", "a***|1.2.3.4"},
		{"foo|bar@example.com|2001:db8::1", "f***@example.com|2001:db8::1"},
	}
	for _, tt := range cases {
		if got := maskLoginRateLimitKey(tt.key); got != tt.want {
			t.Fatalf("maskLoginRateLimitKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}

	// 性质断言:最后一个 `|` 及其后的 IP 段必须逐字保留。
	const key = "a|b@example.com|1.2.3.4"
	cut := strings.LastIndex(key, "|")
	got := maskLoginRateLimitKey(key)
	if !strings.HasSuffix(got, key[cut:]) {
		t.Fatalf("masked %q lost IP suffix %q", got, key[cut:])
	}
	if strings.Contains(got, "b@example.com") {
		t.Fatalf("masked %q still leaks the local-part remainder", got)
	}
}

func TestT15_MaskEdgeCasesDoNotPanic(t *testing.T) {
	cases := []struct{ key, want string }{
		{"", "***"},
		{"no-at", "n***"},
		{"中", "中***"},
		{"@", "***@"},
		{"|", "***|"},
		{"||", "|***|"},
		{"|10.0.0.1", "***|10.0.0.1"},
		{"中文|中文@例え.测试|1.2.3.4", "中***@例え.测试|1.2.3.4"},
	}
	for _, tt := range cases {
		if got := maskLoginRateLimitKey(tt.key); got != tt.want {
			t.Fatalf("maskLoginRateLimitKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestT15_MaskCollisionKeepsDistinctCountKeys(t *testing.T) {
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)

	h, us := newLoginTestHandler(func(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
		return nil, context.DeadlineExceeded
	})
	srv := newLoginRateLimitTestRouter(h)
	const ip = "192.0.2.210:4010"
	const ipHost = "192.0.2.210"
	const first = "a|b@example.com"
	const second = "a|c@example.com"

	// 前提:两者日志掩码同形;若计数 key 也被掩码,预算就会共享。
	if maskLoginRateLimitKey(first+"|"+ipHost) != maskLoginRateLimitKey(second+"|"+ipHost) {
		t.Fatal("masked keys differ; the collision premise does not hold")
	}

	for i := 1; i <= 5; i++ {
		if got := doLogin(t, srv, ip, first).Code; got != http.StatusUnauthorized {
			t.Fatalf("first attempt %d: status = %d, want 401", i, got)
		}
	}
	if got := doLogin(t, srv, ip, first).Code; got != http.StatusTooManyRequests {
		t.Fatalf("first 6th attempt: status = %d, want 429", got)
	}

	// second 掩码同形但计数 key 不同:首次必须到认证(401),不是 429。
	if got := doLogin(t, srv, ip, second).Code; got != http.StatusUnauthorized {
		t.Fatalf("second first attempt: status = %d, want 401 (count key must not be masked)", got)
	}
	if got := us.calls.Load(); got != 6 {
		t.Fatalf("userService.Login calls = %d, want 6 (first 5 + second 1)", got)
	}
}
