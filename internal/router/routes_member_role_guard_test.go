package router

import (
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

// T12 TDD 红阶段：PUT /tenants/:id/members/:user_id 的路由守卫。
//
// 契约（T12-1、AC-38、AC-6）：
//   - 该路由由 g.Owner() 改为 g.OwnerOrSystemAdmin()，系统管理员无需
//     本空间 owner 角色即可进入 handler；
//   - 普通 admin/contributor/viewer 仍 403；
//   - 未认证 401；URL 租户与活动租户不一致 403。
//
// 放行探测：请求体用 {"role":"not-a-role"}（非法角色），handler 在
// 触达 service 之前就返回 400，因此不需要为路由测试装配真实 service。
// 「400 而非 403」证明请求穿过了守卫到达 handler。

const memberRoleGuardPath = "/api/v1/tenants/7/members/u-target"

func putMemberRoleRoute(t *testing.T, s memberGuardSession, path string) int {
	t.Helper()
	r := memberGuardRouter(t, s)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"role":"not-a-role"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code
}

// TestMemberRoleRouteAllowsSystemAdmin 是 T12 的核心红点：当前路由挂的是
// g.Owner()，工作区角色为 viewer/contributor 的系统管理员会被 403 挡在
// handler 之外；改为 OwnerOrSystemAdmin 后应到达 handler（400）。
func TestMemberRoleRouteAllowsSystemAdmin(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleViewer,
		types.TenantRoleContributor,
	} {
		role := role
		t.Run("system_admin_with_"+string(role), func(t *testing.T) {
			code := putMemberRoleRoute(t, memberGuardSession{
				role: role, userID: "u-sysadmin", systemAdmin: true,
			}, memberRoleGuardPath)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass the OwnerOrSystemAdmin guard "+
					"and reach the handler (403 means the route still uses g.Owner())", role)
		})
	}
}

// TestMemberRoleRouteWorkspaceRoleMatrix：T06 起 admin 也放行（co-owner 能力集），
// owner/admin 放行，contributor/viewer 仍 403。
func TestMemberRoleRouteWorkspaceRoleMatrix(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleAdmin} {
		role := role
		t.Run("workspace_"+string(role)+"_passes", func(t *testing.T) {
			code := putMemberRoleRoute(t, memberGuardSession{
				role: role, userID: "u-actor",
			}, memberRoleGuardPath)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace %s must pass the route guard and reach the handler", role)
		})
	}

	for _, role := range []types.TenantRole{
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run("ordinary_"+string(role)+"_rejected_403", func(t *testing.T) {
			code := putMemberRoleRoute(t, memberGuardSession{
				role: role, userID: "u-ordinary",
			}, memberRoleGuardPath)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the OwnerOrSystemAdmin guard", role)
		})
	}
}

// TestMemberRoleRouteRejectsPathTenantMismatch 覆盖租户隔离：活动租户 7
// 的 owner 不能通过改 URL 改租户 8 的成员角色。
func TestMemberRoleRouteRejectsPathTenantMismatch(t *testing.T) {
	code := putMemberRoleRoute(t, memberGuardSession{
		role: types.TenantRoleOwner, userID: "u-owner",
	}, "/api/v1/tenants/8/members/u-target")
	require.Equal(t, http.StatusForbidden, code,
		"an owner of workspace 7 must not change roles in workspace 8 via the URL")
}

// TestMemberRoleRouteRequiresAuthentication 覆盖 AC-38 / 红线 C4：
// 未认证请求必须由后端 Auth 中间件返回 401，而不是只靠前端隐藏按钮。
func TestMemberRoleRouteRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rbacOn := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}
	g := &rbacGuards{cfg: cfg}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterTenantRoutes(
		engine.Group("/api/v1"),
		&handler.TenantHandler{},
		&handler.TenantMemberHandler{},
		nil,
		nil,
		g,
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, memberRoleGuardPath, strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated role change must be rejected by backend auth, not only hidden in the UI")
}
