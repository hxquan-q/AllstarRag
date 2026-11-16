package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T35 TDD 红阶段：平台级封禁 / 解封 / 注销路由契约。
//
// 假定契约（详见 05-tests §54）：
//   - internal/router/routes_auth_tenant.go 新增
//     RegisterPlatformUserManagementRoutes(r, userManagementHandler, g)；
//   - 注册 POST /system/admin/users/:user_id/ban|unban|purge；
//   - 组级 g.SystemAdmin()；不挂 PathTenantMatch（平台接口不依赖当前空间）；
//   - 非系统管理员 403、未认证 401；
//   - 不通过 apiKeyRoute 声明 → API Key 默认拒绝（workspace 全权限 key 与
//     平台系统 key 都不得旁路）。

const t35PlatformPathPrefix = "/api/v1/system/admin/users/u-target/"

var t35PlatformActions = []string{"ban", "unban", "purge"}

type t35RouteSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
	tenantID    uint64
	apiKey      *types.TenantAPIKeyScope
}

func t35RBACGuards() *rbacGuards {
	rbacOn := true
	return &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}
}

func t35ContextMiddleware(s t35RouteSession) gin.HandlerFunc {
	return func(c *gin.Context) {
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
	}
}

func t35PlatformEngine(t *testing.T, s t35RouteSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if s.tenantID == 0 {
		s.tenantID = 7
	}
	g := t35RBACGuards()
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(t35ContextMiddleware(s))
	// 与生产一致：API Key 网关挂在路由守卫之前，按声明表默认拒绝。
	engine.Use(g.ensureAPIKeyAuthorizer().Middleware())
	RegisterPlatformUserManagementRoutes(
		engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)
	return engine
}

func t35PostPlatform(t *testing.T, s t35RouteSession, path, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	t35PlatformEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

func TestT35PlatformRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := t35RBACGuards()
	engine := gin.New()
	RegisterPlatformUserManagementRoutes(
		engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, action := range t35PlatformActions {
		want := "POST /api/v1/system/admin/users/:user_id/" + action
		require.True(t, registered[want], want+" must be registered")
	}
}

// 系统管理员在任意工作区角色（甚至无成员关系）下都必须通过守卫，抵达
// handler；非法 JSON 让 handler 返回 400，证明不是被守卫拦下。
func TestT35PlatformRoutesSystemAdminPassesGuard(t *testing.T) {
	for _, role := range []types.TenantRole{"", types.TenantRoleViewer, types.TenantRoleContributor} {
		for _, action := range t35PlatformActions {
			role, action := role, action
			t.Run(string(role)+"_"+action, func(t *testing.T) {
				code := t35PostPlatform(t, t35RouteSession{
					role: role, userID: "u-sysadmin", systemAdmin: true,
				}, t35PlatformPathPrefix+action, "not-json")
				require.Equal(t, http.StatusBadRequest, code,
					"system admin must reach the handler regardless of workspace role")
			})
		}
	}
}

// 非系统管理员即使是本空间 owner，也必须 403。
func TestT35PlatformRoutesNonSystemAdminRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleOwner,
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		for _, action := range t35PlatformActions {
			role, action := role, action
			t.Run(string(role)+"_"+action, func(t *testing.T) {
				code := t35PostPlatform(t, t35RouteSession{
					role: role, userID: "u-ordinary",
				}, t35PlatformPathPrefix+action, `{"confirm_email":"target@example.com"}`)
				require.Equal(t, http.StatusForbidden, code,
					"non-system-admin %s must be rejected on the platform route", role)
			})
		}
	}
}

func TestT35PlatformRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := t35RBACGuards()

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterPlatformUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	for _, action := range t35PlatformActions {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			t35PlatformPathPrefix+action, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"unauthenticated platform %s must be rejected by backend auth", action)
	}
}

// 新平台路由未声明 API Key 策略：授权表查不到，网关默认拒绝。
func TestT35PlatformRoutesStayUndeclaredForAPIKeys(t *testing.T) {
	g := t35RBACGuards()
	RegisterPlatformUserManagementRoutes(
		gin.New().Group("/api/v1"), &handler.UserManagementHandler{}, g)

	for _, action := range t35PlatformActions {
		_, declared := g.ensureAPIKeyAuthorizer().Lookup(
			http.MethodPost, "/api/v1/system/admin/users/:user_id/"+action)
		require.False(t, declared,
			"platform %s must stay default-deny for API keys", action)
	}
}

// 即使请求上下文已经带系统管理员标志，API Key 主体也不能旁路默认拒绝。
func TestT35PlatformRoutesRejectAPIKeyPrincipals(t *testing.T) {
	for name, scope := range map[string]types.TenantAPIKeyScope{
		"workspace_full_access": {
			KeyID:      1,
			FullAccess: true,
		},
		"platform_settings_manage": {
			KeyID:        2,
			ScopeType:    types.APIKeyScopePlatform,
			Capabilities: types.StringArray{string(types.APIKeyCapabilitySystemSettingsManage)},
		},
	} {
		scope := scope
		t.Run(name, func(t *testing.T) {
			for _, action := range t35PlatformActions {
				code := t35PostPlatform(t, t35RouteSession{
					userID: "api-key-principal", systemAdmin: true, apiKey: &scope,
				}, t35PlatformPathPrefix+action, `{"confirm_email":"target@example.com"}`)
				require.Equal(t, http.StatusForbidden, code,
					"API key principals must stay denied on platform %s", action)
			}
		})
	}
}
