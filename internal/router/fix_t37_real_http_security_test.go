package router

// T37：安全链路真实 HTTP 证据（票面验收 1–5）。
//
// 与 T35 红阶段/复跑的关键差异：
//   - 不使用 t35ContextMiddleware 之类手注 gin context 的方式；
//   - 每个请求都带真实 Bearer JWT（Authorizaton）或 X-API-Key；
//   - 认证走生产同款 middleware.Auth（JWT 签名校验 → X-Tenant-ID →
//     角色解析 → 系统管理员标记），经真实 handler 与真实 service；
//   - 外部持久化（users / auth_tokens / tenant_members / tenants /
//     tenant_api_keys）用内存替身，但服务实现本身是生产代码。
//
// 覆盖票面验收：
//   1. 纯系统管理员（IsSystemAdmin、无成员关系）经 X-Tenant-ID 调用
//      platform ban/unban/purge、transfer-owner、owners/create、
//      user-management 不被 403；普通用户仍 403。
//   2. API Key 网关：manage_members scoped key 改角色成功；无 scope 403；
//      目标保护仍生效。
//   3. F3 目标保护经真实 TenantMemberHandler + 真实 tenantMemberService：
//      系统管理员 / 跨空间超管 / 别处 owner 目标 403。
//   4. 只用真实 HTTP 请求，不手注 gin context/角色。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// 真实 userService.ValidateToken 从 JWT_SECRET 读取签名密钥；必须在任何
// getJwtSecret 调用前固定下来，否则会生成随机密钥导致签名对不上。
func init() {
	_ = os.Setenv("JWT_SECRET", "t37-real-http-evidence-secret")
}

// ---------------------------------------------------------------------------
// 内存持久化替身（只 stub 外部依赖；service 本身是生产实现）
// ---------------------------------------------------------------------------

type t37UserRepo struct {
	interfaces.UserRepository
	users map[string]*types.User
}

func (r *t37UserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if u, ok := r.users[id]; ok {
		return u, nil
	}
	return nil, nil
}

func (r *t37UserRepo) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return nil, nil
}

type t37TokenRepo struct {
	interfaces.AuthTokenRepository
	tokens map[string]*types.AuthToken
}

func (r *t37TokenRepo) GetTokenByValue(_ context.Context, value string) (*types.AuthToken, error) {
	rec, ok := r.tokens[value]
	if !ok {
		return nil, fmt.Errorf("token not found")
	}
	return rec, nil
}

type t37MemberRepo struct {
	interfaces.TenantMemberRepository
	members map[string]*types.TenantMember
}

func t37MemberKey(userID string, tenantID uint64) string {
	return fmt.Sprintf("%s|%d", userID, tenantID)
}

func (r *t37MemberRepo) Get(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	m, ok := r.members[t37MemberKey(userID, tenantID)]
	if !ok {
		return nil, nil
	}
	cp := *m
	return &cp, nil
}

func (r *t37MemberRepo) ListByUser(_ context.Context, userID string) ([]*types.TenantMember, error) {
	out := make([]*types.TenantMember, 0)
	for _, m := range r.members {
		if m.UserID == userID && m.Status == types.TenantMemberStatusActive {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *t37MemberRepo) HasAnyMembers(_ context.Context, tenantID uint64) (bool, error) {
	for _, m := range r.members {
		if m.TenantID == tenantID && m.Status == types.TenantMemberStatusActive {
			return true, nil
		}
	}
	return false, nil
}

// UpdateRoleGuarded 让真实 service 走 guarded 分支（T32 CAS 语义）。
func (r *t37MemberRepo) UpdateRoleGuarded(
	_ context.Context, userID string, tenantID uint64,
	expectedRole, newRole types.TenantRole, allowOwnerDemotion bool,
) error {
	m, ok := r.members[t37MemberKey(userID, tenantID)]
	if !ok || m.Status != types.TenantMemberStatusActive {
		return fmt.Errorf("membership not found")
	}
	if m.Role != expectedRole {
		return fmt.Errorf("membership changed")
	}
	if m.Role == types.TenantRoleOwner && newRole != types.TenantRoleOwner && !allowOwnerDemotion {
		return fmt.Errorf("owner demotion requires transfer")
	}
	m.Role = newRole
	return nil
}

func (r *t37MemberRepo) UpdateRole(
	_ context.Context, userID string, tenantID uint64, newRole types.TenantRole,
) error {
	m, ok := r.members[t37MemberKey(userID, tenantID)]
	if !ok {
		return fmt.Errorf("membership not found")
	}
	m.Role = newRole
	return nil
}

type t37TenantSvc struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
}

func (s *t37TenantSvc) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if tenant, ok := s.tenants[id]; ok {
		return tenant, nil
	}
	return nil, fmt.Errorf("tenant %d not found", id)
}

type t37APIKeySvc struct {
	interfaces.TenantAPIKeyService
	keys map[string]*types.TenantAPIKey
}

func (s *t37APIKeySvc) AuthenticateAPIKey(_ context.Context, token string) (*types.TenantAPIKey, error) {
	if key, ok := s.keys[token]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("invalid api key")
}

// t37UserManagementSvc 是 UserManagementService 的替身，只用于证明
// 路由 + 认证 + 守卫链路放行到 handler；票面验收 1 不要求真实用户管理
// 服务（验收 3 的目标保护另行使用真实 tenantMemberService）。
type t37UserManagementSvc struct {
	interfaces.UserManagementService
	lastListTenantID uint64
}

func (s *t37UserManagementSvc) PlatformBanUser(
	_ context.Context, targetUserID, _ string,
) (*types.User, error) {
	return &types.User{ID: targetUserID, IsActive: false}, nil
}

func (s *t37UserManagementSvc) PlatformUnbanUser(
	_ context.Context, targetUserID, _ string,
) (*types.User, error) {
	return &types.User{ID: targetUserID, IsActive: true}, nil
}

func (s *t37UserManagementSvc) PlatformPurgeUser(
	_ context.Context, _ string, _, _ string,
) error {
	return nil
}

func (s *t37UserManagementSvc) TransferOwner(
	_ context.Context, _ uint64, _, _ string,
) error {
	return nil
}

func (s *t37UserManagementSvc) CreateCoOwnerAccount(
	_ context.Context, _ uint64, req *interfaces.CreateCoOwnerRequest,
) (*types.User, error) {
	return &types.User{ID: "u-new-owner", Username: req.Username, Email: req.Email, IsActive: true}, nil
}

func (s *t37UserManagementSvc) ListUsers(
	_ context.Context, tenantID uint64, _ types.TenantRole, _ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	s.lastListTenantID = tenantID
	return &interfaces.UserManagementListResult{
		Items: []interfaces.UserManagementListItem{},
		Total: 0,
		RoleCounts: map[types.TenantRole]int64{
			types.TenantRoleViewer:      0,
			types.TenantRoleContributor: 0,
			types.TenantRoleAdmin:       0,
			types.TenantRoleOwner:       0,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// 夹具：真实 service + 内存持久化
// ---------------------------------------------------------------------------

type t37Fixture struct {
	cfg        *config.Config
	userRepo   *t37UserRepo
	tokenRepo  *t37TokenRepo
	memberRepo *t37MemberRepo
	tenantSvc  *t37TenantSvc
	apiKeySvc  *t37APIKeySvc
	umSvc      *t37UserManagementSvc
	userSvc    interfaces.UserService
	memberSvc  interfaces.TenantMemberService
}

func newT37Fixture(t *testing.T) *t37Fixture {
	t.Helper()
	rbacOn := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}

	userRepo := &t37UserRepo{users: map[string]*types.User{
		"u-sys":            {ID: "u-sys", Username: "sys", Email: "sys@example.com", TenantID: 1, IsActive: true, IsSystemAdmin: true},
		"u-viewer":         {ID: "u-viewer", Username: "viewer", Email: "viewer@example.com", TenantID: 7, IsActive: true},
		"u-owner":          {ID: "u-owner", Username: "owner", Email: "owner@example.com", TenantID: 7, IsActive: true},
		"u-target":         {ID: "u-target", Username: "target", Email: "target@example.com", TenantID: 7, IsActive: true},
		"u-target-admin":   {ID: "u-target-admin", Username: "target-admin", Email: "target-admin@example.com", TenantID: 7, IsActive: true, IsSystemAdmin: true},
		"u-target-super":   {ID: "u-target-super", Username: "target-super", Email: "target-super@example.com", TenantID: 7, IsActive: true, CanAccessAllTenants: true},
		"u-target-elseown": {ID: "u-target-elseown", Username: "target-elseown", Email: "target-elseown@example.com", TenantID: 7, IsActive: true},
	}}
	tokenRepo := &t37TokenRepo{tokens: map[string]*types.AuthToken{}}
	memberRepo := &t37MemberRepo{members: map[string]*types.TenantMember{}}
	seed := func(userID string, tenantID uint64, role types.TenantRole) {
		memberRepo.members[t37MemberKey(userID, tenantID)] = &types.TenantMember{
			UserID: userID, TenantID: tenantID, Role: role, Status: types.TenantMemberStatusActive,
		}
	}
	seed("u-viewer", 7, types.TenantRoleViewer)
	seed("u-owner", 7, types.TenantRoleOwner)
	seed("u-target", 7, types.TenantRoleViewer)
	seed("u-target-admin", 7, types.TenantRoleViewer)
	seed("u-target-super", 7, types.TenantRoleViewer)
	seed("u-target-elseown", 7, types.TenantRoleViewer)
	seed("u-target-elseown", 9, types.TenantRoleOwner)

	tenantSvc := &t37TenantSvc{tenants: map[uint64]*types.Tenant{
		1: {ID: 1}, 7: {ID: 7}, 9: {ID: 9},
	}}
	tenantID7 := uint64(7)
	apiKeySvc := &t37APIKeySvc{keys: map[string]*types.TenantAPIKey{
		"sk-manage-members": {
			ID: 1, TenantID: &tenantID7, ScopeType: types.APIKeyScopeTenant,
			Name: "t37-manage-members", FullAccess: false,
			Capabilities: types.StringArray{string(types.APIKeyCapabilityManageMembers)},
		},
		"sk-no-scope": {
			ID: 2, TenantID: &tenantID7, ScopeType: types.APIKeyScopeTenant,
			Name: "t37-no-scope", FullAccess: false,
			Capabilities: types.StringArray{},
		},
	}}

	// 真实生产 service；持久化用上面的内存替身。
	memberSvc := appservice.NewTenantMemberService(memberRepo, nil, userRepo, nil)
	userSvc := appservice.NewUserService(cfg, userRepo, tokenRepo, tenantSvc, memberSvc, nil)

	return &t37Fixture{
		cfg: cfg, userRepo: userRepo, tokenRepo: tokenRepo, memberRepo: memberRepo,
		tenantSvc: tenantSvc, apiKeySvc: apiKeySvc, umSvc: &t37UserManagementSvc{},
		userSvc: userSvc, memberSvc: memberSvc,
	}
}

// issue 生成真实 HS256 access token，并写入 tokenRepo（ValidateToken 会查
// 撤销表；这是生产链路的真实步骤）。
func (f *t37Fixture) issue(t *testing.T, userID string, tenantID uint64) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id":   userID,
		"tenant_id": tenantID,
		"type":      "access",
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(os.Getenv("JWT_SECRET")))
	require.NoError(t, err)
	f.tokenRepo.tokens[signed] = &types.AuthToken{
		ID: "tok-" + userID, UserID: userID, Token: signed,
		TokenType: "access_token", ExpiresAt: time.Now().Add(time.Hour),
	}
	return signed
}

// engine 按生产顺序装配：ErrorHandler → 全局 middleware.Auth → /api/v1
// 组上的 API Key 网关 → 真实路由注册函数。
func (f *t37Fixture) engine(t *testing.T, withMemberHandler bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(f.tenantSvc, f.userSvc, f.memberSvc, f.apiKeySvc, f.cfg))

	v1 := engine.Group("/api/v1")
	g := &rbacGuards{cfg: f.cfg}
	v1.Use(g.ensureAPIKeyAuthorizer().Middleware())

	tenantHandler := handler.NewTenantHandler(
		f.tenantSvc, f.apiKeySvc, f.userSvc, f.memberSvc, nil, f.cfg, nil, nil)
	var memberHandler *handler.TenantMemberHandler
	if withMemberHandler {
		memberHandler = handler.NewTenantMemberHandler(f.memberSvc, f.userSvc)
	}
	RegisterTenantRoutes(v1, tenantHandler, memberHandler, nil, nil, g)

	umHandler := handler.NewUserManagementHandler(f.umSvc, f.memberSvc)
	RegisterUserManagementRoutes(v1, umHandler, g)
	RegisterPlatformUserManagementRoutes(v1, umHandler, g)
	return engine
}

type t37Request struct {
	method  string
	path    string
	headers map[string]string
	body    string
}

func t37Serve(t *testing.T, engine *gin.Engine, req t37Request) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if req.body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(req.body)
	}
	httpReq := httptest.NewRequest(req.method, req.path, reader)
	if req.body != "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for key, value := range req.headers {
		httpReq.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httpReq)
	return w
}

func t37Bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func t37BearerTenant(token string, tenantID string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "X-Tenant-ID": tenantID}
}

// ---------------------------------------------------------------------------
// 验收 1：纯系统管理员经 X-Tenant-ID 不被 403；普通用户仍 403
// ---------------------------------------------------------------------------

func t37ManagementRequests() []t37Request {
	return []t37Request{
		{method: http.MethodPost, path: "/api/v1/system/admin/users/u-target/ban", body: `{}`},
		{method: http.MethodPost, path: "/api/v1/system/admin/users/u-target/unban", body: `{}`},
		{method: http.MethodPost, path: "/api/v1/system/admin/users/u-target/purge", body: `{"confirm_email":"target@example.com"}`},
		{method: http.MethodPost, path: "/api/v1/tenants/7/transfer-owner", body: `{"to_user_id":"u-target"}`},
		{method: http.MethodPost, path: "/api/v1/tenants/7/owners/create", body: `{"username":"newowner","email":"newowner@example.com","password":"Secret123!"}`},
		{method: http.MethodGet, path: "/api/v1/tenants/7/user-management"},
	}
}

func TestT37SystemAdminWithoutMembershipReachesEveryManagementRoute(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, false)
	token := f.issue(t, "u-sys", 1) // home tenant=1，请求空间经 X-Tenant-ID=7

	wantCodes := []int{
		http.StatusOK,      // platform ban
		http.StatusOK,      // platform unban
		http.StatusOK,      // platform purge
		http.StatusOK,      // transfer-owner
		http.StatusCreated, // owners/create
		http.StatusOK,      // user-management
	}
	requests := t37ManagementRequests()
	require.Len(t, requests, len(wantCodes))
	for i, req := range requests {
		req.headers = t37BearerTenant(token, "7")
		w := t37Serve(t, engine, req)
		require.Equal(t, wantCodes[i], w.Code,
			"route %s %s must not be 403 for a pure system admin", req.method, req.path)
	}
}

func TestT37OrdinaryUserStillForbiddenOnEveryManagementRoute(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, false)
	token := f.issue(t, "u-viewer", 7) // 本空间 viewer，非系统管理员

	for _, req := range t37ManagementRequests() {
		req.headers = t37BearerTenant(token, "7")
		w := t37Serve(t, engine, req)
		require.Equal(t, http.StatusForbidden, w.Code,
			"ordinary user must stay 403 on %s %s", req.method, req.path)
	}
}

// ---------------------------------------------------------------------------
// 验收 2：API Key 网关真实链路
// ---------------------------------------------------------------------------

func t37UpdateRoleRequest(apiKey string) t37Request {
	return t37Request{
		method:  http.MethodPut,
		path:    "/api/v1/tenants/7/members/u-target",
		headers: map[string]string{"X-API-Key": apiKey},
		body:    `{"role":"admin"}`,
	}
}

func TestT37APIKeyManageMembersScopeUpdatesRole(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)

	w := t37Serve(t, engine, t37UpdateRoleRequest("sk-manage-members"))
	require.Equal(t, http.StatusOK, w.Code,
		"scoped key with manage_members must be allowed through the real API-key gate")

	member, err := f.memberRepo.Get(context.Background(), "u-target", 7)
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleAdmin, member.Role,
		"the real tenantMemberService must persist the role change")
}

func TestT37APIKeyWithoutManageMembersScopeIsDenied(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)

	w := t37Serve(t, engine, t37UpdateRoleRequest("sk-no-scope"))
	require.Equal(t, http.StatusForbidden, w.Code,
		"a scoped key without manage_members must be denied by the API-key gate")

	member, err := f.memberRepo.Get(context.Background(), "u-target", 7)
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleViewer, member.Role,
		"denied API-key request must not change the target role")
}

func TestT37APIKeyTargetProtectionStillEnforced(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)

	req := t37UpdateRoleRequest("sk-manage-members")
	req.path = "/api/v1/tenants/7/members/u-target-admin"
	w := t37Serve(t, engine, req)
	require.Equal(t, http.StatusForbidden, w.Code,
		"target protection (privileged account) must still be enforced for API keys")

	member, err := f.memberRepo.Get(context.Background(), "u-target-admin", 7)
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleViewer, member.Role,
		"protected target must not be modified")
}

// ---------------------------------------------------------------------------
// 验收 3：F3 目标保护经真实 handler + 真实 tenantMemberService
// ---------------------------------------------------------------------------

func TestT37TargetProtectionThroughRealHandlerAndService(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)
	token := f.issue(t, "u-owner", 7) // 非系统管理员的本空间 owner

	protected := map[string]string{
		"system administrator":       "u-target-admin",
		"cross-workspace superuser":  "u-target-super",
		"owner of another workspace": "u-target-elseown",
	}
	for name, targetUserID := range protected {
		req := t37Request{
			method:  http.MethodPut,
			path:    "/api/v1/tenants/7/members/" + targetUserID,
			headers: t37Bearer(token),
			body:    `{"role":"admin"}`,
		}
		w := t37Serve(t, engine, req)
		require.Equal(t, http.StatusForbidden, w.Code,
			"target protection must reject %s through the real handler+service", name)
	}
}

func TestT37RoleUpdateControlSucceedsThroughRealHandlerAndService(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)
	token := f.issue(t, "u-owner", 7)

	req := t37Request{
		method:  http.MethodPut,
		path:    "/api/v1/tenants/7/members/u-target",
		headers: t37Bearer(token),
		body:    `{"role":"admin"}`,
	}
	w := t37Serve(t, engine, req)
	require.Equal(t, http.StatusOK, w.Code,
		"an ordinary target must still be updatable, proving the protection is not blanket-deny")

	member, err := f.memberRepo.Get(context.Background(), "u-target", 7)
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleAdmin, member.Role)
}

// ---------------------------------------------------------------------------
// 验收 4：真实 HTTP / 真实认证链（无认证 401、伪造签名 401）
// ---------------------------------------------------------------------------

func TestT37RealAuthChainRejectsMissingAndForgedCredentials(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, true)

	noAuth := t37Serve(t, engine, t37Request{
		method: http.MethodPut,
		path:   "/api/v1/tenants/7/members/u-target",
		body:   `{"role":"admin"}`,
	})
	require.Equal(t, http.StatusUnauthorized, noAuth.Code)

	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": "u-owner", "tenant_id": 7, "type": "access",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("wrong-secret"))
	require.NoError(t, err)

	forgedResp := t37Serve(t, engine, t37Request{
		method:  http.MethodPut,
		path:    "/api/v1/tenants/7/members/u-target",
		headers: t37Bearer(forged),
		body:    `{"role":"admin"}`,
	})
	require.Equal(t, http.StatusUnauthorized, forgedResp.Code,
		"forged JWT signature must be rejected by the real userService.ValidateToken")
}

// 防止误删：确认响应体是 JSON（ErrorHandler 生效，说明走的是真实 HTTP
// 栈而不是直接调用 handler）。
func TestT37ResponsesComeFromRealHTTPStack(t *testing.T) {
	f := newT37Fixture(t)
	engine := f.engine(t, false)
	token := f.issue(t, "u-viewer", 7)

	w := t37Serve(t, engine, t37Request{
		method:  http.MethodPost,
		path:    "/api/v1/system/admin/users/u-target/ban",
		headers: t37BearerTenant(token, "7"),
		body:    `{}`,
	})
	require.Equal(t, http.StatusForbidden, w.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Contains(t, payload, "error",
		"guard rejection must come from the real HTTP middleware stack")
}
