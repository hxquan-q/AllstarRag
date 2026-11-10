package service

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T05 用户管理列表服务层红阶段用例（AC-2、AC-26、AC-27、AC-32、AC-41）。
//
// 假定契约（03-tickets/T05.md + 02-plan §3.1 + 队长派单）：
//   - interfaces.UserManagementService.ListUsers(ctx, tenantID, role, query,
//     page, pageSize) 返回 *interfaces.UserManagementListResult{Items, Total,
//     RoleCounts}；
//   - role 为空 = 全部；非法 role → 400；
//   - items 只含 active 成员关系；墓碑用户（users.deleted_at 非空）不出现；
//     被封禁用户出现且 status="banned"，否则 "active"；
//   - role_counts 覆盖 viewer/contributor/admin/owner；墓碑不计、封禁计入；
//   - q 按邮箱/用户名模糊搜索；分页与现有列表同口径（默认 20、上限 100）；
//   - 非 owner/超管调用 → 403；超管无需本空间 owner 角色。
//
// 业务规则只在本文件断言；handler 只验证参数/响应/错误映射，router 只验证
// 路由注册与守卫。

// umListSeedMember 种一个用户 + 一条 active 成员关系。
func umListSeedMember(
	t *testing.T,
	env *umTestEnv,
	userID, email, username string,
	tenantID uint64,
	role types.TenantRole,
	active bool,
) {
	t.Helper()
	umSeedUser(t, env, userID, func(u *types.User) {
		u.Email = email
		u.Username = username
		u.IsActive = active
	})
	umListSeedMembership(t, env, userID, tenantID, role, types.TenantMemberStatusActive)
}

// umListSeedMembership 种一条指定状态的成员关系（用于 suspended 等边界）。
func umListSeedMembership(
	t *testing.T,
	env *umTestEnv,
	userID string,
	tenantID uint64,
	role types.TenantRole,
	status types.TenantMemberStatus,
) {
	t.Helper()
	require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   status,
		JoinedAt: time.Now(),
	}))
}

// umListTombstoneUser 把用户软删成墓碑行（users.deleted_at 非空），
// 模拟 T03 PurgeUser 之后的状态；成员关系保持 active。
func umListTombstoneUser(t *testing.T, env *umTestEnv, userID string) {
	t.Helper()
	require.NoError(t, env.db.Exec(
		"UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), userID).Error)
}

func umListItemByID(
	res *interfaces.UserManagementListResult, userID string,
) *interfaces.UserManagementListItem {
	if res == nil {
		return nil
	}
	for i := range res.Items {
		if res.Items[i].UserID == userID {
			return &res.Items[i]
		}
	}
	return nil
}

func umListIDs(res *interfaces.UserManagementListResult) []string {
	ids := make([]string, 0, len(res.Items))
	for _, item := range res.Items {
		ids = append(ids, item.UserID)
	}
	sort.Strings(ids)
	return ids
}

// TestUserManagementListUsersRoleCountsIncludeBannedExcludeTombstones 锁定
// AC-2/AC-41 的左侧目录计数口径：owner/admin/viewer 三桶；封禁用户计入；墓碑用户
// 与非 active 成员关系都不计入，也不出现在列表。
func TestUserManagementListUsersRoleCountsIncludeBannedExcludeTombstones(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-contrib", "contrib@example.com", "contrib", umTenantA, types.TenantRoleContributor, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	umListSeedMember(t, env, "u-banned", "banned@example.com", "banned", umTenantA, types.TenantRoleViewer, false)

	// 墓碑用户的成员关系仍 active，但 users.deleted_at 非空 → 不出现、不计数。
	umListSeedMember(t, env, "u-tomb", "tomb@example.com", "tomb", umTenantA, types.TenantRoleViewer, true)
	umListTombstoneUser(t, env, "u-tomb")

	// suspended 成员关系不是 active → 不出现、不计数。
	umSeedUser(t, env, "u-susp", func(u *types.User) {
		u.Email = "susp@example.com"
		u.Username = "susp"
	})
	umListSeedMembership(t, env, "u-susp", umTenantA, types.TenantRoleAdmin, types.TenantMemberStatusSuspended)

	res, err := env.svc.ListUsers(
		umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "", "", 1, 20,
	)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Equal(t, int64(5), res.Total,
		"only active memberships of non-tombstoned users count toward total")
	require.Len(t, res.Items, 5)
	// 历史 contributor 成员行仍在列表里（枚举保留），但目录只统计三桶。
	require.Equal(t, map[types.TenantRole]int64{
		types.TenantRoleOwner:  1,
		types.TenantRoleAdmin:  1,
		types.TenantRoleViewer: 2,
	}, res.RoleCounts,
		"role_counts must only expose owner/admin/viewer buckets, include banned, exclude tombstones")

	require.Nil(t, umListItemByID(res, "u-tomb"), "tombstoned user must not be listed")
	require.Nil(t, umListItemByID(res, "u-susp"), "non-active membership must not be listed")

	banned := umListItemByID(res, "u-banned")
	require.NotNil(t, banned)
	require.Equal(t, "banned", banned.Status)
	require.False(t, banned.IsActive)
	require.Equal(t, types.TenantRoleViewer, banned.Role)

	active := umListItemByID(res, "u-viewer")
	require.NotNil(t, active)
	require.Equal(t, "active", active.Status)
	require.True(t, active.IsActive)
}

// TestUserManagementListUsersFiltersByRole 锁定 AC-2/AC-27：按角色过滤，
// 空 role = 全部；role_counts 始终是整空间目录计数，不随过滤缩小。
func TestUserManagementListUsersFiltersByRole(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-contrib", "contrib@example.com", "contrib", umTenantA, types.TenantRoleContributor, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)

	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)
	fullCounts := map[types.TenantRole]int64{
		types.TenantRoleOwner:  1,
		types.TenantRoleAdmin:  1,
		types.TenantRoleViewer: 1,
	}

	for _, role := range []types.TenantRole{
		types.TenantRoleViewer,
		types.TenantRoleContributor,
		types.TenantRoleAdmin,
		types.TenantRoleOwner,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			res, err := env.svc.ListUsers(ctx, umTenantA, role, "", 1, 20)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Equal(t, int64(1), res.Total)
			require.Len(t, res.Items, 1)
			require.Equal(t, role, res.Items[0].Role)
			require.Equal(t, fullCounts, res.RoleCounts,
				"directory counts must stay tenant-wide regardless of the role filter")
		})
	}

	t.Run("empty_means_all", func(t *testing.T) {
		res, err := env.svc.ListUsers(ctx, umTenantA, "", "", 1, 20)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, int64(4), res.Total)
		require.Len(t, res.Items, 4)
	})
}

// TestUserManagementListUsersRejectsInvalidRole 锁定只要 role 非空就必须是
// viewer/contributor/admin/owner 之一，否则 400。
func TestUserManagementListUsersRejectsInvalidRole(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)

	for _, role := range []types.TenantRole{"superuser", "member", "system_admin"} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			_, err := env.svc.ListUsers(ctx, umTenantA, role, "", 1, 20)
			umRequireHTTPStatus(t, err, 400)
		})
	}
}

// TestUserManagementListUsersSearchByEmailAndUsername 锁定 AC-27：q 同时
// 匹配邮箱与用户名，大小写不敏感，total 随搜索条件变化。
func TestUserManagementListUsersSearchByEmailAndUsername(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-alice", "alice@example.com", "alice", umTenantA, types.TenantRoleViewer, true)
	umListSeedMember(t, env, "u-bob", "bob@example.com", "builder", umTenantA, types.TenantRoleContributor, true)
	umListSeedMember(t, env, "u-carol", "carol@example.com", "AliceFan", umTenantA, types.TenantRoleAdmin, true)

	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"email_or_username", "alice", []string{"u-alice", "u-carol"}},
		{"case_insensitive", "ALICE", []string{"u-alice", "u-carol"}},
		{"email_substring", "bob@example", []string{"u-bob"}},
		{"username_substring", "build", []string{"u-bob"}},
		{"no_match", "zzzzz", []string{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			res, err := env.svc.ListUsers(ctx, umTenantA, "", tc.query, 1, 20)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Equal(t, int64(len(tc.want)), res.Total,
				"total must reflect the search filter")
			require.Equal(t, tc.want, umListIDs(res))
		})
	}
}

// TestUserManagementListUsersPaginationAndTotal 锁定 AC-27：page/page_size
// 与 total；跨页不重复；默认页大小 20；page < 1 回落第 1 页。
func TestUserManagementListUsersPaginationAndTotal(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("u-v%02d", i)
		umListSeedMember(t, env, id, id+"@example.com", id, umTenantA, types.TenantRoleViewer, true)
	}

	ctx := umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false)
	page1, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleViewer, "", 1, 10)
	require.NoError(t, err)
	require.NotNil(t, page1)
	require.Equal(t, int64(25), page1.Total)
	require.Len(t, page1.Items, 10)

	page2, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleViewer, "", 2, 10)
	require.NoError(t, err)
	require.Equal(t, int64(25), page2.Total)
	require.Len(t, page2.Items, 10)

	seen := map[string]bool{}
	for _, id := range umListIDs(page1) {
		seen[id] = true
	}
	for _, id := range umListIDs(page2) {
		require.False(t, seen[id], "page 1 and page 2 must not overlap")
	}

	page3, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleViewer, "", 3, 10)
	require.NoError(t, err)
	require.Equal(t, int64(25), page3.Total)
	require.Len(t, page3.Items, 5)

	page4, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleViewer, "", 4, 10)
	require.NoError(t, err)
	require.Equal(t, int64(25), page4.Total)
	require.Empty(t, page4.Items)

	defaults, err := env.svc.ListUsers(ctx, umTenantA, types.TenantRoleViewer, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(25), defaults.Total)
	require.Len(t, defaults.Items, 20,
		"page < 1 and page_size < 1 must fall back to page 1 / default size 20")
}

// TestUserManagementListUsersTenantIsolation 锁定 T05-5：列表只返回目标
// 空间的成员，不串租户。
func TestUserManagementListUsersTenantIsolation(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner-a", "owner-a@example.com", "owner-a", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-a", "a@example.com", "a", umTenantA, types.TenantRoleViewer, true)
	umListSeedMember(t, env, "u-owner-b", "owner-b@example.com", "owner-b", umTenantB, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-b", "b@example.com", "b", umTenantB, types.TenantRoleViewer, true)

	resA, err := env.svc.ListUsers(
		umCallerCtx("u-owner-a", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "", "", 1, 20,
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), resA.Total)
	require.Equal(t, []string{"u-a", "u-owner-a"}, umListIDs(resA))
	require.Nil(t, umListItemByID(resA, "u-b"), "tenant A list must not include tenant B members")

	resB, err := env.svc.ListUsers(
		umCallerCtx("u-owner-b", umTenantB, types.TenantRoleOwner, false),
		umTenantB, "", "", 1, 20,
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), resB.Total)
	require.Equal(t, []string{"u-b", "u-owner-b"}, umListIDs(resB))
	require.Nil(t, umListItemByID(resB, "u-a"), "tenant B list must not include tenant A members")
}

// TestUserManagementListUsersNonOwnerRejected 锁定 AC-38/C4：服务层自证防御，
// admin/contributor/viewer 一律 403（不能只靠前端隐藏）。
func TestUserManagementListUsersNonOwnerRejected(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-target", "target@example.com", "target", umTenantA, types.TenantRoleViewer, true)

	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			_, err := env.svc.ListUsers(
				umCallerCtx("u-"+string(role), umTenantA, role, false),
				umTenantA, "", "", 1, 20,
			)
			umRequireHTTPStatus(t, err, 403)
		})
	}
}

// TestUserManagementListUsersSystemAdminBypassesOwnerRequirement 锁定 T05-1：
// 系统管理员不需要在本空间持有 owner 角色。
func TestUserManagementListUsersSystemAdminBypassesOwnerRequirement(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)

	res, err := env.svc.ListUsers(
		umCallerCtx("u-sysadmin", umTenantA, types.TenantRoleViewer, true),
		umTenantA, "", "", 1, 20,
	)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, int64(2), res.Total)
	require.NotNil(t, umListItemByID(res, "u-owner"),
		"system admin must be able to see owner rows even with a viewer workspace role")
}

// TestUserManagementListUsersItemStatusAndCreatedAt 锁定列表项字段：
// user_id/email/username/role/status/is_active/created_at。
func TestUserManagementListUsersItemStatusAndCreatedAt(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-active", "active@example.com", "active-user", umTenantA, types.TenantRoleContributor, true)
	umListSeedMember(t, env, "u-banned", "banned@example.com", "banned-user", umTenantA, types.TenantRoleAdmin, false)

	res, err := env.svc.ListUsers(
		umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "", "", 1, 20,
	)
	require.NoError(t, err)

	active := umListItemByID(res, "u-active")
	require.NotNil(t, active)
	require.Equal(t, "active@example.com", active.Email)
	require.Equal(t, "active-user", active.Username)
	require.Equal(t, types.TenantRoleContributor, active.Role)
	require.Equal(t, "active", active.Status)
	require.True(t, active.IsActive)

	seeded := umLoadUser(t, env, "u-active")
	require.False(t, active.CreatedAt.IsZero(), "created_at must be populated")
	require.WithinDuration(t, seeded.CreatedAt, active.CreatedAt, time.Second)

	banned := umListItemByID(res, "u-banned")
	require.NotNil(t, banned)
	require.Equal(t, "banned@example.com", banned.Email)
	require.Equal(t, "banned", banned.Status)
	require.False(t, banned.IsActive)
}
