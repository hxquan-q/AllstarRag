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

// T03 路由注册 + 守卫红阶段用例（AC-38、C4、T03-2）。
//
// 假定契约（02-plan §3.1）：
//   - RegisterUserManagementRoutes 新增
//     POST /tenants/:id/members/:user_id/purge；
//   - 与 ban/unban 相同的 /tenants/:id 组级 PathTenantMatch 与逐路由
//     g.AdminOrSystemAdmin()（T06 起普通管理员可 purge 游客）；
//   - 未认证 → 401；contributor/viewer → 403；路径租户与活动租户不一致 → 403。
//
// 复用 T02 的 umRouteSession / newUMRouteEngine / postUMRouteWithSession：
// 守卫放行时请求到达 handler，故意发非法 JSON 让 ShouldBindJSON 返回 400。

const umPurgePath = "/api/v1/tenants/7/members/u-target/purge"

func TestUserManagementPurgeRouteIsRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}
	engine := gin.New()
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered["POST /api/v1/tenants/:id/members/:user_id/purge"],
		"POST /tenants/:id/members/:user_id/purge must be registered")
}

func TestUserManagementPurgeRouteOwnerPassesGuard(t *testing.T) {
	code := postUMRouteWithSession(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner",
	}, umPurgePath)
	require.Equal(t, http.StatusBadRequest, code,
		"workspace owner must pass the OwnerOrSystemAdmin guard and reach the handler")
}

func TestUserManagementPurgeRouteSystemAdminPassesGuardRegardlessOfRole(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			code := postUMRouteWithSession(t, umRouteSession{
				role: role, userID: "u-sysadmin", systemAdmin: true,
			}, umPurgePath)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass (普通 g.Owner() 会误拒)", role)
		})
	}
}

func TestUserManagementPurgeRouteAdminPassesGuard(t *testing.T) {
	code := postUMRouteWithSession(t, umRouteSession{
		role: types.TenantRoleAdmin, userID: "u-admin",
	}, umPurgePath)
	require.Equal(t, http.StatusBadRequest, code,
		"ordinary admin must pass the AdminOrSystemAdmin guard and reach the handler")
}

func TestUserManagementPurgeRouteNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			code := postUMRouteWithSession(t, umRouteSession{
				role: role, userID: "u-ordinary",
			}, umPurgePath)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the OwnerOrSystemAdmin guard", role)
		})
	}
}

func TestUserManagementPurgeRouteRejectsPathTenantMismatch(t *testing.T) {
	code := postUMRouteWithSession(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner", tenantID: 7,
	}, "/api/v1/tenants/8/members/u-target/purge")
	require.Equal(t, http.StatusForbidden, code,
		"an owner of workspace 7 must not purge a member of workspace 8 by URL")
}

func TestUserManagementPurgeRouteRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	g := &rbacGuards{cfg: cfg}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, umPurgePath,
		strings.NewReader(`{"confirm_email":"target@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated purge must be rejected by backend auth, not only hidden in the UI")
}
