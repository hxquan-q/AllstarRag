package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T44 路由层红阶段用例：真实 router → handler → service 链路上的
// without_membership 参数与 no_membership_count 字段（Codex 复核 ①）。
//
// 复用 T16 的 newUMScopeRouteEnv（真实 user management service + SQLite）：
//   - 系统管理员 scope=all & without_membership=true → 200，只返回无空间
//     账号，响应带 no_membership_count；
//   - 系统管理员 scope=all（不带过滤）→ 200，同样带 no_membership_count；
//   - owner（非系统管理员）scope=all & without_membership=true → 403；
//   - scope=current/other 带 without_membership=true → 400。

// t44SeedLoner 在路由环境里种一个无成员关系账号（types.User 无 GORM
// hook，直接建行与仓储路径等价）。
func t44SeedLoner(t *testing.T, e *umScopeRouteEnv, userID, email, username string) {
	t.Helper()
	require.NoError(t, e.db.Create(&types.User{
		ID:           userID,
		Username:     username,
		Email:        email,
		PasswordHash: "hash-" + userID,
		IsActive:     true,
	}).Error)
}

// t44Payload 解析路由层响应：data.no_membership_count 用指针区分缺席。
type t44Payload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []struct {
			UserID      string `json:"user_id"`
			Memberships []struct {
				TenantID uint64 `json:"tenant_id"`
			} `json:"memberships"`
		} `json:"items"`
		Total             int64  `json:"total"`
		NoMembershipCount *int64 `json:"no_membership_count"`
	} `json:"data"`
}

// TestT44RouteSystemAdminNoMembershipDirectory 锁定完整链路的行为契约。
func TestT44RouteSystemAdminNoMembershipDirectory(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleViewer, userID: "u-sysadmin", systemAdmin: true,
	})
	// 本空间一个 viewer + 两个无空间账号（一个正常、一个被封禁）。
	env.seedMember(t, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umScopeTenantA, types.TenantRoleViewer)
	t44SeedLoner(t, env, "u-probe-1", "probe-1@example.com", "probe-1")
	t44SeedLoner(t, env, "u-probe-2", "probe-2@example.com", "probe-2")
	require.NoError(t, env.db.Exec(
		"UPDATE users SET is_active = false WHERE id = ?", "u-probe-2").Error)

	t.Run("filtered_listing_returns_only_membershipless", func(t *testing.T) {
		w := umScopeDoGet(t, env,
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true")
		require.Equal(t, http.StatusOK, w.Code)

		var payload t44Payload
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		require.Equal(t, int64(2), payload.Data.Total,
			"无空间目录只含两个 probe 账号，不含本空间 viewer")
		require.Len(t, payload.Data.Items, 2)
		for _, item := range payload.Data.Items {
			require.Contains(t, []string{"u-probe-1", "u-probe-2"}, item.UserID)
			require.Empty(t, item.Memberships, "无空间账号的 memberships 为空")
		}
		require.NotNil(t, payload.Data.NoMembershipCount,
			"scope=all 响应必须带 no_membership_count")
		require.Equal(t, int64(2), *payload.Data.NoMembershipCount,
			"no_membership_count 与过滤 total 一致（无 q 时）")
	})

	t.Run("unfiltered_all_also_carries_count", func(t *testing.T) {
		w := umScopeDoGet(t, env,
			"/api/v1/tenants/10/user-management?scope=all")
		require.Equal(t, http.StatusOK, w.Code)

		var payload t44Payload
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		require.Equal(t, int64(3), payload.Data.Total,
			"不过滤时全平台视图含 viewer + 两个 probe")
		require.NotNil(t, payload.Data.NoMembershipCount)
		require.Equal(t, int64(2), *payload.Data.NoMembershipCount)
	})

	t.Run("search_narrows_total_but_not_count", func(t *testing.T) {
		w := umScopeDoGet(t, env,
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true&q=probe-1")
		require.Equal(t, http.StatusOK, w.Code)

		var payload t44Payload
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		require.Equal(t, int64(1), payload.Data.Total, "q 缩小列表")
		require.NotNil(t, payload.Data.NoMembershipCount,
			"过滤请求同样带 no_membership_count（目录徽标数据源）")
		require.Equal(t, int64(2), *payload.Data.NoMembershipCount,
			"目录徽标计数不随 q 缩小（与 role_counts 同语义）")
	})
}

// TestT44RouteWithoutMembershipParamGuards 锁定参数守卫与判权走完整链路。
func TestT44RouteWithoutMembershipParamGuards(t *testing.T) {
	t.Run("owner_forbidden", func(t *testing.T) {
		env := newUMScopeRouteEnv(t, umRouteSession{
			role: types.TenantRoleOwner, userID: "u-owner-a",
		})
		env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
			umScopeTenantA, types.TenantRoleOwner)
		w := umScopeDoGet(t, env,
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true")
		require.Equal(t, http.StatusForbidden, w.Code,
			"owner（非系统管理员）请求无空间过滤必须 403")
	})

	for _, scope := range []string{"current", "other"} {
		scope := scope
		t.Run("scope_"+scope+"_rejected", func(t *testing.T) {
			env := newUMScopeRouteEnv(t, umRouteSession{
				role: types.TenantRoleViewer, userID: "u-sysadmin", systemAdmin: true,
			})
			w := umScopeDoGet(t, env,
				"/api/v1/tenants/10/user-management?scope="+scope+"&without_membership=true")
			require.Equal(t, http.StatusBadRequest, w.Code,
				"无空间过滤只在 scope=all 可用")
		})
	}
}
