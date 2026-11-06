package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T16 全平台用户列表（scope 空间筛选 + 多空间聚合）服务层红阶段用例
// （AC-2、AC-26、AC-27、AC-41 + T16 验收 1–9）。
//
// 假定契约（03-tickets/T16.md + 队长派单，全文见 05-tests.md §25.1）：
//   - interfaces 新增 UserManagementListScope（current/other/all 常量）与
//     UserManagementMembership{TenantID,TenantName,Role}；
//   - UserManagementListItem 增加 Memberships 字段（current 视图为空）；
//   - UserManagementService 新增 ListUsersScoped(ctx, tenantID, scope, role,
//     query, page, pageSize)，旧 ListUsers 签名不动（现有调用方零破坏）；
//   - scope=other：本空间之外任一空间有 active 成员关系的用户（可同时是
//     本空间成员），按 user 去重，memberships 只含其它空间的 active 关系；
//     墓碑用户排除；封禁用户照列 status="banned"；
//   - scope=all：全平台全部非墓碑用户（含本空间成员与无成员关系账号），
//     同一 user 一行，memberships 聚合全部 active 关系（含本空间，
//     按 tenant_id 升序）；
//   - role 过滤匹配视图内任意一条成员关系的角色；role_counts 按视图内
//     "有该角色的去重用户数"计且四键始终存在；q 搜索与分页在去重后进行
//     （排序不锁，分页用集合断言）；
//   - 顶层 role 只弱断言为该用户视图内成员关系角色之一，不锁展示优先级；
//   - 非系统管理员请求 other/all → 403（服务层自证，C4）；非法 scope → 400
//     （精确匹配 current/other/all，不做大小写宽容）。
//
// 业务规则只在本文件断言；handler 只验证参数/响应，router 只验证守卫与链路。

const umTenantC uint64 = 30

// umScopeEnv 在 T05 夹具之上补 tenants 表：memberships 的 tenant_name
// 投影需要它。
func umScopeEnv(t *testing.T) *umTestEnv {
	t.Helper()
	env := newUMTestEnv(t)
	require.NoError(t, env.db.AutoMigrate(&types.Tenant{}))
	for _, tn := range []struct {
		id   uint64
		name string
	}{
		{umTenantA, "空间A"},
		{umTenantB, "空间B"},
		{umTenantC, "空间C"},
	} {
		require.NoError(t, env.db.Create(&types.Tenant{
			ID: tn.id, Name: tn.name, Status: "active",
		}).Error)
	}
	return env
}

// umScopeSeedOwnership 给空间 A 种 owner（u-owner-a）：owner 调用者需要
// 真实 owner 成员关系才能通过 requireOwnerOrSystemAdmin 的回查。
func umScopeSeedOwnership(t *testing.T, env *umTestEnv) {
	t.Helper()
	umListSeedMember(t, env, "u-owner-a", "owner-a@example.com", "owner-a",
		umTenantA, types.TenantRoleOwner, true)
}

// umScopeSysAdminCtx 超管调用：工作区角色故意用 viewer，锁定"超管放行
// 与工作区角色无关"的口径；超管身份不需要 users 行。
func umScopeSysAdminCtx(tenantID uint64) context.Context {
	return umCallerCtx("u-sysadmin", tenantID, types.TenantRoleViewer, true)
}

// umScopeOwnerCtx 本空间 owner 调用（配合 umScopeSeedOwnership 使用）。
func umScopeOwnerCtx(tenantID uint64) context.Context {
	return umCallerCtx("u-owner-a", tenantID, types.TenantRoleOwner, false)
}

// umScopeMembershipMap 把 memberships 转成 map，避免锁定内部排序；
// umScopeItemRoles 返回角色集合，用于顶层 role 的弱断言。
func umScopeMembershipMap(item *interfaces.UserManagementListItem) map[uint64]types.TenantRole {
	out := map[uint64]types.TenantRole{}
	if item == nil {
		return out
	}
	for _, m := range item.Memberships {
		out[m.TenantID] = m.Role
	}
	return out
}

func umScopeItemRoles(item *interfaces.UserManagementListItem) map[types.TenantRole]bool {
	out := map[types.TenantRole]bool{}
	if item == nil {
		return out
	}
	for _, m := range item.Memberships {
		out[m.Role] = true
	}
	return out
}

// TestUserManagementListScopedOtherAggregatesMemberships 锁定 T16-3：
// other 视图 = 本空间之外有 active 成员关系的用户，按 user 去重，每项
// memberships 只含其它空间并带 tenant_id/tenant_name/role。
func TestUserManagementListScopedOtherAggregatesMemberships(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	// u-dual：本空间 viewer + 空间 B admin → other 出现，memberships 只有 B。
	umListSeedMember(t, env, "u-dual", "dual@example.com", "dual",
		umTenantA, types.TenantRoleViewer, true)
	umListSeedMembership(t, env, "u-dual", umTenantB,
		types.TenantRoleAdmin, types.TenantMemberStatusActive)
	// u-triple：本空间 contributor + 空间 C viewer → other 出现。
	umListSeedMember(t, env, "u-triple", "triple@example.com", "triple",
		umTenantA, types.TenantRoleContributor, true)
	umListSeedMembership(t, env, "u-triple", umTenantC,
		types.TenantRoleViewer, types.TenantMemberStatusActive)
	umListSeedMember(t, env, "u-owner-b", "owner-b@example.com", "owner-b",
		umTenantB, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-only-b", "only-b@example.com", "only-b",
		umTenantB, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsersScoped(umScopeSysAdminCtx(umTenantA),
		umTenantA, interfaces.UserManagementScopeOther, "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(4), res.Total,
		"other 视图只含本空间之外有 active 成员的用户，按 user 去重")
	require.Equal(t, []string{"u-dual", "u-only-b", "u-owner-b", "u-triple"}, umListIDs(res))
	require.Nil(t, umListItemByID(res, "u-owner-a"),
		"只属于本空间的用户不出现在 other 视图")

	dual := umListItemByID(res, "u-dual")
	require.NotNil(t, dual)
	require.Equal(t, map[uint64]types.TenantRole{
		umTenantB: types.TenantRoleAdmin,
	}, umScopeMembershipMap(dual), "other 视图的 memberships 只含本空间之外的成员关系")
	require.Len(t, dual.Memberships, 1)
	require.Equal(t, "空间B", dual.Memberships[0].TenantName)
	require.Contains(t, umScopeItemRoles(dual), dual.Role,
		"顶层 role 必须来自该用户视图内的成员关系")

	onlyB := umListItemByID(res, "u-only-b")
	require.NotNil(t, onlyB)
	require.Equal(t, "only-b@example.com", onlyB.Email)
	require.Equal(t, "active", onlyB.Status)
	require.True(t, onlyB.IsActive)
	require.False(t, onlyB.CreatedAt.IsZero())

	triple := umListItemByID(res, "u-triple")
	require.NotNil(t, triple)
	require.Equal(t, map[uint64]types.TenantRole{
		umTenantC: types.TenantRoleViewer,
	}, umScopeMembershipMap(triple))
}

// TestUserManagementListScopedOtherExcludesTombstonesAndInactive 锁定
// other 视图的成员口径：墓碑用户排除；其它空间 suspended 不算 active；
// 封禁用户照常出现且 status="banned"。
func TestUserManagementListScopedOtherExcludesTombstonesAndInactive(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	umListSeedMember(t, env, "u-banned", "banned@example.com", "banned",
		umTenantB, types.TenantRoleViewer, false)
	umListSeedMember(t, env, "u-tomb", "tomb@example.com", "tomb",
		umTenantB, types.TenantRoleViewer, true)
	umListTombstoneUser(t, env, "u-tomb")
	// u-susp：本空间 active contributor + 空间 B suspended admin——
	// 没有任何"本空间之外的 active 成员关系"，不出现。
	umSeedUser(t, env, "u-susp", func(u *types.User) {
		u.Email = "susp@example.com"
		u.Username = "susp"
	})
	umListSeedMembership(t, env, "u-susp", umTenantA,
		types.TenantRoleContributor, types.TenantMemberStatusActive)
	umListSeedMembership(t, env, "u-susp", umTenantB,
		types.TenantRoleAdmin, types.TenantMemberStatusSuspended)

	res, err := env.svc.ListUsersScoped(umScopeSysAdminCtx(umTenantA),
		umTenantA, interfaces.UserManagementScopeOther, "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(1), res.Total)
	banned := umListItemByID(res, "u-banned")
	require.NotNil(t, banned, "封禁用户照常出现（封禁只影响 status 展示）")
	require.Equal(t, "banned", banned.Status)
	require.False(t, banned.IsActive)
	require.Nil(t, umListItemByID(res, "u-tomb"), "墓碑用户不进入 other 视图")
	require.Nil(t, umListItemByID(res, "u-susp"),
		"本空间 active + 其它空间 suspended 不构成 other 视图成员")
}

// TestUserManagementListScopedAllIncludesPlatformWide 锁定 T16-4：all 视图
// = 全平台全部非墓碑用户（含无成员关系账号），同一 user 一行，memberships
// 聚合本空间在内的全部 active 关系（tenant_id 升序）。
func TestUserManagementListScopedAllIncludesPlatformWide(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	umListSeedMember(t, env, "u-dual", "dual@example.com", "dual",
		umTenantA, types.TenantRoleViewer, true)
	umListSeedMembership(t, env, "u-dual", umTenantB,
		types.TenantRoleAdmin, types.TenantMemberStatusActive)
	umListSeedMember(t, env, "u-owner-b", "owner-b@example.com", "owner-b",
		umTenantB, types.TenantRoleOwner, true)
	// 无任何成员关系的注册账号也要出现在 all 视图（全平台口径）。
	umSeedUser(t, env, "u-loner", func(u *types.User) {
		u.Email = "loner@example.com"
		u.Username = "loner"
	})
	umListSeedMember(t, env, "u-tomb-all", "tomb-all@example.com", "tomb-all",
		umTenantC, types.TenantRoleViewer, true)
	umListTombstoneUser(t, env, "u-tomb-all")

	res, err := env.svc.ListUsersScoped(umScopeSysAdminCtx(umTenantA),
		umTenantA, interfaces.UserManagementScopeAll, "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(4), res.Total,
		"all 视图 = 全平台非墓碑用户（含无成员关系账号），墓碑排除")
	require.Equal(t, []string{"u-dual", "u-loner", "u-owner-a", "u-owner-b"}, umListIDs(res))

	dual := umListItemByID(res, "u-dual")
	require.NotNil(t, dual)
	require.Equal(t, []interfaces.UserManagementMembership{
		{TenantID: umTenantA, TenantName: "空间A", Role: types.TenantRoleViewer},
		{TenantID: umTenantB, TenantName: "空间B", Role: types.TenantRoleAdmin},
	}, dual.Memberships,
		"all 视图 memberships 聚合本空间在内的全部 active 关系，按 tenant_id 升序")

	loner := umListItemByID(res, "u-loner")
	require.NotNil(t, loner)
	require.Empty(t, loner.Memberships, "无成员关系的账号 memberships 为空")
	require.Nil(t, umListItemByID(res, "u-tomb-all"), "墓碑用户不进入 all 视图")
}

// TestUserManagementListScopedRoleFilterMatchesAnyMembership 锁定 T16-5/7：
// role 过滤匹配"任意一条视图内成员关系的角色"；role_counts 不随过滤缩小。
func TestUserManagementListScopedRoleFilterMatchesAnyMembership(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	umListSeedMember(t, env, "u-dual", "dual@example.com", "dual",
		umTenantA, types.TenantRoleViewer, true)
	umListSeedMembership(t, env, "u-dual", umTenantB,
		types.TenantRoleAdmin, types.TenantMemberStatusActive)
	umListSeedMember(t, env, "u-admin-a", "admin-a@example.com", "admin-a",
		umTenantA, types.TenantRoleAdmin, true)

	sysCtx := umScopeSysAdminCtx(umTenantA)

	all, err := env.svc.ListUsersScoped(sysCtx, umTenantA,
		interfaces.UserManagementScopeAll, types.TenantRoleAdmin, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), all.Total,
		"all+admin 命中任意成员关系是 admin 的去重用户（u-dual 因空间 B 的 admin 命中）")
	require.Equal(t, []string{"u-admin-a", "u-dual"}, umListIDs(all))
	require.Equal(t, map[types.TenantRole]int64{
		types.TenantRoleOwner:       1,
		types.TenantRoleAdmin:       2,
		types.TenantRoleContributor: 0,
		types.TenantRoleViewer:      1,
	}, all.RoleCounts,
		"role_counts 按视图全量去重计、不随 role 过滤缩小；u-dual 的 B admin 计入 admin")

	other, err := env.svc.ListUsersScoped(sysCtx, umTenantA,
		interfaces.UserManagementScopeOther, types.TenantRoleAdmin, "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), other.Total)
	require.Equal(t, []string{"u-dual"}, umListIDs(other),
		"u-admin-a 的 admin 角色在本空间，不算 other 视图的 admin")
}

// TestUserManagementListScopedRoleCountsDedupedByUser 锁定 T16-7：同一用户
// 在多个空间持同一角色只按去重用户数计一次；四个角色键始终存在。
func TestUserManagementListScopedRoleCountsDedupedByUser(t *testing.T) {
	env := umScopeEnv(t)
	umListSeedMember(t, env, "u-multi-admin", "multi-admin@example.com", "multi-admin",
		umTenantB, types.TenantRoleAdmin, true)
	umListSeedMembership(t, env, "u-multi-admin", umTenantC,
		types.TenantRoleAdmin, types.TenantMemberStatusActive)
	umListSeedMember(t, env, "u-owner-b", "owner-b@example.com", "owner-b",
		umTenantB, types.TenantRoleOwner, true)

	for _, scope := range []interfaces.UserManagementListScope{
		interfaces.UserManagementScopeOther,
		interfaces.UserManagementScopeAll,
	} {
		scope := scope
		t.Run(string(scope), func(t *testing.T) {
			res, err := env.svc.ListUsersScoped(umScopeSysAdminCtx(umTenantA),
				umTenantA, scope, "", "", 1, 20)
			require.NoError(t, err)
			require.Equal(t, map[types.TenantRole]int64{
				types.TenantRoleOwner:       1,
				types.TenantRoleAdmin:       1,
				types.TenantRoleContributor: 0,
				types.TenantRoleViewer:      0,
			}, res.RoleCounts,
				"u-multi-admin 在 B/C 两个空间都是 admin，只计 1；四键始终存在")
			require.Equal(t, int64(2), res.Total)
		})
	}
}

// TestUserManagementListScopedSearchAndPagination 锁定 T16-8 / AC-27：q 在
// 去重后过滤（跨空间用户仍是一行）；分页在去重后进行，跨页不重复、并集
// 覆盖全集（排序不锁，用集合断言）。
func TestUserManagementListScopedSearchAndPagination(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	for _, id := range []string{"u-alpha", "u-beta", "u-carol", "u-dave", "u-eric"} {
		umListSeedMember(t, env, id, id+"@example.com", id,
			umTenantB, types.TenantRoleViewer, true)
	}
	// u-beta 在空间 C 还有一层 admin：命中搜索后仍是一行、memberships 聚两条。
	umListSeedMembership(t, env, "u-beta", umTenantC,
		types.TenantRoleAdmin, types.TenantMemberStatusActive)

	sysCtx := umScopeSysAdminCtx(umTenantA)

	t.Run("search_after_dedup", func(t *testing.T) {
		res, err := env.svc.ListUsersScoped(sysCtx, umTenantA,
			interfaces.UserManagementScopeOther, "", "beta", 1, 20)
		require.NoError(t, err)
		require.Equal(t, int64(1), res.Total)
		require.Equal(t, []string{"u-beta"}, umListIDs(res))
		beta := umListItemByID(res, "u-beta")
		require.NotNil(t, beta)
		require.Len(t, beta.Memberships, 2,
			"跨两个空间的用户搜索命中后仍是一行，memberships 聚合两个空间")
	})

	t.Run("search_no_match", func(t *testing.T) {
		res, err := env.svc.ListUsersScoped(sysCtx, umTenantA,
			interfaces.UserManagementScopeOther, "", "zzzzz", 1, 20)
		require.NoError(t, err)
		require.Equal(t, int64(0), res.Total)
		require.Empty(t, res.Items)
	})

	t.Run("pagination_after_dedup", func(t *testing.T) {
		seen := map[string]bool{}
		for page := 1; page <= 4; page++ {
			res, err := env.svc.ListUsersScoped(sysCtx, umTenantA,
				interfaces.UserManagementScopeOther, "", "", page, 2)
			require.NoError(t, err)
			require.Equal(t, int64(5), res.Total)
			switch page {
			case 1, 2:
				require.Len(t, res.Items, 2)
			case 3:
				require.Len(t, res.Items, 1)
			case 4:
				require.Empty(t, res.Items)
			}
			for _, item := range res.Items {
				require.False(t, seen[item.UserID],
					"去重后的分页跨页不得重复: %s", item.UserID)
				seen[item.UserID] = true
			}
		}
		require.Len(t, seen, 5, "各页并集必须覆盖去重后的全部用户")
	})
}

// TestUserManagementListScopedNonSystemAdminForbidden 锁定 T16-6 / C4：
// owner/admin/viewer（非系统管理员）请求 other/all 一律 403，服务层自证；
// scope=current 不收紧 owner 的既有访问。
func TestUserManagementListScopedNonSystemAdminForbidden(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	umListSeedMember(t, env, "u-admin-a", "admin-a@example.com", "admin-a",
		umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umTenantA, types.TenantRoleViewer, true)
	umListSeedMember(t, env, "u-only-b", "only-b@example.com", "only-b",
		umTenantB, types.TenantRoleViewer, true)

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		scope interfaces.UserManagementListScope
	}{
		{"owner_other", umScopeOwnerCtx(umTenantA), interfaces.UserManagementScopeOther},
		{"owner_all", umScopeOwnerCtx(umTenantA), interfaces.UserManagementScopeAll},
		{"admin_other", umCallerCtx("u-admin-a", umTenantA, types.TenantRoleAdmin, false), interfaces.UserManagementScopeOther},
		{"viewer_all", umCallerCtx("u-viewer-a", umTenantA, types.TenantRoleViewer, false), interfaces.UserManagementScopeAll},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.ListUsersScoped(tc.ctx, umTenantA, tc.scope, "", "", 1, 20)
			umRequireHTTPStatus(t, err, 403)
		})
	}

	t.Run("owner_current_still_allowed", func(t *testing.T) {
		res, err := env.svc.ListUsersScoped(umScopeOwnerCtx(umTenantA),
			umTenantA, interfaces.UserManagementScopeCurrent, "", "", 1, 20)
		require.NoError(t, err, "scope=current 不收紧 owner 的既有访问")
		require.Equal(t, int64(3), res.Total, "current 视图仍只看本空间 active 成员")
	})
}

// TestUserManagementListScopedCurrentMatchesLegacy 锁定 T16-2/9：current
// 视图与旧 ListUsers 完全一致（服务层零破坏面）。
func TestUserManagementListScopedCurrentMatchesLegacy(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	umListSeedMember(t, env, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umTenantA, types.TenantRoleViewer, true)
	umListSeedMember(t, env, "u-banned-a", "banned-a@example.com", "banned-a",
		umTenantA, types.TenantRoleContributor, false)
	umListSeedMember(t, env, "u-only-b", "only-b@example.com", "only-b",
		umTenantB, types.TenantRoleViewer, true)

	ctx := umScopeOwnerCtx(umTenantA)
	legacy, err := env.svc.ListUsers(ctx, umTenantA, "", "", 1, 20)
	require.NoError(t, err)
	scoped, err := env.svc.ListUsersScoped(ctx, umTenantA,
		interfaces.UserManagementScopeCurrent, "", "", 1, 20)
	require.NoError(t, err)

	require.Equal(t, legacy.Total, scoped.Total)
	require.Equal(t, umListIDs(legacy), umListIDs(scoped),
		"current 视图的成员集合必须与旧 ListUsers 一致（本空间成员，不含空间 B）")
	require.Equal(t, legacy.RoleCounts, scoped.RoleCounts)
	for i := range scoped.Items {
		require.Empty(t, scoped.Items[i].Memberships, "current 视图不聚合 memberships")
	}
}

// TestUserManagementListScopedInvalidScopeRejected 锁定 scope 精确匹配
// current/other/all（不做大小写宽容），非法值 400。
func TestUserManagementListScopedInvalidScopeRejected(t *testing.T) {
	env := umScopeEnv(t)
	umScopeSeedOwnership(t, env)
	for _, scope := range []interfaces.UserManagementListScope{"galaxy", "everyone", "CURRENT"} {
		scope := scope
		t.Run(string(scope), func(t *testing.T) {
			_, err := env.svc.ListUsersScoped(umScopeOwnerCtx(umTenantA),
				umTenantA, scope, "", "", 1, 20)
			umRequireHTTPStatus(t, err, 400)
		})
	}
}
