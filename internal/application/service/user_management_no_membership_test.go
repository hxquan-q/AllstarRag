package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T44 服务层红阶段用例：「无空间」目录列表 + no_membership_count 计数。
//
// 契约（03-tickets/T44.md + 队长派单，05-tests §79）：
//   - 新增服务方法（不改既有签名，零破坏）：
//     ListUsersWithoutMembership(ctx, tenantID, query, page, pageSize)
//     (*interfaces.UserManagementListResult, error)；
//   - 仅系统管理员可调（服务层自证，非系统管理员 403）；
//   - items 只含「无任何 active 成员关系」的非墓碑普通账号（排除
//     is_system_admin / can_access_all_tenants）；memberships 为空数组
//     （非 nil）；封禁账号保留并标 status="banned"；
//   - total 与列表同口径；RoleCounts 仍为四键目录计数（不随过滤缩小）；
//   - NoMembershipCount = 无空间账号数（不受 q 影响，与 role_counts 的
//     「目录徽标计数」语义一致）；
//   - 既有 ListUsersScoped / ListUsersScopedWithRoles 在 scope=all 时同样
//     填 NoMembershipCount（目录徽标在任意目录选中时都要显示），
//     scope=other/current 保持 0。

// t44SeedNoMembershipFixture 种一套服务层夹具（复用 um* 助手）。
// 命中集合：u-loner / u-suspended / u-banned-loner；
// 排除集合：u-member（active 关系）、u-tombstone（墓碑）、
// u-sysadmin、u-all-tenants（特权账号走超管目录）。
func t44SeedNoMembershipFixture(t *testing.T, env *umTestEnv) {
	t.Helper()
	umListSeedMember(t, env, "u-member", "member@example.com", "member",
		umTenantA, types.TenantRoleViewer, true)

	umSeedUser(t, env, "u-loner", func(u *types.User) {
		u.Email = "loner@example.com"
		u.Username = "loner"
	})

	umSeedUser(t, env, "u-suspended", func(u *types.User) {
		u.Email = "suspended@example.com"
		u.Username = "suspended"
	})
	umListSeedMembership(t, env, "u-suspended", umTenantA,
		types.TenantRoleViewer, types.TenantMemberStatusSuspended)

	umSeedUser(t, env, "u-tombstone", func(u *types.User) {
		u.Email = "tombstone@example.com"
		u.Username = "tombstone"
	})
	umListTombstoneUser(t, env, "u-tombstone")

	umSeedUser(t, env, "u-sysadmin", func(u *types.User) {
		u.Email = "sysadmin@example.com"
		u.Username = "sysadmin"
		u.IsSystemAdmin = true
	})

	umSeedUser(t, env, "u-all-tenants", func(u *types.User) {
		u.Email = "alltenants@example.com"
		u.Username = "alltenants"
		u.CanAccessAllTenants = true
	})

	umSeedUser(t, env, "u-banned-loner", func(u *types.User) {
		u.Email = "bannedloner@example.com"
		u.Username = "bannedloner"
		u.IsActive = false
	})
}

// TestT44ListUsersWithoutMembershipReturnsMembershiplessAccounts 锁定列表
// 口径：命中集合、memberships 空数组、封禁状态如实、计数一致。
func TestT44ListUsersWithoutMembershipReturnsMembershiplessAccounts(t *testing.T) {
	env := newUMTestEnv(t)
	t44SeedNoMembershipFixture(t, env)
	ctx := umCallerCtx("u-sysadmin", umTenantA, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsersWithoutMembership(ctx, umTenantA, "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(3), res.Total)
	require.ElementsMatch(t,
		[]string{"u-loner", "u-suspended", "u-banned-loner"}, umListIDs(res))

	for _, id := range []string{"u-loner", "u-suspended", "u-banned-loner"} {
		item := umListItemByID(res, id)
		require.NotNil(t, item, "%s 必须出现", id)
		require.NotNil(t, item.Memberships, "%s 的 memberships 必须是空数组而不是 nil", id)
		require.Empty(t, item.Memberships)
		require.Empty(t, item.Role, "无空间账号顶层角色为空")
	}

	banned := umListItemByID(res, "u-banned-loner")
	require.Equal(t, "banned", banned.Status, "封禁的无空间账号保留且状态如实")
	require.False(t, banned.IsActive)
	require.Equal(t, int64(3), res.NoMembershipCount,
		"no_membership_count 与无搜索时的过滤 total 一致")
}

// TestT44ListUsersWithoutMembershipSearchAndCountsNotShrunkByQ 锁定 q 搜索
// 缩小列表与 total，但 NoMembershipCount（目录徽标）不随 q 缩小——与
// role_counts「目录计数不随过滤缩小」同语义。
func TestT44ListUsersWithoutMembershipSearchAndCountsNotShrunkByQ(t *testing.T) {
	env := newUMTestEnv(t)
	t44SeedNoMembershipFixture(t, env)
	ctx := umCallerCtx("u-sysadmin", umTenantA, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsersWithoutMembership(ctx, umTenantA, "loner", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Total, "q 缩小列表 total")
	require.ElementsMatch(t,
		[]string{"u-loner", "u-banned-loner"}, umListIDs(res),
		"q=loner 同时命中邮箱 loner@ 与 bannedloner@（模糊匹配）")
	require.Equal(t, int64(3), res.NoMembershipCount,
		"目录徽标计数不受 q 影响")
}

// TestT44ListUsersWithoutMembershipNonSystemAdminForbidden 锁定服务层判权：
// 非 system_admin 调用 → 403（C4：前端隐藏不等于权限隔离）。
func TestT44ListUsersWithoutMembershipNonSystemAdminForbidden(t *testing.T) {
	env := newUMTestEnv(t)
	t44SeedNoMembershipFixture(t, env)
	ctx := umCallerCtx("u-member", umTenantA, types.TenantRoleOwner, false)

	_, err := env.svc.ListUsersWithoutMembership(ctx, umTenantA, "", 1, 20)
	umRequireHTTPStatus(t, err, 403)
}

// TestT44ScopedAllListExposesNoMembershipCount 锁定既有 scope=all 列表
// 响应带无空间计数：单角色与多角色两条路径都要填，other 视图为 0。
func TestT44ScopedAllListExposesNoMembershipCount(t *testing.T) {
	env := newUMTestEnv(t)
	t44SeedNoMembershipFixture(t, env)
	ctx := umCallerCtx("u-sysadmin", umTenantA, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsersScoped(
		ctx, umTenantA, interfaces.UserManagementScopeAll, "", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(3), res.NoMembershipCount,
		"scope=all 单角色路径必须填 NoMembershipCount（目录徽标数据源）")

	resMulti, err := env.svc.ListUsersScopedWithRoles(
		ctx, umTenantA, interfaces.UserManagementScopeAll,
		[]types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin}, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(3), resMulti.NoMembershipCount,
		"scope=all 多角色路径同样必须填 NoMembershipCount")

	resOther, err := env.svc.ListUsersScoped(
		ctx, umTenantA, interfaces.UserManagementScopeOther, "", "", 1, 20)
	require.NoError(t, err)
	require.Zero(t, resOther.NoMembershipCount,
		"other 视图不渲染无空间目录，计数保持 0（handler 不下发该键）")
}
