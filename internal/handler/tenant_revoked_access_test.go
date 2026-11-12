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

// tenant_revoked_access_test.go 覆盖「被移除空间」的两条后端防线:
//  1. POST /tenants 对被移除用户返回 403(详情 access_revoked),平台级管理员不受限;
//  2. /auth/login 与 /auth/me 的 capabilities.access_revoked / can_create_tenant 取值。
//
// T11 / AC-44 产品决策变更:普通用户(owner/admin/contributor/viewer)不再能自助
// 建空间,即使 tenant.self_service_creation_enabled 开启也一律 403;can_create_tenant
// 只对平台级管理员(IsSystemAdmin 或 CanAccessAllTenants)为 true。本文件里普通用户
// 的建空间用例因此断言 403,但「撤销判定/恢复」的原安全意图保留。

type revokedMemberService struct {
	interfaces.TenantMemberService
	revoked      bool
	revokedErr   error
	revokedCalls int
	memberships  []*types.TenantMember
}

func (s *revokedMemberService) HasRevokedAccess(context.Context, string) (bool, error) {
	s.revokedCalls++
	return s.revoked, s.revokedErr
}

func (s *revokedMemberService) EnsureOwner(
	_ context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	return &types.TenantMember{UserID: userID, TenantID: tenantID, Role: types.TenantRoleOwner}, nil
}

func (s *revokedMemberService) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	return s.memberships, nil
}

func (s *revokedMemberService) RemoveMember(context.Context, string, uint64) error {
	return nil
}

type revokedTenantUserService struct {
	interfaces.UserService
	user        *types.User
	updateCalls int
}

func (s *revokedTenantUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return s.user, nil
}

func (s *revokedTenantUserService) UpdateUser(_ context.Context, user *types.User) error {
	s.updateCalls++
	s.user = user
	return nil
}

func newCreateTenantTestRouter(h *TenantHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(tenantPolicyErrorCapture())
	r.POST("/tenants", h.CreateTenant)
	return r
}

func doCreateTenantRequest(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestCreateTenant_RejectsRevokedMember(t *testing.T) {
	tenants := &tenantPolicyTenantService{}
	members := &revokedMemberService{revoked: true}
	h := &TenantHandler{
		service: tenants,
		userService: &revokedTenantUserService{user: &types.User{
			ID:       "removed-user",
			TenantID: 1,
		}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := doCreateTenantRequest(t, newCreateTenantTestRouter(h), `{"name":"should-not-exist"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":1002`) {
		t.Fatalf("response missing forbidden code: %s", body)
	}
	if !strings.Contains(body, "access_revoked") {
		t.Fatalf("response missing stable details access_revoked: %s", body)
	}
}

// TestCreateTenant_AllowsUserWithActiveWorkspace 原意:活跃用户(有活跃成员关系)
// 不属于「被移除」,旧语义下可再建空间。T11 / AC-44 后普通用户不再能自助建空间,
// 但「撤销判定不误伤活跃用户」的安全意图仍需保留:必须走到平台门禁、而非被
// access_revoked 拦截。
func TestCreateTenant_AllowsUserWithActiveWorkspace(t *testing.T) {
	tenants := &tenantPolicyTenantService{}
	members := &revokedMemberService{
		revoked:     false,
		memberships: []*types.TenantMember{{TenantID: 1, Role: types.TenantRoleOwner}},
	}
	h := &TenantHandler{
		service: tenants,
		userService: &revokedTenantUserService{user: &types.User{
			ID:       "active-user",
			TenantID: 1,
		}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := doCreateTenantRequest(t, newCreateTenantTestRouter(h), `{"name":"second-workspace"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	if members.revokedCalls != 1 {
		t.Fatalf("HasRevokedAccess called %d times, want 1 (revocation check must still run)", members.revokedCalls)
	}
	body := w.Body.String()
	if strings.Contains(body, "access_revoked") {
		t.Fatalf("active user must not be denied as revoked: %s", body)
	}
	if !strings.Contains(body, `"details":"tenant.create_requires_platform_admin"`) {
		t.Fatalf("want platform-admin gate detail, body=%s", body)
	}
}

// TestCreateTenant_AllowsBrandNewUser 原意:从没加入过任何空间的新用户不算被移除。
// T11 / AC-44 后普通新用户同样不能自助建空间;这里改为断言被平台门禁拦下、
// 且不会创建租户或把首个空间写成 home。
func TestCreateTenant_AllowsBrandNewUser(t *testing.T) {
	// 从没加入过任何空间的新用户:活跃=0 但没有软删行,不算被移除。
	tenants := &tenantPolicyTenantService{}
	users := &revokedTenantUserService{user: &types.User{ID: "brand-new-user"}}
	h := &TenantHandler{
		service:          tenants,
		userService:      users,
		memberService:    &revokedMemberService{revoked: false},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := doCreateTenantRequest(t, newCreateTenantTestRouter(h), `{"name":"first-workspace"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant called %d times, want 0", tenants.createCalls)
	}
	if users.updateCalls != 0 {
		t.Fatalf("UpdateUser called %d times, want 0 (no workspace should be created)", users.updateCalls)
	}
	body := w.Body.String()
	if strings.Contains(body, "access_revoked") {
		t.Fatalf("brand-new user must not be denied as revoked: %s", body)
	}
	if !strings.Contains(body, `"details":"tenant.create_requires_platform_admin"`) {
		t.Fatalf("want platform-admin gate detail, body=%s", body)
	}
}

func TestCreateTenant_SystemAdminNotBlocked(t *testing.T) {
	tenants := &tenantPolicyTenantService{}
	members := &revokedMemberService{revoked: true}
	h := &TenantHandler{
		service: tenants,
		userService: &revokedTenantUserService{user: &types.User{
			ID:            "sysadmin",
			TenantID:      1,
			IsSystemAdmin: true,
		}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := doCreateTenantRequest(t, newCreateTenantTestRouter(h), `{"name":"admin-workspace"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if members.revokedCalls != 0 {
		t.Fatalf("HasRevokedAccess must not run for system admins, called %d times", members.revokedCalls)
	}
}

func TestCreateTenant_CrossTenantSuperuserNotBlocked(t *testing.T) {
	tenants := &tenantPolicyTenantService{}
	members := &revokedMemberService{revoked: true}
	h := &TenantHandler{
		service: tenants,
		userService: &revokedTenantUserService{user: &types.User{
			ID:                  "superuser",
			TenantID:            1,
			CanAccessAllTenants: true,
		}},
		memberService:    members,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}

	w := doCreateTenantRequest(t, newCreateTenantTestRouter(h), `{"name":"catalog-workspace"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if members.revokedCalls != 0 {
		t.Fatalf("HasRevokedAccess must not run for catalog managers, called %d times", members.revokedCalls)
	}
}

type revokedAuthUserService struct {
	interfaces.UserService
	user  *types.User
	login *types.LoginResponse
}

func (s *revokedAuthUserService) GetCurrentUser(context.Context) (*types.User, error) {
	return s.user, nil
}

func (s *revokedAuthUserService) BuildLoginMemberships(context.Context, *types.User, *types.Tenant) []types.Membership {
	return []types.Membership{}
}

func (s *revokedAuthUserService) Login(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
	return s.login, nil
}

func newLoginCapabilityTestRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.POST("/auth/login", h.Login)
	return r
}

func doCapabilityLogin(t *testing.T, r *gin.Engine, email string) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(map[string]string{"email": email, "password": "wrongpassword"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.80:1234"
	r.ServeHTTP(w, req)
	return w
}

func TestLogin_ProjectsRevokedCapability(t *testing.T) {
	user := &types.User{ID: "removed-user", Email: "t07-revoked@example.com", TenantID: 1}
	h := &AuthHandler{
		userService: &revokedAuthUserService{
			user:  user,
			login: &types.LoginResponse{Success: true, User: user},
		},
		memberService:    &revokedMemberService{revoked: true},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: nil,
	}

	w := doCapabilityLogin(t, newLoginCapabilityTestRouter(h), "t07-revoked@example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"access_revoked":true`) {
		t.Fatalf("login response missing access_revoked=true: %s", body)
	}
	if !strings.Contains(body, `"can_create_tenant":false`) {
		t.Fatalf("revoked user must not advertise can_create_tenant: %s", body)
	}
}

func TestLogin_ProjectsNormalCapability(t *testing.T) {
	user := &types.User{ID: "active-user", Email: "t07-active@example.com", TenantID: 1}
	h := &AuthHandler{
		userService: &revokedAuthUserService{
			user:  user,
			login: &types.LoginResponse{Success: true, User: user},
		},
		memberService:    &revokedMemberService{revoked: false},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: nil,
	}

	w := doCapabilityLogin(t, newLoginCapabilityTestRouter(h), "t07-active@example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"access_revoked":false`) {
		t.Fatalf("login response missing access_revoked=false: %s", body)
	}
	// T11 / AC-44:普通活跃用户不再获得建空间能力。
	if !strings.Contains(body, `"can_create_tenant":false`) {
		t.Fatalf("regular active user must not advertise can_create_tenant: %s", body)
	}
}

func newAuthMeCapabilityTestRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/auth/me", h.GetCurrentUser)
	return r
}

func TestAuthMe_ProjectsRevokedCapability(t *testing.T) {
	h := &AuthHandler{
		userService: &revokedAuthUserService{user: &types.User{
			ID:       "removed-user",
			Username: "removed",
			Email:    "t07-me-revoked@example.com",
		}},
		memberService:    &revokedMemberService{revoked: true},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: nil,
	}

	w := httptest.NewRecorder()
	newAuthMeCapabilityTestRouter(h).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"access_revoked":true`) {
		t.Fatalf("/auth/me missing access_revoked=true: %s", body)
	}
	if !strings.Contains(body, `"can_create_tenant":false`) {
		t.Fatalf("/auth/me must fold revoked into can_create_tenant: %s", body)
	}
}

func TestAuthMe_ProjectsNormalCapability(t *testing.T) {
	h := &AuthHandler{
		userService: &revokedAuthUserService{user: &types.User{
			ID:       "active-user",
			Username: "active",
			Email:    "t07-me-active@example.com",
		}},
		memberService:    &revokedMemberService{revoked: false},
		configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: nil,
	}

	w := httptest.NewRecorder()
	newAuthMeCapabilityTestRouter(h).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"access_revoked":false`) {
		t.Fatalf("/auth/me missing access_revoked=false: %s", body)
	}
	// T11 / AC-44:普通活跃用户不再获得建空间能力。
	if !strings.Contains(body, `"can_create_tenant":false`) {
		t.Fatalf("normal user must not advertise can_create_tenant: %s", body)
	}
}
