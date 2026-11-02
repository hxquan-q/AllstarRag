package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// CP3 F3 复验：UpdateUserPreferences 对 BoundAgentIDsByTenant 逐键合并。
// handler 只传当前活动工作区一个键（空数组 = 删除该键）；service 必须基于
// 重读到的最新 user 行合并——其他工作区的既有键保留，不再被整表覆盖。

func TestUpdateUserPreferencesMergesOnlyPatchedTenantKey(t *testing.T) {
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"alice": {ID: "alice", Preferences: types.UserPreferences{
			BoundAgentIDsByTenant: map[string][]string{"7": {"a"}, "8": {"b"}},
		}},
	}}
	users := &userService{userRepo: repo}

	prefs, err := users.UpdateUserPreferences(t.Context(), "alice", types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {"c"}},
	})

	require.NoError(t, err)
	// 工作区 7 被覆盖；工作区 8 的既有键必须保留（AC-1 跨工作区不互相覆盖）。
	require.Equal(t, map[string][]string{"7": {"c"}, "8": {"b"}},
		prefs.BoundAgentIDsByTenant)
	require.Equal(t, map[string][]string{"7": {"c"}, "8": {"b"}},
		repo.users["alice"].Preferences.BoundAgentIDsByTenant)
}

func TestUpdateUserPreferencesEmptyArrayDeletesOnlyThatTenantKey(t *testing.T) {
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"alice": {ID: "alice", Preferences: types.UserPreferences{
			BoundAgentIDsByTenant: map[string][]string{"7": {"a"}, "8": {"b"}},
		}},
	}}
	users := &userService{userRepo: repo}

	prefs, err := users.UpdateUserPreferences(t.Context(), "alice", types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {}},
	})

	require.NoError(t, err)
	// 空数组 = 删除该工作区键：结果里没有 "7"，也不留空数组；其他工作区不动。
	require.Equal(t, map[string][]string{"8": {"b"}},
		prefs.BoundAgentIDsByTenant)
	require.Equal(t, map[string][]string{"8": {"b"}},
		repo.users["alice"].Preferences.BoundAgentIDsByTenant)
}

func TestUpdateUserPreferencesBindingPatchOnMissingMap(t *testing.T) {
	// 用户从未存过绑定时，单键 patch 直接建立该键。
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"alice": {ID: "alice"},
	}}
	users := &userService{userRepo: repo}

	prefs, err := users.UpdateUserPreferences(t.Context(), "alice", types.UserPreferences{
		BoundAgentIDsByTenant: map[string][]string{"7": {"c"}},
	})

	require.NoError(t, err)
	require.Equal(t, map[string][]string{"7": {"c"}}, prefs.BoundAgentIDsByTenant)
}
