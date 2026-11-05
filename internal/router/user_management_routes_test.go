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

// T02 路由注册 + 守卫红阶段用例（AC-38、C4、T02-3、T02-5）。
//
// 假定契约（02-plan §3.1）：
//   - internal/router/routes_auth_tenant.go 提供
//     RegisterUserManagementRoutes(r, userManagementHandler, g)；
//   - POST /api/v1/tenants/:id/members/:user_id/ban|unban；
//   - /tenants/:id 组级 PathTenantMatch，逐路由 g.OwnerOrSystemAdmin()
//     （RequireRoleOrSystemAdmin(Owner)：owner 或系统管理员，普通 g.Owner() 不认超管）。
//
// 守卫放行 → 请求到达 handler，故意发非法 JSON，让 ShouldBindJSON 失败、
// ErrorHandler 渲染 400（不触发 nil service）。守卫拒绝 → 403；未认证 → 401。

const umRouterTenantID uint64 = 7

type umRouteSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
	tenantID    uint64
}

func newUMRouteEngine(t *testing.T, s umRouteSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, s.tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)
	return engine
}

func postUMRouteWithSession(t *testing.T, s umRouteSession, path string) int {
	t.Helper()
	if s.tenantID == 0 {
		s.tenantID = umRouterTenantID
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	newUMRouteEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

func TestUserManagementRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}
	engine := gin.New()
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered["POST /api/v1/tenants/:id/members/:user_id/ban"],
		"POST /tenants/:id/members/:user_id/ban must be registered")
	require.True(t, registered["POST /api/v1/tenants/:id/members/:user_id/unban"],
		"POST /tenants/:id/members/:user_id/unban must be registered")
}

func TestUserManagementRoutesOwnerPassesGuard(t *testing.T) {
	for _, action := range []string{"ban", "unban"} {
		action := action
		t.Run(action, func(t *testing.T) {
			path := "/api/v1/tenants/7/members/u-target/" + action
			code := postUMRouteWithSession(t, umRouteSession{
				role: types.TenantRoleOwner, userID: "u-owner",
			}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace owner must pass the OwnerOrSystemAdmin guard and reach the handler")
		})
	}
}

func TestUserManagementRoutesSystemAdminPassesGuardRegardlessOfRole(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		for _, action := range []string{"ban", "unban"} {
			role, action := role, action
			t.Run(string(role)+"_"+action, func(t *testing.T) {
				path := "/api/v1/tenants/7/members/u-target/" + action
				code := postUMRouteWithSession(t, umRouteSession{
					role: role, userID: "u-sysadmin", systemAdmin: true,
				}, path)
				require.Equal(t, http.StatusBadRequest, code,
					"system admin with workspace role %s must pass (普通 g.Owner() 会误拒)", role)
			})
		}
	}
}

func TestUserManagementRoutesNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		for _, action := range []string{"ban", "unban"} {
			role, action := role, action
			t.Run(string(role)+"_"+action, func(t *testing.T) {
				path := "/api/v1/tenants/7/members/u-target/" + action
				code := postUMRouteWithSession(t, umRouteSession{
					role: role, userID: "u-ordinary",
				}, path)
				require.Equal(t, http.StatusForbidden, code,
					"ordinary %s must be rejected by the OwnerOrSystemAdmin guard", role)
			})
		}
	}
}

func TestUserManagementRoutesRejectPathTenantMismatch(t *testing.T) {
	path := "/api/v1/tenants/8/members/u-target/ban"
	code := postUMRouteWithSession(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner", tenantID: 7,
	}, path)
	require.Equal(t, http.StatusForbidden, code,
		"an owner of workspace 7 must not drive a ban against workspace 8 by URL")
}

func TestUserManagementRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := &rbacGuards{cfg: cfg}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/7/members/u-target/ban",
		strings.NewReader(`{"reason":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated ban must be rejected by backend auth, not only hidden in the UI")
}
