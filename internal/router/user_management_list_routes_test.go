package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T05 路由注册 + 守卫红阶段用例（T05-1/5/6、AC-38、C4）。
//
// 假定契约（02-plan §3.1）：
//   - RegisterUserManagementRoutes 增加
//     GET /api/v1/tenants/:id/user-management；
//   - /tenants/:id 组级 PathTenantMatch，逐路由 g.AdminOrSystemAdmin()
//     （T06 起普通管理员可读当前空间列表）；
//   - 现有 GET /tenants/:id/members（Viewer+）行为不变。
//
// 守卫放行 → 到达 handler；本轮用「非法 role/page → 400」作为可观察信号，
// 这样无需构造假 service 也不会触发零值 handler 的 nil 依赖。

const umListRouterTenantID uint64 = 7

func newUMListRouteEngine(t *testing.T, s umRouteSession) *gin.Engine {
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

func getUMListRouteWithSession(t *testing.T, s umRouteSession, path string) int {
	t.Helper()
	if s.tenantID == 0 {
		s.tenantID = umListRouterTenantID
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	newUMListRouteEngine(t, s).ServeHTTP(w, req)
	return w.Code
}

func TestUserManagementListRouteIsRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}
	engine := gin.New()
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{}, g)

	registered := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/api/v1/tenants/:id/user-management" {
			registered = true
			break
		}
	}
	require.True(t, registered,
		"GET /api/v1/tenants/:id/user-management must be registered in RegisterUserManagementRoutes")
}

func TestUserManagementListRouteOwnerPassesGuard(t *testing.T) {
	code := getUMListRouteWithSession(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner",
	}, "/api/v1/tenants/7/user-management?role=superuser&page=0")

	require.Equal(t, http.StatusBadRequest, code,
		"workspace owner must pass the OwnerOrSystemAdmin guard and reach the handler "+
			"(400 = invalid role/page parsed after the guard; 404/403 means the route/guard is missing)")
}

func TestUserManagementListRouteSystemAdminPassesGuardRegardlessOfRole(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			code := getUMListRouteWithSession(t, umRouteSession{
				role: role, userID: "u-sysadmin", systemAdmin: true,
			}, "/api/v1/tenants/7/user-management?role=superuser&page=0")

			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass (普通 g.Owner() 会误拒)", role)
		})
	}
}

func TestUserManagementListRouteAdminPassesGuard(t *testing.T) {
	// 非法 scope 让 handler 在触达 service 前返回 400；403 说明守卫仍拒绝 admin。
	code := getUMListRouteWithSession(t, umRouteSession{
		role: types.TenantRoleAdmin, userID: "u-admin",
	}, "/api/v1/tenants/7/user-management?scope=bogus")
	require.Equal(t, http.StatusBadRequest, code,
		"ordinary admin must pass the AdminOrSystemAdmin guard and reach the handler")
}

func TestUserManagementListRouteNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			code := getUMListRouteWithSession(t, umRouteSession{
				role: role, userID: "u-ordinary",
			}, "/api/v1/tenants/7/user-management")

			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the OwnerOrSystemAdmin route guard", role)
		})
	}
}

func TestUserManagementListRouteRejectsPathTenantMismatch(t *testing.T) {
	code := getUMListRouteWithSession(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner", tenantID: 7,
	}, "/api/v1/tenants/8/user-management")

	require.Equal(t, http.StatusForbidden, code,
		"URL :id must match the active workspace (cross-tenant superusers excluded)")
}

func TestUserManagementListRouteRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterUserManagementRoutes(engine.Group("/api/v1"), &handler.UserManagementHandler{},
		&rbacGuards{cfg: cfg})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/7/user-management", nil)
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated user-management list must be rejected by backend auth")
}

// umListStubMemberService 只实现 ListMembersPage；其余方法由嵌入接口兜底。
type umListStubMemberService struct {
	interfaces.TenantMemberService
}

func (s *umListStubMemberService) ListMembersPage(
	_ context.Context, tenantID uint64, _ string, _, _ int,
) ([]*types.TenantMember, int64, error) {
	return []*types.TenantMember{{
		UserID:   "u-viewer",
		TenantID: tenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: time.Now(),
	}}, 1, nil
}

// umListStubUserService 只实现批次用户查询。
type umListStubUserService struct {
	interfaces.UserService
}

func (s *umListStubUserService) GetUsersByIDs(
	_ context.Context, _ []string,
) (map[string]*types.User, error) {
	return map[string]*types.User{
		"u-viewer": {ID: "u-viewer", Email: "viewer@example.com", Username: "viewer"},
	}, nil
}

// TestTenantMembersListRouteStillAvailableToViewer 是 T05-6 的回归保护网：
// 现有 GET /tenants/:id/members 仍注册、仍允许 viewer，响应结构不变。
func TestTenantMembersListRouteStillAvailableToViewer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, umListRouterTenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "u-viewer")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, false)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	memberHandler := handler.NewTenantMemberHandler(
		&umListStubMemberService{}, &umListStubUserService{},
	)
	RegisterTenantRoutes(
		engine.Group("/api/v1"), &handler.TenantHandler{}, memberHandler, nil, nil, g,
	)

	registered := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/api/v1/tenants/:id/members" {
			registered = true
			break
		}
	}
	require.True(t, registered, "GET /tenants/:id/members must stay registered")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/7/members", nil)
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"viewer must keep read access to GET /tenants/:id/members")
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Members []struct {
				UserID   string `json:"user_id"`
				Email    string `json:"email"`
				Username string `json:"username"`
				Role     string `json:"role"`
				Status   string `json:"status"`
			} `json:"members"`
			Total    int64 `json:"total"`
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Len(t, payload.Data.Members, 1)
	require.Equal(t, "u-viewer", payload.Data.Members[0].UserID)
	require.Equal(t, "viewer@example.com", payload.Data.Members[0].Email)
	require.Equal(t, "viewer", payload.Data.Members[0].Role)
	require.Equal(t, int64(1), payload.Data.Total)
	require.Equal(t, 1, payload.Data.Page)
	require.Equal(t, 20, payload.Data.PageSize)
}
