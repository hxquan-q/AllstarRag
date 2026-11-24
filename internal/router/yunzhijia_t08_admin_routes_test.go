package router

// T08 测试：云之家管理端路由权限（AC-14 / AC-16 / C4）。
//
// 真实 gin 路由 + g.SystemAdmin() 守卫：系统管理员 200；owner/viewer
// 与未登录一律 403（未登录在生产链路由 Auth 中间件先返回 401，这里
// 只验证平台守卫不放行）。handler 内还有第二层独立校验，见 handler 测试。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type yzjT08RouteRunRepo struct {
	interfaces.YunzhijiaSyncRunRepository
}

func (r *yzjT08RouteRunRepo) Latest(context.Context) (*types.YunzhijiaSyncRun, error) {
	return nil, nil
}

func (r *yzjT08RouteRunRepo) CreateRunning(
	_ context.Context, run *types.YunzhijiaSyncRun,
) error {
	run.ID = 1
	return nil
}

func (r *yzjT08RouteRunRepo) Finish(context.Context, *types.YunzhijiaSyncRun) error {
	return nil
}

type yzjT08RouteEmployeeRepo struct {
	interfaces.YunzhijiaEmployeeRepository
	blocked []*types.YunzhijiaEmployee
}

func (r *yzjT08RouteEmployeeRepo) ListBlocked(
	context.Context,
) ([]*types.YunzhijiaEmployee, error) {
	return r.blocked, nil
}

func (r *yzjT08RouteEmployeeRepo) ListByStatus(
	context.Context, types.YunzhijiaEmployeeStatus,
) ([]*types.YunzhijiaEmployee, error) {
	return nil, nil
}

func (r *yzjT08RouteEmployeeRepo) UpsertByOpenID(
	context.Context, *types.YunzhijiaEmployee,
) error {
	return nil
}

func (r *yzjT08RouteEmployeeRepo) GetByOpenID(
	_ context.Context, openID string,
) (*types.YunzhijiaEmployee, error) {
	return &types.YunzhijiaEmployee{
		OpenID:     openID,
		Name:       "占位员工",
		Department: "金蝶社区",
		Status:     types.YunzhijiaEmployeeStatusActive,
		TenantID:   10024,
	}, nil
}

func (r *yzjT08RouteEmployeeRepo) Unblock(_ context.Context, openID string) error {
	for _, row := range r.blocked {
		if row.OpenID == openID {
			return nil
		}
	}
	return repository.ErrYunzhijiaEmployeeNotFound
}

type yzjT08RouteClient struct{}

func (c *yzjT08RouteClient) QueryUserName(context.Context, string) (string, error) {
	return "", yunzhijia.ErrInvalidTicket
}

func (c *yzjT08RouteClient) ListEmployees(
	_ context.Context, status yunzhijia.Status, _ int, _ int,
) (*yunzhijia.EmployeePage, error) {
	if status == yunzhijia.StatusActive {
		// 整轮同步要求在职总数 > 0；给一条占位员工让管理端接口跑通。
		return &yunzhijia.EmployeePage{
			List: []yunzhijia.Employee{{
				OpenID:     "open-route-placeholder",
				Name:       "占位员工",
				Department: "金蝶社区",
			}},
			Page: 1, PageSize: 100, Total: 1,
		}, nil
	}
	return &yunzhijia.EmployeePage{Page: 1, PageSize: 100, Total: 0}, nil
}

type yzjT08RouteLifecycle struct{}

func (l *yzjT08RouteLifecycle) ApplyEmployee(
	context.Context, *types.YunzhijiaEmployee,
) (types.YunzhijiaAccountResult, error) {
	return types.YunzhijiaAccountResultNone, nil
}

func (l *yzjT08RouteLifecycle) ApplyInactive(
	context.Context, *types.YunzhijiaEmployee,
) (types.YunzhijiaAccountResult, error) {
	return types.YunzhijiaAccountResultNone, nil
}

type yzjT08RouteActor struct {
	userID      string
	role        types.TenantRole
	systemAdmin bool
}

func yzjT08RouteEngine(t *testing.T, actor yzjT08RouteActor) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rbacOn := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}

	g := &rbacGuards{cfg: cfg}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		if actor.userID != "" {
			ctx = context.WithValue(ctx, types.UserIDContextKey, actor.userID)
		}
		if actor.role != "" {
			ctx = context.WithValue(ctx, types.TenantRoleContextKey, actor.role)
		}
		if actor.systemAdmin {
			ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	ycfg := &config.YunzhijiaSSOConfig{
		Enabled:             true,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/employees",
		TicketUserPath:      "/ticket/user",
		AppID:               "app-placeholder",
		TenantDefault:       10024,
		TenantHR:            10022,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            100,
		HTTPTimeoutSeconds:  10,
	}
	employees := &yzjT08RouteEmployeeRepo{
		blocked: []*types.YunzhijiaEmployee{{
			OpenID:     "open-blocked-route-placeholder",
			Name:       "禁建占位员工",
			Department: "金蝶社区",
			Status:     types.YunzhijiaEmployeeStatusActive,
		}},
	}
	runs := &yzjT08RouteRunRepo{}
	syncSvc := service.NewYunzhijiaSyncService(
		ycfg, &yzjT08RouteClient{}, employees, runs, &yzjT08RouteLifecycle{},
	)
	adminSvc := service.NewYunzhijiaAdminService(ycfg, syncSvc, runs, employees, nil)
	h := handler.NewYunzhijiaAdminHandler(adminSvc)
	RegisterYunzhijiaAdminRoutes(engine.Group("/api/v1"), h, g)
	return engine
}

func yzjT08RouteDo(t *testing.T, engine *gin.Engine, method, path string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w.Code
}

func yzjT08AdminRoutes() []struct {
	method string
	path   string
} {
	return []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/system/admin/yunzhijia/sync"},
		{http.MethodPost, "/api/v1/system/admin/yunzhijia/sync/preview"},
		{http.MethodPost, "/api/v1/system/admin/yunzhijia/sync"},
		{http.MethodPost, "/api/v1/system/admin/yunzhijia/employees/open-blocked-route-placeholder/unblock"},
	}
}

func TestYunzhijiaT08SystemAdminGets200(t *testing.T) {
	engine := yzjT08RouteEngine(t, yzjT08RouteActor{
		userID:      "sys-admin-placeholder",
		role:        types.TenantRoleOwner,
		systemAdmin: true,
	})
	for _, route := range yzjT08AdminRoutes() {
		if code := yzjT08RouteDo(t, engine, route.method, route.path); code != http.StatusOK {
			t.Fatalf("%s %s 系统管理员应 200，实际 %d", route.method, route.path, code)
		}
	}
}

func TestYunzhijiaT08NonAdminsGet403(t *testing.T) {
	cases := []struct {
		name  string
		actor yzjT08RouteActor
	}{
		{"owner", yzjT08RouteActor{userID: "owner-placeholder", role: types.TenantRoleOwner}},
		{"viewer", yzjT08RouteActor{userID: "viewer-placeholder", role: types.TenantRoleViewer}},
		{"unauthenticated", yzjT08RouteActor{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := yzjT08RouteEngine(t, tc.actor)
			for _, route := range yzjT08AdminRoutes() {
				code := yzjT08RouteDo(t, engine, route.method, route.path)
				// 未登录在生产链路会先被 Auth 中间件挡成 401；本例只验证
				// 平台守卫不会放行，401/403 均可接受。
				if code != http.StatusForbidden && code != http.StatusUnauthorized {
					t.Fatalf("%s %s 应被拒绝（401/403），实际 %d",
						route.method, route.path, code)
				}
			}
		})
	}
}
