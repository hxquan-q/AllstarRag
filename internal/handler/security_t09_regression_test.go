package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// security_t09_regression_test.go 是 T09 的独立回归:不复用实现方新增的
// 行为断言,而是从交替路径验证同一组业务不变量。
//  1. 真实 service+repo(sqlite)驱动创建接口:被移除用户 403、重新邀请后恢复;
//  2. 平台 API key 走目录管理路径,完全绕过撤销判定;
//  3. 登录账号维度限速的 key 由「邮箱|ClientIP」决定:未受信来源伪造
//     X-Forwarded-For 不改变 key,受信代理转发的不同 XFF 才是不同 key;
//  4. swagger 契约:登录/注册声明 429,capabilities 含 access_revoked。

const t09RevokedUser = "t09-removed-user"

func newT09RevokedMemberService(t *testing.T) (interfaces.TenantMemberService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := apprepo.NewTenantMemberRepository(db)
	return service.NewTenantMemberService(repo, nil, nil, nil), db
}

func t09AddMembership(t *testing.T, db *gorm.DB, userID string, tenantID uint64, deleted bool) {
	t.Helper()
	// CountDeletedByUser 只统计所属空间仍存在的软删行,夹具需先建空间。
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&types.Tenant{
		ID:     tenantID,
		Name:   "workspace",
		Status: "active",
	}).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	member := &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if deleted {
		if err := db.Delete(&types.TenantMember{}, member.ID).Error; err != nil {
			t.Fatalf("soft delete membership: %v", err)
		}
	}
}

func newT09CreateTenantRouter(h *TenantHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 用生产同款错误中间件,断言真实的 code/details 形状。
	r.Use(middleware.ErrorHandler())
	r.POST("/tenants", h.CreateTenant)
	return r
}

func t09PostTenant(t *testing.T, r *gin.Engine, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestT09_CreateTenant_RealServiceRejectsRevokedUser(t *testing.T) {
	memberSvc, db := newT09RevokedMemberService(t)
	// 用户曾经属于该空间,但已被移除(只有软删行,没有活跃行)。
	t09AddMembership(t, db, t09RevokedUser, 1, true)

	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &revokedTenantUserService{user: &types.User{ID: t09RevokedUser, TenantID: 1}},
		memberService:    memberSvc,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := t09PostTenant(t, newT09CreateTenantRouter(h), `{"name":"should-not-exist"}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":1002`) {
		t.Fatalf("want forbidden code 1002, body=%s", body)
	}
	if !strings.Contains(body, `"details":"access_revoked"`) {
		t.Fatalf("want stable detail access_revoked, body=%s", body)
	}
}

func TestT09_CreateTenant_ReinvitedUserRecovers(t *testing.T) {
	// T02 修正:旧版用 db.Create 直接塞活跃行冒充「重新邀请被接受」,
	// 属伪覆盖。现改走真实链路:owner 建邀请 → 被移除用户调 accept 端点
	// (AddMember 新插活跃行)后,撤销状态应解除(POST /tenants 403→201)。
	const removedEmail = "t09-removed-user@example.com"
	env := newRejoinEnv(t)
	env.seedRemovedViewer(t, 1, t09RevokedUser, removedEmail)

	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &revokedTenantUserService{user: &types.User{ID: t09RevokedUser, TenantID: 1}},
		memberService:    env.members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}
	router := newT09CreateTenantRouter(h)

	// 恢复前:403。
	if w := t09PostTenant(t, router, `{"name":"before-reinvite"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("before re-invite: status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}

	// 重新邀请被接受:真实邀请 + accept 端点完成恢复。
	invID := env.rejoinCreatePerUserInvitation(t, 1, removedEmail)
	env.rejoinAcceptPerUser(t, invID, t09RevokedUser)

	w := t09PostTenant(t, router, `{"name":"after-reinvite"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("after re-invite: status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant called %d times, want 1", tenants.createCalls)
	}
}

func TestT09_CreateTenant_PlatformAPIKeyBypassesRevocation(t *testing.T) {
	// 平台 API key 属于目录管理路径:即使成员判定为撤销也不应查询/拦截。
	members := &revokedMemberService{revoked: true}
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &revokedTenantUserService{user: &types.User{ID: "platform-caller"}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := t09PostTenant(t, newT09CreateTenantRouter(h), `{"name":"platform-workspace"}`, func(req *http.Request) {
		ctx := types.WithTenantAPIKeyScope(req.Context(), types.TenantAPIKeyScope{
			ScopeType: types.APIKeyScopePlatform,
		})
		*req = *req.WithContext(ctx)
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if members.revokedCalls != 0 {
		t.Fatalf("HasRevokedAccess must not run for platform API keys, called %d times", members.revokedCalls)
	}
}

type t09LoginStub struct {
	interfaces.UserService
	calls atomic.Int32
}

func (s *t09LoginStub) Login(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
	s.calls.Add(1)
	return nil, context.DeadlineExceeded // 统一失败,只观察限速层
}

func newT09LoginRouter(h *AuthHandler, trustedProxies []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		panic(err)
	}
	r.Use(middleware.ErrorHandler())
	r.POST("/auth/login", h.Login)
	return r
}

func t09Login(t *testing.T, r *gin.Engine, remoteAddr, forwardedFor, email string) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(map[string]string{"email": email, "password": "wrongpassword"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestT09_Login_SpoofedXFFDoesNotChangeKey(t *testing.T) {
	// 包级 limiter 跨 `go test -count=N` 保留状态,固定邮箱第二轮会直接 429;
	// 先重置只清状态,不改变下面的断言。
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)
	// 只信任回环代理:来自不可信来源的 XFF 必须被忽略,同一账号依旧共享
	// 一个「邮箱|真实来源 IP」桶,6 次后 429(防止伪造 XFF 绕过限速)。
	us := &t09LoginStub{}
	h := &AuthHandler{userService: us}
	r := newT09LoginRouter(h, []string{"127.0.0.1/32"})
	const email = "t09-xff-spoof@example.com"
	const realRemote = "198.51.100.10:5555"

	for i := 1; i <= 5; i++ {
		w := t09Login(t, r, realRemote, fmt.Sprintf("203.0.113.%d", 10+i), email)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	w := t09Login(t, r, realRemote, "203.0.113.99", email)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt with rotated XFF: status = %d, want 429 (body=%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":1006`) {
		t.Fatalf("429 body missing code 1006: %s", body)
	}
	if !strings.Contains(body, "too many requests") {
		t.Fatalf("429 body missing message: %s", body)
	}
	if got := us.calls.Load(); got != 5 {
		t.Fatalf("auth reached %d times, want 5 (6th must be stopped by the limiter)", got)
	}
}

func TestT09_Login_TrustedProxyXFFSeparatesAccounts(t *testing.T) {
	// 同样隔离包级 limiter,保证 -count>1 时预算从零开始。
	resetLoginAccountLimiterForTest()
	t.Cleanup(resetLoginAccountLimiterForTest)
	// 受信代理(回环)转发的真实客户端 IP 应形成不同 key:同一邮箱从不同
	// 真实 IP 登录不会互相连坐。6 次不同 XFF 不应触发 429。
	us := &t09LoginStub{}
	h := &AuthHandler{userService: us}
	r := newT09LoginRouter(h, []string{"127.0.0.1/32"})
	const email = "t09-xff-trusted@example.com"

	for i := 1; i <= 6; i++ {
		w := t09Login(t, r, "127.0.0.1:4444", fmt.Sprintf("203.0.113.%d", 30+i), email)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d from distinct trusted client IP got 429; keys must be independent", i)
		}
	}
	if got := us.calls.Load(); got != 6 {
		t.Fatalf("auth reached %d times, want 6", got)
	}
}

func TestT09_SwaggerDeclares429AndAccessRevoked(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "swagger.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read swagger: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse swagger: %v", err)
	}

	for _, endpoint := range []string{"/auth/login", "/auth/register"} {
		responses := doc.Paths[endpoint]["post"].Responses
		if _, ok := responses["429"]; !ok {
			t.Fatalf("%s missing 429 response in swagger", endpoint)
		}
	}

	const capabilitiesSchema = "github_com_Tencent_WeKnora_internal_types.LoginCapabilities"
	props, ok := doc.Definitions[capabilitiesSchema].Properties["access_revoked"]
	if !ok || len(props) == 0 {
		t.Fatalf("%s missing access_revoked property", capabilitiesSchema)
	}
	if _, ok := doc.Definitions["internal_handler.CurrentUserCapabilities"].Properties["access_revoked"]; !ok {
		t.Fatal("CurrentUserCapabilities missing access_revoked property")
	}
}
