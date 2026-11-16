package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T22 路由层红阶段用例（AC-1/AC-2，完整 router → handler → service 链路）。
//
// 关注点：多角色过滤在真实服务上生效、单值零回归、非法参数 400、
// scope=all 与多值组合。

func umMultiRoleRouteIDs(t *testing.T, w *httptest.ResponseRecorder) (int64, []string) {
	t.Helper()
	var payload umScopeRoutePayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	ids := make([]string, 0, len(payload.Data.Items))
	for _, item := range payload.Data.Items {
		ids = append(ids, item.UserID)
	}
	return payload.Data.Total, ids
}

func TestUserManagementMultiRoleRouteFiltersAnyRole(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner-a",
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-admin-a", "admin-a@example.com", "admin-a",
		umScopeTenantA, types.TenantRoleAdmin)
	env.seedMember(t, "u-contrib-a", "contrib-a@example.com", "contrib-a",
		umScopeTenantA, types.TenantRoleContributor)
	env.seedMember(t, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umScopeTenantA, types.TenantRoleViewer)

	w := umScopeDoGet(t, env,
		"/api/v1/tenants/10/user-management?role=contributor,admin")
	require.Equal(t, http.StatusOK, w.Code)
	total, ids := umMultiRoleRouteIDs(t, w)
	require.Equal(t, int64(2), total)
	require.ElementsMatch(t, []string{"u-admin-a", "u-contrib-a"}, ids)
}

func TestUserManagementMultiRoleRouteSingleValueZeroRegression(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner-a",
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-admin-a", "admin-a@example.com", "admin-a",
		umScopeTenantA, types.TenantRoleAdmin)
	env.seedMember(t, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umScopeTenantA, types.TenantRoleViewer)

	w := umScopeDoGet(t, env, "/api/v1/tenants/10/user-management?role=admin")
	require.Equal(t, http.StatusOK, w.Code)
	total, ids := umMultiRoleRouteIDs(t, w)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"u-admin-a"}, ids)
}

func TestUserManagementMultiRoleRouteRejectsMalformedRole(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner-a",
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)

	for _, query := range []string{
		"role=contributor,",
		"role=,admin",
		"role=Admin",
		"role=admin,superuser",
	} {
		query := query
		t.Run(query, func(t *testing.T) {
			w := umScopeDoGet(t, env,
				"/api/v1/tenants/10/user-management?"+query)
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestUserManagementMultiRoleRouteScopedAllCombinesScopeAndRoles(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleViewer, userID: "u-sys-admin", systemAdmin: true,
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-admin-b", "admin-b@example.com", "admin-b",
		umScopeTenantB, types.TenantRoleAdmin)
	env.seedMember(t, "u-viewer-b", "viewer-b@example.com", "viewer-b",
		umScopeTenantB, types.TenantRoleViewer)

	w := umScopeDoGet(t, env,
		"/api/v1/tenants/10/user-management?scope=all&role=admin,contributor")
	require.Equal(t, http.StatusOK, w.Code)
	total, ids := umMultiRoleRouteIDs(t, w)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"u-admin-b"}, ids)

	// 单值 + scope=all 继续走旧 scoped 方法，结果一致（零破坏）。
	w = umScopeDoGet(t, env,
		"/api/v1/tenants/10/user-management?scope=all&role=admin")
	require.Equal(t, http.StatusOK, w.Code)
	total, ids = umMultiRoleRouteIDs(t, w)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"u-admin-b"}, ids)
}
