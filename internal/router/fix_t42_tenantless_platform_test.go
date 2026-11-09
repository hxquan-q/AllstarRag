package router

// T42 TDD 红阶段：无租户系统管理员经平台三路由（GPT 复核 #6）。
//
// 票面验收 3（03-tickets/T42.md）：
//   - tenant_id=0、无任何 active 成员关系的系统管理员，用真实 JWT 调
//     POST /system/admin/users/:user_id/ban|unban|purge 不再被 409
//     TENANT_REQUIRED 挡住，能进入 handler（合法请求 200、非法 JSON 400）；
//   - 普通无租户用户仍 403（被 SystemAdmin 守卫拒绝，而不是 409）；
//   - API Key 仍 403（平台路由未声明能力，网关默认拒绝）；
//   - 未认证仍 401。
//
// 复用 T37 的真实 HTTP 夹具：真实 middleware.Auth + 真实 JWT 签名校验 +
// 真实路由注册。UserManagementService 用 T37 替身——本票只证明认证/守卫/
// 路由链路放行到 handler，不在 router 包重复服务层语义。

import (
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// t42AddTenantlessUsers 在 T37 夹具上补两个无租户账号：
//   - 系统管理员：TenantID=0、IsSystemAdmin=true、无成员关系（对应
//     /system/admin/promote 提升后的纯超管）；
//   - 普通用户：TenantID=0、无成员关系，用于证明 403 而不是 409。
func t42AddTenantlessUsers(f *t37Fixture) {
	f.userRepo.users["u-sys-tenantless"] = &types.User{
		ID: "u-sys-tenantless", Username: "sys0", Email: "sys0@example.com",
		IsActive: true, IsSystemAdmin: true,
	}
	f.userRepo.users["u-viewer-tenantless"] = &types.User{
		ID: "u-viewer-tenantless", Username: "viewer0", Email: "viewer0@example.com",
		IsActive: true,
	}
}

func t42PlatformRequest(action, body string, headers map[string]string) t37Request {
	return t37Request{
		method:  http.MethodPost,
		path:    "/api/v1/system/admin/users/u-target/" + action,
		headers: headers,
		body:    body,
	}
}

// R1 + R2：无租户超管经真实 JWT 到达 handler。
func TestT42TenantlessSystemAdminReachesPlatformRoutes(t *testing.T) {
	f := newT37Fixture(t)
	t42AddTenantlessUsers(f)
	engine := f.engine(t, false)
	token := f.issue(t, "u-sys-tenantless", 0) // JWT tenant_id=0，无成员关系

	validBody := map[string]string{
		"ban":   `{"reason":"tenantless admin"}`,
		"unban": `{}`,
		"purge": `{"confirm_email":"target@example.com"}`,
	}
	for action, body := range validBody {
		w := t37Serve(t, engine, t42PlatformRequest(action, body, t37Bearer(token)))
		require.NotEqual(t, http.StatusConflict, w.Code,
			"platform %s must not answer TENANT_REQUIRED for a tenantless system admin; body=%s",
			action, w.Body.String())
		require.Equal(t, http.StatusOK, w.Code,
			"tenantless system admin must reach the handler on platform %s; body=%s",
			action, w.Body.String())
	}

	// 非法 JSON 由 handler 返回 400：同样证明请求已穿过认证与守卫，
	// 而不是在 middleware 被 409 拦下。
	for _, action := range []string{"ban", "unban", "purge"} {
		w := t37Serve(t, engine, t42PlatformRequest(action, `{bad json`, t37Bearer(token)))
		require.Equal(t, http.StatusBadRequest, w.Code,
			"malformed JSON reaching the handler proves platform %s passed tenantless auth", action)
	}
}

// R3：普通无租户用户不是系统管理员 → 403（而不再是无租户 409）。
func TestT42TenantlessOrdinaryUserStillForbidden(t *testing.T) {
	f := newT37Fixture(t)
	t42AddTenantlessUsers(f)
	engine := f.engine(t, false)
	token := f.issue(t, "u-viewer-tenantless", 0)

	for _, action := range []string{"ban", "unban", "purge"} {
		w := t37Serve(t, engine, t42PlatformRequest(action,
			`{"confirm_email":"target@example.com"}`, t37Bearer(token)))
		require.Equal(t, http.StatusForbidden, w.Code,
			"ordinary tenantless user must be 403 (SystemAdmin guard), not 409, on platform %s", action)
	}
}

// R4：API Key 默认拒绝保持原样（平台 key 与 workspace key 都不得旁路）。
func TestT42PlatformRoutesStillDenyAPIKeys(t *testing.T) {
	f := newT37Fixture(t)
	t42AddTenantlessUsers(f)
	// 平台 key：认证层允许无 X-Tenant-ID 通过 system/admin 前缀，但路由未
	// 声明能力，API Key 网关必须默认拒绝。
	f.apiKeySvc.keys["sk-platform-t42"] = &types.TenantAPIKey{
		ID: 42, ScopeType: types.APIKeyScopePlatform, Name: "t42-platform",
		FullAccess: true,
	}
	engine := f.engine(t, false)

	body := map[string]string{
		"ban":   `{}`,
		"unban": `{}`,
		"purge": `{"confirm_email":"target@example.com"}`,
	}
	for _, key := range []string{"sk-platform-t42", "sk-manage-members"} {
		for action, reqBody := range body {
			w := t37Serve(t, engine, t42PlatformRequest(action, reqBody,
				map[string]string{"X-API-Key": key}))
			require.Equal(t, http.StatusForbidden, w.Code,
				"platform %s must stay default-deny for API key %s", action, key)
		}
	}
}

// R5：未认证仍 401。
func TestT42PlatformRoutesStillRequireAuthentication(t *testing.T) {
	f := newT37Fixture(t)
	t42AddTenantlessUsers(f)
	engine := f.engine(t, false)

	for _, action := range []string{"ban", "unban", "purge"} {
		w := t37Serve(t, engine, t42PlatformRequest(action, `{}`, nil))
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"unauthenticated platform %s must stay 401", action)
	}
}
