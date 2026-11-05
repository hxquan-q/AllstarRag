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

// T04 路由注册 + 守卫红阶段用例（T04-1/2/3/4、AC-38、C4）。
//
// 假定契约（02-plan §3.1/§3.2）：
//   - RegisterUserManagementRoutes 增加
//     POST /tenants/:id/transfer-owner 与 POST /tenants/:id/owners/create，
//     均挂 g.OwnerOrSystemAdmin()；
//   - 新 RegisterOwnerManagementRoutes(r, ownerManagementHandler, g) 注册
//     POST /system/admin/users/:user_id/owner-space 与
//     POST /system/admin/owners/invite，沿用 /system/admin 组的 SystemAdmin()。
//
// 守卫放行 → 请求到达 handler，故意发非法 JSON → 400；守卫拒绝 → 403；
// 未认证 → 401。

const omRouterTenantID uint64 = 7

type omRouteSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
	tenantID    uint64
}

func omRBACGuards() *rbacGuards {
	rbacOn := true
	return &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}
}

func omContextMiddleware(s omRouteSession) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, s.tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func newOMTenantRoutesEngine(t *testing.T, s omRouteSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if s.tenantID == 0 {
		s.tenantID = omRouterTenantID
	}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(omContextMiddleware(s))
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, omRBACGuards())
	return engine
}

func newOMSystemAdminRoutesEngine(t *testing.T, s omRouteSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(omContextMiddleware(s))
	RegisterOwnerManagementRoutes(engine.Group("/api/v1"), &handler.OwnerManagementHandler{}, omRBACGuards())
	return engine
}

func postOMTenantRoute(t *testing.T, s omRouteSession, path string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	newOMTenantRoutesEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

func postOMSystemAdminRoute(t *testing.T, s omRouteSession, path string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	newOMSystemAdminRoutesEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

func TestOwnerManagementRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := omRBACGuards()

	tenantEngine := gin.New()
	RegisterUserManagementRoutes(tenantEngine.Group("/api/v1"), &handler.UserManagementHandler{}, g)
	systemEngine := gin.New()
	RegisterOwnerManagementRoutes(systemEngine.Group("/api/v1"), &handler.OwnerManagementHandler{}, g)

	registered := map[string]bool{}
	for _, route := range tenantEngine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range systemEngine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	require.True(t, registered["POST /api/v1/tenants/:id/transfer-owner"],
		"POST /tenants/:id/transfer-owner must be registered")
	require.True(t, registered["POST /api/v1/tenants/:id/owners/create"],
		"POST /tenants/:id/owners/create must be registered")
	require.True(t, registered["POST /api/v1/system/admin/users/:user_id/owner-space"],
		"POST /system/admin/users/:user_id/owner-space must be registered")
	require.True(t, registered["POST /api/v1/system/admin/owners/invite"],
		"POST /system/admin/owners/invite must be registered")
}

func TestOwnerTenantRoutesOwnerPassesGuard(t *testing.T) {
	for _, path := range []string{
		"/api/v1/tenants/7/transfer-owner",
		"/api/v1/tenants/7/owners/create",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			code := postOMTenantRoute(t, omRouteSession{
				role: types.TenantRoleOwner, userID: "u-owner",
			}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace owner must pass the OwnerOrSystemAdmin guard and reach the handler")
		})
	}
}

func TestOwnerTenantRoutesSystemAdminPassesGuardRegardlessOfRole(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		for _, path := range []string{
			"/api/v1/tenants/7/transfer-owner",
			"/api/v1/tenants/7/owners/create",
		} {
			role, path := role, path
			t.Run(string(role)+"_"+path, func(t *testing.T) {
				code := postOMTenantRoute(t, omRouteSession{
					role: role, userID: "u-sysadmin", systemAdmin: true,
				}, path)
				require.Equal(t, http.StatusBadRequest, code,
					"system admin with workspace role %s must pass (普通 g.Owner() 会误拒)", role)
			})
		}
	}
}

func TestOwnerTenantRoutesNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		for _, path := range []string{
			"/api/v1/tenants/7/transfer-owner",
			"/api/v1/tenants/7/owners/create",
		} {
			role, path := role, path
			t.Run(string(role)+"_"+path, func(t *testing.T) {
				code := postOMTenantRoute(t, omRouteSession{
					role: role, userID: "u-ordinary",
				}, path)
				require.Equal(t, http.StatusForbidden, code,
					"ordinary %s must be rejected by the OwnerOrSystemAdmin guard", role)
			})
		}
	}
}

func TestOwnerTenantRoutesRejectPathTenantMismatch(t *testing.T) {
	for _, path := range []string{
		"/api/v1/tenants/8/transfer-owner",
		"/api/v1/tenants/8/owners/create",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			code := postOMTenantRoute(t, omRouteSession{
				role: types.TenantRoleOwner, userID: "u-owner", tenantID: 7,
			}, path)
			require.Equal(t, http.StatusForbidden, code,
				"an owner of workspace 7 must not drive workspace 8 by URL")
		})
	}
}

func TestOwnerTenantRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := omRBACGuards()

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	for _, path := range []string{
		"/api/v1/tenants/7/transfer-owner",
		"/api/v1/tenants/7/owners/create",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"to_user_id":"x"}`))
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code,
				"unauthenticated owner management must be rejected by backend auth, not only hidden in the UI")
		})
	}
}

func TestOwnerSystemAdminRoutesNonAdminRejected(t *testing.T) {
	for _, path := range []string{
		"/api/v1/system/admin/users/u-target/owner-space",
		"/api/v1/system/admin/owners/invite",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			code := postOMSystemAdminRoute(t, omRouteSession{
				role: types.TenantRoleOwner, userID: "u-owner",
			}, path)
			require.Equal(t, http.StatusForbidden, code,
				"a workspace owner without system-admin privileges must be rejected")
		})
	}
}

func TestOwnerSystemAdminRoutesSystemAdminPassesGuard(t *testing.T) {
	for _, path := range []string{
		"/api/v1/system/admin/users/u-target/owner-space",
		"/api/v1/system/admin/owners/invite",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			code := postOMSystemAdminRoute(t, omRouteSession{
				role: types.TenantRoleViewer, userID: "u-sysadmin", systemAdmin: true,
			}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"a system administrator must pass the SystemAdmin guard and reach the handler")
		})
	}
}

func TestOwnerSystemAdminRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := omRBACGuards()

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterOwnerManagementRoutes(engine.Group("/api/v1"), &handler.OwnerManagementHandler{}, g)

	for _, path := range []string{
		"/api/v1/system/admin/users/u-target/owner-space",
		"/api/v1/system/admin/owners/invite",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}
