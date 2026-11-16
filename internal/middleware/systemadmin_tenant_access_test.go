package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// tenantMemberLookupStub 只实现 resolveTenantRole/IsTenantAccessible 用到的
// 两个方法；其余方法由嵌入接口兜底（本测试不会调用）。
type tenantMemberLookupStub struct {
	interfaces.TenantMemberService
	membership *types.TenantMember
}

func (s *tenantMemberLookupStub) GetMembership(context.Context, string, uint64) (*types.TenantMember, error) {
	return s.membership, nil
}

func (s *tenantMemberLookupStub) HasAnyMembers(context.Context, uint64) (bool, error) {
	return false, nil
}

func rbacOnConfig() *config.Config {
	on := true
	return &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &on}}
}

// F1/F22：纯系统管理员（IsSystemAdmin=true、CanAccessAllTenants=false、无成员
// 关系）必须被认证层当作平台级身份放行；普通用户仍按成员关系判定。
func TestSystemAdminTenantAccessBypass(t *testing.T) {
	ctx := context.Background()
	otherTenant := uint64(42)
	admin := &types.User{ID: "u-admin", TenantID: 7, IsSystemAdmin: true}
	ordinary := &types.User{ID: "u-user", TenantID: 7}
	members := &tenantMemberLookupStub{}

	require.True(t, IsTenantAccessible(ctx, admin, otherTenant, members, rbacOnConfig()),
		"system admin must access any tenant without membership")
	require.False(t, IsTenantAccessible(ctx, ordinary, otherTenant, members, rbacOnConfig()),
		"ordinary user without membership must stay blocked")

	role, ok := resolveTenantRole(ctx, members, admin, otherTenant, false, rbacOnConfig())
	require.True(t, ok, "system admin must resolve a temporary role")
	require.Equal(t, types.TenantRoleAdmin, role)

	role, ok = resolveTenantRole(ctx, members, ordinary, otherTenant, false, rbacOnConfig())
	require.False(t, ok, "ordinary user without membership must fail closed")
	require.Empty(t, role)
}

// F22：/tenants/all、/tenants/search 的跨空间守卫对系统管理员放行，
// 即使集群跨空间开关关闭；普通用户仍被拒。
func TestRequireCrossTenantAccessAllowsSystemAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Tenant: &config.TenantConfig{}} // EnableCrossTenantAccess=false

	serve := func(user *types.User) int {
		engine := gin.New()
		engine.Use(ErrorHandler())
		engine.Use(func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), types.UserContextKey, user)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		})
		engine.GET("/tenants/all", RequireCrossTenantAccess(cfg), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tenants/all", nil))
		return w.Code
	}

	require.Equal(t, http.StatusOK, serve(&types.User{ID: "u-admin", IsSystemAdmin: true}),
		"system admin must reach cross-tenant list/search endpoints")
	require.Equal(t, http.StatusForbidden, serve(&types.User{ID: "u-user"}),
		"ordinary user must stay blocked when cross-tenant access is disabled")
}
