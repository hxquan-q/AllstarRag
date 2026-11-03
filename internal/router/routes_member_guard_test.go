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

// T01（gpt-review-fixes）针对 members 两个建账号/重置密码入口的路由守卫测试：
//
//	POST /tenants/:id/members/create
//	POST /tenants/:id/members/:user_id/reset-password
//
// handler 层已把平台系统管理员按 Owner 处理（tenant_member.go 的
// 「系统管理员按 owner 处理」），因此当前工作区角色只是 viewer/contributor
// 的系统管理员不能被路由守卫弹开——否则 handler 级矩阵恰恰对它专门
// 服务的这批调用方永远走不到。
//
// 测试通过 RegisterTenantRoutes 注册真实路由树（与生产同一条守卫链：
// /:id 组上的 g.PathTenantMatch() 加逐路由角色守卫）并发出真实 HTTP
// 请求，只有真实路由声明变了才会转绿。
//
// 可观察契约：
//
//	守卫拒绝 -> 403（RBAC 中间件直接写出）
//	守卫放行 -> 400（请求到达真实 handler，故意留空的 body 让
//	             ShouldBindJSON 失败、ErrorHandler 渲染校验错误；
//	             不触发 service 调用，也没有 nil-service panic）
//
// RBAC 强制显式开启：EnableRBAC 关闭时 RequireRole 会 fail-open，
// 那样所有用例都会通过，bug 就被藏住了。

const memberGuardTenantID uint64 = 7

// memberGuardSession 复刻 middleware/auth.go 的 JWT 路径（applyAuthSession）
// 给租户 7 内调用方挂上的信息：工作区角色、用户 id、平台系统管理员标志。
// 守卫从请求上下文读这些值，所以只种上下文就够（与
// internal/middleware/rbac_test.go 的 rbacTestHarness 同款风格）。
type memberGuardSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
}

// memberGuardRouter 建一个挂真实租户路由树的新 engine。每个用例各建一个，
// 种入中间件才能保持为对 session 结构体的简单闭包。
// EnableCrossTenantAccess 保持关闭，谁也误触不了 RequireRole /
// RequirePathTenantMatch 里的跨租户超级用户旁路——本测试只测系统管理员路径。
func memberGuardRouter(t *testing.T, s memberGuardSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, memberGuardTenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	// invitation/audit handler 传 nil：它们的子路由与生产装配一样按 nil
	// 条件注册（nil 即跳过），被测的两条 member 路由挂在
	// `memberHandler != nil` 分支下。
	RegisterTenantRoutes(
		engine.Group("/api/v1"),
		&handler.TenantHandler{},
		&handler.TenantMemberHandler{},
		nil,
		nil,
		g,
	)
	return engine
}

// postMemberRoute 发出的 POST body 会让 handler 的 JSON 绑定失败，这样
// 「守卫放行」就确定性地表现为 400，且不会调起任何（零值的）service 依赖。
func postMemberRoute(t *testing.T, s memberGuardSession, path string) int {
	t.Helper()
	r := memberGuardRouter(t, s)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code
}

func TestMemberGuardCreateMemberAccountRoute(t *testing.T) {
	const path = "/api/v1/tenants/7/members/create"

	// A：工作区角色是 viewer/contributor 的平台系统管理员必须到达 handler
	// （再由 handler 按「系统管理员按 owner」矩阵处理）。修复前守卫是
	// Admin-only，会抢在 handler 自有规则运行之前把这批调用方拒掉。
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("system_admin_with_"+string(role)+"_reaches_handler", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-sysadmin", systemAdmin: true}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass the route guard and reach the handler (403 means the guard rejected them)", role)
		})
	}

	// B：普通 viewer/contributor（非系统管理员）必须仍是 403。这组同时是
	// 金丝雀，证明本测试架子里 RBAC 强制确实开着——一旦变成非 403，说明
	// 配置悄悄 fail-open，A/C 两组的证据也就全部作废。
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("ordinary_"+string(role)+"_rejected_403", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-ordinary"}, path)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the Admin+ route guard", role)
		})
	}

	// C：工作区 admin/owner 照旧放行（矩阵不回归）。
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run("workspace_"+string(role)+"_passes", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-admin"}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace %s must keep passing the route guard", role)
		})
	}
}

func TestMemberGuardResetMemberPasswordRoute(t *testing.T) {
	// 目标用户 id 与种入的所有调用方 id 都不同，handler 的「不能重置自己
	// 密码」前置检查才不会短路。
	const path = "/api/v1/tenants/7/members/u-guard-target/reset-password"

	// A：工作区角色较低的平台系统管理员必须到达 handler（handler 自有
	// 矩阵会授予 owner 级重置权限）。
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("system_admin_with_"+string(role)+"_reaches_handler", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-sysadmin", systemAdmin: true}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass the route guard and reach the handler (403 means the guard rejected them)", role)
		})
	}

	// B：普通 viewer/contributor 仍是 403。
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("ordinary_"+string(role)+"_rejected_403", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-ordinary"}, path)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the Admin+ route guard", role)
		})
	}

	// C：工作区 admin/owner 照旧放行。
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run("workspace_"+string(role)+"_passes", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-admin"}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace %s must keep passing the route guard", role)
		})
	}
}
