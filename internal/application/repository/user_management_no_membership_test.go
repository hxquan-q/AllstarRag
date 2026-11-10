package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T44 仓储层红阶段用例：「无空间」目录的列表与计数（Codex 复核 ①）。
//
// 契约（03-tickets/T44.md + 队长派单）：
//   - ListUserManagementUsersWithoutMembership(ctx, search, offset, limit)
//     返回「无任何 active 成员关系」的非墓碑用户分页行，排除
//     is_system_admin / can_access_all_tenants 账号（超管有自己的目录）；
//     封禁（is_active=false）账号保留，供超管执行平台操作；
//   - CountUserManagementUsersWithoutMembership(ctx, search) 同口径计数；
//   - search 按邮箱/用户名模糊匹配（大小写不敏感，LIKE ESCAPE），与既有
//     scoped 列表同口径；
//   - 排序 users.created_at ASC, users.id ASC，offset/limit 分页与
//     ListUserManagementScopedPaged 同构；
//   - 两个方法挂在具体类型 *tenantMemberRepository 上（与 scoped 列表
//     同约定：不扩宽 interfaces.TenantMemberRepository），测试直接构造
//     具体结构体调用；service 侧用窄接口自动探测。

// t44TestDB 建一张含 users / tenants / tenant_members 的 SQLite 库。
// 不复用 tenant_test.go 的 setupTestDB（那张表集合没有 users）。
func t44TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "t44_no_membership.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.Tenant{}, &types.TenantMember{}))
	return db
}

// t44SeedUser 建一个用户行（无成员关系）；created 控制排序位次。
// is_active=false 必须绕过 GORM default:true：先建再 UPDATE。
func t44SeedUser(
	t *testing.T, db *gorm.DB, id, email, username string, created time.Time, active bool,
) {
	t.Helper()
	require.NoError(t, db.Create(&types.User{
		ID:           id,
		Username:     username,
		Email:        email,
		PasswordHash: "hash-" + id,
		IsActive:     true,
		CreatedAt:    created,
	}).Error)
	if !active {
		require.NoError(t, db.Exec("UPDATE users SET is_active = false WHERE id = ?", id).Error)
	}
}

// t44BaseTime 给种子行一个共同的基准时间；各用户按分钟递增控制顺序。
var t44BaseTime = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

// t44SeedStandardFixture 种一套标准夹具并返回期望出现在「无空间」目录的
// 用户（按 created_at 升序）。租户 10 为本空间。
//
//	期望命中（4 人）：u-loner / u-suspended-member / u-invited-only /
//	                 u-banned-loner
//	期望排除（4 人）：u-member（active 关系）、u-tombstone（墓碑）、
//	                 u-sysadmin（is_system_admin）、u-all-tenants
//	                 （can_access_all_tenants）
func t44SeedStandardFixture(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	require.NoError(t, db.Create(&types.Tenant{ID: 10, Name: "空间A", Status: "active"}).Error)

	min := func(m int) time.Time { return t44BaseTime.Add(time.Duration(m) * time.Minute) }

	// 有 active 成员关系 → 排除
	t44SeedUser(t, db, "u-member", "member@example.com", "member", min(1), true)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "u-member", TenantID: 10,
		Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: min(1),
	}).Error)

	// 无任何关系 → 命中（第 1 个）
	t44SeedUser(t, db, "u-loner", "loner@example.com", "loner", min(2), true)

	// 只有 suspended 关系（非 active）→ 命中（第 2 个）
	t44SeedUser(t, db, "u-suspended-member", "suspended@example.com", "suspended", min(3), true)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "u-suspended-member", TenantID: 10,
		Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended, JoinedAt: min(3),
	}).Error)

	// 只有 invited 关系（邀请已发、账号未入组）→ 命中：invited 不算
	// active 成员关系，该账号当前确实不属于任何空间，超管应能看到并处置。
	t44SeedUser(t, db, "u-invited-only", "invited@example.com", "invited", min(4), true)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: "u-invited-only", TenantID: 10,
		Role: types.TenantRoleViewer, Status: types.TenantMemberStatusInvited, JoinedAt: min(4),
	}).Error)

	// 墓碑用户（无关系，deleted_at 非空）→ 排除
	t44SeedUser(t, db, "u-tombstone", "tombstone@example.com", "tombstone", min(5), true)
	require.NoError(t, db.Exec(
		"UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), "u-tombstone").Error)

	// 系统管理员（无关系）→ 排除（有 system_admin 目录）
	t44SeedUser(t, db, "u-sysadmin", "sysadmin@example.com", "sysadmin", min(6), true)
	require.NoError(t, db.Exec(
		"UPDATE users SET is_system_admin = true WHERE id = ?", "u-sysadmin").Error)

	// 跨空间超管账号（无关系）→ 排除（同为超管目录口径）
	t44SeedUser(t, db, "u-all-tenants", "alltenants@example.com", "alltenants", min(7), true)
	require.NoError(t, db.Exec(
		"UPDATE users SET can_access_all_tenants = true WHERE id = ?", "u-all-tenants").Error)

	// 封禁的无关系账号 → 命中（第 4 个）：超管需要能看到并解封/注销
	t44SeedUser(t, db, "u-banned-loner", "bannedloner@example.com", "bannedloner", min(8), false)

	return []string{"u-loner", "u-suspended-member", "u-invited-only", "u-banned-loner"}
}

func t44RowIDs(rows []UserManagementScopedRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	return ids
}

// TestT44ListUsersWithoutMembershipFiltersOnlyMembershiplessUsers 锁定过滤
// 口径：只有「无 active 关系」的非墓碑、非特权账号出现，顺序稳定。
func TestT44ListUsersWithoutMembershipFiltersOnlyMembershiplessUsers(t *testing.T) {
	db := t44TestDB(t)
	want := t44SeedStandardFixture(t, db)
	repo := &tenantMemberRepository{db: db}

	rows, err := repo.ListUserManagementUsersWithoutMembership(context.Background(), "", 0, 50)
	require.NoError(t, err)
	require.Equal(t, want, t44RowIDs(rows),
		"目录必须只含无 active 成员关系的普通账号，按 created_at 升序")

	// 封禁账号保留且 is_active 如实投影（超管要能从 UI 解封/注销）。
	var banned *UserManagementScopedRow
	for i := range rows {
		if rows[i].UserID == "u-banned-loner" {
			banned = &rows[i]
		}
	}
	require.NotNil(t, banned, "封禁的无空间账号必须出现在目录里")
	require.False(t, banned.IsActive)
	require.Equal(t, "bannedloner@example.com", banned.Email)
}

// TestT44CountUsersWithoutMembershipMatchesListTotal 锁定计数与列表同口径。
func TestT44CountUsersWithoutMembershipMatchesListTotal(t *testing.T) {
	db := t44TestDB(t)
	want := t44SeedStandardFixture(t, db)
	repo := &tenantMemberRepository{db: db}

	total, err := repo.CountUserManagementUsersWithoutMembership(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, int64(len(want)), total)
}

// TestT44UsersWithoutMembershipSearchByEmailOrUsername 锁定 q 搜索口径：
// 邮箱/用户名模糊、大小写不敏感，与既有 scoped 列表一致。
func TestT44UsersWithoutMembershipSearchByEmailOrUsername(t *testing.T) {
	db := t44TestDB(t)
	_ = t44SeedStandardFixture(t, db)
	repo := &tenantMemberRepository{db: db}
	ctx := context.Background()

	// 注意搜索词选型：夹具里 u-loner 与 u-banned-loner 的邮箱/用户名都含
	// 「loner」子串，q=loner/LONER 必然命中两人（service 层 S-2 用例正是这
	// 么断言的）。这里要断言「只返回匹配者」，必须选唯一命中的词：BANNED
	// 只出现在 u-banned-loner 的邮箱与用户名里。
	for _, q := range []string{"BANNED", "bannedloner@example.com"} {
		rows, err := repo.ListUserManagementUsersWithoutMembership(ctx, q, 0, 50)
		require.NoError(t, err, "q=%s", q)
		require.Equal(t, []string{"u-banned-loner"}, t44RowIDs(rows),
			"q=%s 应只命中唯一匹配者（邮箱与用户名均含该词，大小写不敏感）", q)

		total, err := repo.CountUserManagementUsersWithoutMembership(ctx, q)
		require.NoError(t, err)
		require.Equal(t, int64(1), total, "q=%s 计数与列表一致", q)
	}

	// 搜索词命中有关系的账号 → 空结果（该账号不在无空间目录）。
	rows, err := repo.ListUserManagementUsersWithoutMembership(ctx, "member@example.com", 0, 50)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// TestT44UsersWithoutMembershipPaginationIsScopedListShaped 锁定分页同构：
// offset/limit 切片无重复无遗漏。
func TestT44UsersWithoutMembershipPaginationIsScopedListShaped(t *testing.T) {
	db := t44TestDB(t)
	want := t44SeedStandardFixture(t, db)
	repo := &tenantMemberRepository{db: db}
	ctx := context.Background()

	first, err := repo.ListUserManagementUsersWithoutMembership(ctx, "", 0, 2)
	require.NoError(t, err)
	second, err := repo.ListUserManagementUsersWithoutMembership(ctx, "", 2, 2)
	require.NoError(t, err)
	require.Equal(t, want[:2], t44RowIDs(first))
	require.Equal(t, want[2:], t44RowIDs(second))
}
