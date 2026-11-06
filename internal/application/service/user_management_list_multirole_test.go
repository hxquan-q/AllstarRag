package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T22 用户列表多角色过滤服务层红阶段用例（AC-1/AC-2/AC-3）。
//
// 假定契约（03-tickets/T22.md + 02-plan §13）：
//   - 新增接口方法，不改旧 ListUsers 签名（零破坏）：
//     ListUsersWithRoles(ctx, tenantID, roles []types.TenantRole, query,
//     page, pageSize) (*UserManagementListResult, error)
//     ListUsersScopedWithRoles(ctx, tenantID, scope, roles []types.TenantRole,
//     query, page, pageSize)
//   - roles 为空 = 全部；任一角色非法（含空段/带空格/大小写不符）→ 400；
//   - 多值过滤 = 命中任意一个角色；
//   - total/分页按去重用户计算；role_counts 不随过滤缩小；
//   - scope=other/all 仍只允许系统管理员。

func umRequireMultiRoleStatus(t *testing.T, err error, status int) {
	t.Helper()
	umRequireHTTPStatus(t, err, status)
}

func TestUserManagementListUsersWithRolesFiltersAnyRole(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-contrib", "contrib@example.com", "contrib", umTenantA, types.TenantRoleContributor, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)

	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)
	res, err := env.svc.ListUsersWithRoles(
		ctx, umTenantA,
		[]types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin},
		"", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(2), res.Total)
	require.ElementsMatch(t,
		[]string{"u-admin", "u-contrib"}, umListIDs(res))
	require.Equal(t, map[types.TenantRole]int64{
		types.TenantRoleOwner:       1,
		types.TenantRoleAdmin:       1,
		types.TenantRoleContributor: 1,
		types.TenantRoleViewer:      1,
	}, res.RoleCounts,
		"role_counts 是整空间目录计数，不随多角色过滤缩小")
}

func TestUserManagementListUsersWithRolesEmptyMeansAll(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)

	res, err := env.svc.ListUsersWithRoles(ctx, umTenantA, nil, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Total)
}

// 单值必须与旧 ListUsers 完全等价（零破坏）。
func TestUserManagementListUsersWithRolesSingleValueMatchesLegacy(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)

	legacy, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleAdmin, "", 1, 20)
	require.NoError(t, err)
	multi, err := env.svc.ListUsersWithRoles(
		ctx, umTenantA, []types.TenantRole{types.TenantRoleAdmin}, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, legacy.Total, multi.Total)
	require.Equal(t, umListIDs(legacy), umListIDs(multi))
	require.Equal(t, legacy.RoleCounts, multi.RoleCounts)
}

func TestUserManagementListUsersWithRolesRejectsInvalidValues(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)

	cases := map[string][]types.TenantRole{
		"unknown":          {types.TenantRole("superuser")},
		"uppercase":        {types.TenantRole("Admin")},
		"empty_segment":    {types.TenantRoleContributor, types.TenantRole("")},
		"padded":           {types.TenantRole("viewer ")},
		"valid_then_bad":   {types.TenantRoleViewer, types.TenantRole("member")},
		"empty_then_valid": {types.TenantRole(""), types.TenantRoleAdmin},
	}
	for name, roles := range cases {
		roles := roles
		t.Run(name, func(t *testing.T) {
			_, err := env.svc.ListUsersWithRoles(ctx, umTenantA, roles, "", 1, 20)
			umRequireMultiRoleStatus(t, err, 400)
		})
	}
}

// scope=all：用户只要在任意一个成员关系里命中过滤角色就出现。
func TestUserManagementListScopedWithRolesMatchesAnyMembership(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-cross", "cross@example.com", "cross", umTenantA, types.TenantRoleContributor, true)
	umListSeedMembership(t, env, "u-cross", umTenantB, types.TenantRoleAdmin, types.TenantMemberStatusActive)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	umSeedUser(t, env, "u-sys-admin", func(u *types.User) { u.IsSystemAdmin = true })
	ctx := umCallerCtx("u-sys-admin", umTenantA, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsersScopedWithRoles(
		ctx, umTenantA, interfaces.UserManagementScopeAll,
		[]types.TenantRole{types.TenantRoleAdmin}, "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, umListIDs(res), "u-cross",
		"scope=all 时用户在任意空间的 admin 关系都应命中")
	require.NotContains(t, umListIDs(res), "u-viewer")

	res, err = env.svc.ListUsersScopedWithRoles(
		ctx, umTenantA, interfaces.UserManagementScopeOther,
		[]types.TenantRole{types.TenantRoleAdmin}, "", 1, 20)
	require.NoError(t, err)
	require.Contains(t, umListIDs(res), "u-cross")
}

// 分页按去重用户计算：同一用户同时有 contributor+admin 两个关系时，
// [contributor,admin] 过滤下只出现一次，跨页不重复不遗漏。
func TestUserManagementListScopedWithRolesPaginationDeduplicatesUsers(t *testing.T) {
	env := newUMTestEnv(t)
	for _, id := range []string{"u-both-1", "u-both-2", "u-both-3"} {
		umListSeedMember(t, env, id, id+"@example.com", id, umTenantA, types.TenantRoleContributor, true)
		umListSeedMembership(t, env, id, umTenantA, types.TenantRoleAdmin, types.TenantMemberStatusActive)
	}
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	umSeedUser(t, env, "u-sys-admin", func(u *types.User) { u.IsSystemAdmin = true })
	ctx := umCallerCtx("u-sys-admin", umTenantA, types.TenantRoleViewer, true)

	roles := []types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin}
	first, err := env.svc.ListUsersScopedWithRoles(
		ctx, umTenantA, interfaces.UserManagementScopeCurrent, roles, "", 1, 2)
	require.NoError(t, err)
	second, err := env.svc.ListUsersScopedWithRoles(
		ctx, umTenantA, interfaces.UserManagementScopeCurrent, roles, "", 2, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), first.Total, "total 按去重用户计")
	require.Len(t, first.Items, 2)
	require.Len(t, second.Items, 1)
	require.Empty(t, intersectionIDs(umListIDs(first), umListIDs(second)),
		"跨页不得重复")
	all := append(umListIDs(first), umListIDs(second)...)
	require.ElementsMatch(t, []string{"u-both-1", "u-both-2", "u-both-3"}, all)
}

func TestUserManagementListScopedWithRolesNonSystemAdminForbidden(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)

	for _, scope := range []interfaces.UserManagementListScope{
		interfaces.UserManagementScopeOther,
		interfaces.UserManagementScopeAll,
	} {
		scope := scope
		t.Run(string(scope), func(t *testing.T) {
			_, err := env.svc.ListUsersScopedWithRoles(
				ctx, umTenantA, scope,
				[]types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin},
				"", 1, 20)
			umRequireMultiRoleStatus(t, err, 403)
		})
	}
}

func intersectionIDs(a, b []string) []string {
	set := make(map[string]struct{}, len(a))
	for _, id := range a {
		set[id] = struct{}{}
	}
	var out []string
	for _, id := range b {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
