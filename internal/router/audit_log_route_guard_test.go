package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T27 TDD 红阶段：GET /tenants/:id/audit-log 收紧到「owner + 系统管理员」。
//
// 契约（T27 AC 1–4、AC-34、AC-38、C4）：
//   - 路由守卫由 g.Admin() 改为 g.OwnerOrSystemAdmin()；
//   - owner 放行；系统管理员不论工作区角色（viewer/contributor/无成员关系）放行；
//   - admin/contributor/viewer → 403；未认证 → 401；URL 空间与活动空间不一致 → 403；
//   - API Key：该路由现状未通过 apiKeyRoute 声明，网关默认拒绝，本票不得放宽。
//
// 测试走真实 RegisterTenantRoutes（组级 PathTenantMatch + 逐路由守卫），
// 审计 handler 装一个只实现 List 的桩服务：放行 → 200，守卫拒绝 → 403。

const auditGuardPath = "/api/v1/tenants/7/audit-log"

// auditGuardStubService 只实现 List，其余方法调用即 panic（不应被触达）。
type auditGuardStubService struct {
	interfaces.AuditLogService
}

func (auditGuardStubService) List(
	_ context.Context, _ uint64, _ *interfaces.AuditLogQuery,
) ([]*types.AuditLog, error) {
	return []*types.AuditLog{}, nil
}

type auditGuardSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
	tenantID    uint64
	apiKey      *types.TenantAPIKeyScope
}

func auditGuardEngine(t *testing.T, s auditGuardSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if s.tenantID == 0 {
		s.tenantID = memberGuardTenantID
	}
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, s.tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		if s.role != "" {
			ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		}
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		if s.apiKey != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *s.apiKey)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	// 与生产一致：API Key 网关挂在路由守卫之前，按声明表默认拒绝。
	engine.Use(g.ensureAPIKeyAuthorizer().Middleware())

	RegisterTenantRoutes(
		engine.Group("/api/v1"),
		&handler.TenantHandler{},
		nil,
		nil,
		handler.NewAuditLogHandler(auditGuardStubService{}),
		g,
	)
	return engine
}

func getAuditLog(t *testing.T, s auditGuardSession, path string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	auditGuardEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

// 绿保护网：owner 现状与目标都放行。
func TestAuditLogRouteOwnerAllowed(t *testing.T) {
	code := getAuditLog(t, auditGuardSession{role: types.TenantRoleOwner, userID: "u-owner"}, auditGuardPath)
	require.Equal(t, http.StatusOK, code, "workspace owner must read the tenant audit log")
}

// 红点 1：系统管理员不论工作区角色都应放行（现状 g.Admin() 不认超管 → 403）。
func TestAuditLogRouteSystemAdminAllowedRegardlessOfRole(t *testing.T) {
	cases := map[string]types.TenantRole{
		"viewer":        types.TenantRoleViewer,
		"contributor":   types.TenantRoleContributor,
		"no_membership": "",
	}
	for name, role := range cases {
		role := role
		t.Run("system_admin_"+name, func(t *testing.T) {
			code := getAuditLog(t, auditGuardSession{
				role: role, userID: "u-sysadmin", systemAdmin: true,
			}, auditGuardPath)
			require.Equal(t, http.StatusOK, code,
				"system admin (workspace role %q) must pass the OwnerOrSystemAdmin guard", role)
		})
	}
}

// 红点 2（admin）+ 绿保护网（contributor/viewer）：普通成员一律 403。
func TestAuditLogRouteRejectsNonOwnerMembers(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			code := getAuditLog(t, auditGuardSession{role: role, userID: "u-ordinary"}, auditGuardPath)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must not read the tenant audit log (T27: owner + system admin only)", role)
		})
	}
}

// 绿保护网：URL 空间与活动空间不一致 → 403（owner 与系统管理员都不能越空间）。
func TestAuditLogRouteRejectsPathTenantMismatch(t *testing.T) {
	for name, s := range map[string]auditGuardSession{
		"owner":        {role: types.TenantRoleOwner, userID: "u-owner", tenantID: 7},
		"system_admin": {role: types.TenantRoleViewer, userID: "u-sysadmin", systemAdmin: true, tenantID: 7},
	} {
		s := s
		t.Run(name, func(t *testing.T) {
			code := getAuditLog(t, s, "/api/v1/tenants/8/audit-log")
			require.Equal(t, http.StatusForbidden, code,
				"active workspace 7 must not read workspace 8's audit log by URL")
		})
	}
}

// 绿保护网：未认证 → 401。
func TestAuditLogRouteRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := &rbacGuards{cfg: cfg}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterTenantRoutes(engine.Group("/api/v1"), &handler.TenantHandler{}, nil, nil,
		handler.NewAuditLogHandler(auditGuardStubService{}), g)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, auditGuardPath, nil))
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated audit-log read must be rejected by backend auth")
}

// 绿保护网（API Key 现状）：路由未声明 API Key 策略，默认拒绝。
func TestAuditLogRouteStaysUndeclaredForAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	RegisterTenantRoutes(gin.New().Group("/api/v1"), &handler.TenantHandler{}, nil, nil,
		handler.NewAuditLogHandler(auditGuardStubService{}), g)

	_, declared := g.ensureAPIKeyAuthorizer().Lookup(http.MethodGet, "/api/v1/tenants/:id/audit-log")
	require.False(t, declared,
		"tenant audit-log must stay default-deny for API keys (not declared via apiKeyRoute)")
}

// 绿保护网（API Key 现状）：全权限空间 Key、带 system_audit_read 的平台 Key
// 实际请求都被网关 403，守卫改动不得让机器主体旁路。
func TestAuditLogRouteRejectsAPIKeyPrincipals(t *testing.T) {
	for name, scope := range map[string]types.TenantAPIKeyScope{
		"workspace_full_access": {KeyID: 1, FullAccess: true},
		"platform_system_audit_read": {
			KeyID:        2,
			ScopeType:    types.APIKeyScopePlatform,
			Capabilities: types.StringArray{string(types.APIKeyCapabilitySystemAuditRead)},
		},
	} {
		scope := scope
		t.Run(name, func(t *testing.T) {
			code := getAuditLog(t, auditGuardSession{
				role: types.TenantRoleOwner, userID: "", apiKey: &scope,
			}, auditGuardPath)
			require.Equal(t, http.StatusForbidden, code,
				"API key principals must stay denied on the tenant audit log")
		})
	}
}
