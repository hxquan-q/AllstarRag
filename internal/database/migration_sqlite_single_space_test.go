package database

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// T01 红阶段：single-space-per-account 的 SQLite 迁移 000054 尚不存在。
//
// 用例先把临时库迁到 000053，造出多 owner / 多空间 / contributor / pending
// 邀请 / 零 owner 空间 / users.tenant_id 不一致等存量数据，再迁到 000054 并
// 核对迁移不变量（AC-3/4/21/22/23/24/25）。
const (
	t01TenantA = 101
	t01TenantB = 102
	t01TenantC = 103
)

func TestT01SingleSpaceMigration(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)

	legacyRoot := copySQLiteMigrationsThrough(t, repoRoot, 53)
	chdirAndRestore(t, legacyRoot)

	dbPath := filepath.Join(t.TempDir(), "single-space.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db := openSQLiteDB(t, dbPath)
	version, dirty := sqliteMigrationState(t, db)
	require.Equal(t, 53, version, "前置条件：临时库必须先迁到 000053")
	require.False(t, dirty)

	seedT01SingleSpaceData(t, db)
	require.NoError(t, db.Close())

	// 回到仓库根目录执行完整迁移；当前缺 000054 时版本会停在 53。
	chdirAndRestore(t, repoRoot)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db = openSQLiteDB(t, dbPath)
	version, dirty = sqliteMigrationState(t, db)
	require.Equal(t, expectedSQLiteMigrationVersion, version,
		"T01 迁移后 SQLite 版本应为 %d；若仍为 53 说明 000054_single_space_per_account 尚未落地",
		expectedSQLiteMigrationVersion)
	require.False(t, dirty)

	// AC-21：A 空间只保留 joined_at 最早的 active owner，其余 owner 降为 admin。
	rows, err := db.Query(
		`SELECT user_id FROM tenant_members
		 WHERE tenant_id = ? AND role = 'owner' AND status = 'active' AND deleted_at IS NULL
		 ORDER BY joined_at ASC, id ASC`, t01TenantA)
	require.NoError(t, err)
	var owners []string
	for rows.Next() {
		var userID string
		require.NoError(t, rows.Scan(&userID))
		owners = append(owners, userID)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"u-owner-early"}, owners,
		"A 空间迁移后必须只剩 joined_at 最早的 1 个 active owner")

	var lateRole string
	require.NoError(t, db.QueryRow(
		`SELECT role FROM tenant_members WHERE tenant_id = ? AND user_id = ?`, t01TenantA, "u-owner-late",
	).Scan(&lateRole))
	require.Equal(t, "admin", lateRole, "多余的 owner 必须降为 admin")

	// AC-3：contributor 全部迁移为 admin。
	var contributorCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members WHERE role = 'contributor'`,
	).Scan(&contributorCount))
	require.Zero(t, contributorCount, "迁移后不得再存在 contributor 成员关系")

	// AC-22：X 只保留 1 条 active 成员关系（A），B 置 suspended；users.tenant_id 修正为 A。
	var activeCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members WHERE user_id = ? AND status = 'active' AND deleted_at IS NULL`, "u-x",
	).Scan(&activeCount))
	require.Equal(t, 1, activeCount, "多空间账号迁移后只能保留 1 条 active 成员关系")

	var keptTenant int
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id FROM tenant_members WHERE user_id = ? AND status = 'active' AND deleted_at IS NULL`, "u-x",
	).Scan(&keptTenant))
	require.Equal(t, t01TenantA, keptTenant, "应保留 joined_at 最早的 A 空间成员关系")

	var suspendedStatus string
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant_members WHERE user_id = ? AND tenant_id = ?`, "u-x", t01TenantB,
	).Scan(&suspendedStatus))
	require.Equal(t, "suspended", suspendedStatus, "其余空间成员关系必须挂起而不是硬删")

	var homeTenant int
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM users WHERE id = ?`, "u-x").Scan(&homeTenant))
	require.Equal(t, t01TenantA, homeTenant, "users.tenant_id 必须指向保留的 active 成员空间")

	// AC-23：pending contributor 邀请撤销。
	var invitationStatus string
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant_invitations WHERE invitee_user_id = ? AND tenant_id = ?`,
		"u-invitee", t01TenantA,
	).Scan(&invitationStatus))
	require.Equal(t, "revoked", invitationStatus, "pending contributor 邀请必须置为 revoked")

	// AC-24：零 owner 空间保持现状，不自动补 owner。
	var zeroOwnerCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members WHERE tenant_id = ? AND role = 'owner' AND deleted_at IS NULL`,
		t01TenantC,
	).Scan(&zeroOwnerCount))
	require.Zero(t, zeroOwnerCount, "零 owner 空间不得被自动补 owner")

	// AC-4：唯一 owner 部分索引存在，且写入第二个 owner 失败。
	require.True(t, sqliteHasSingleOwnerIndex(t, db),
		"必须存在 tenant_members(tenant_id) WHERE role='owner' AND deleted_at IS NULL 的唯一索引")
	_, err = db.Exec(
		`INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at)
		 VALUES (?, ?, 'owner', 'active', CURRENT_TIMESTAMP)`, "u-extra-owner", t01TenantA)
	require.Error(t, err, "唯一 owner 索引必须阻止同空间插入第二个 owner")

	// AC-25：迁移逐条写审计。
	var auditCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM audit_logs
		 WHERE action = 'system.membership_migrated' AND tenant_id = 0
		   AND actor_role = 'system_admin' AND scope_id = 'migration:single-space-per-account'`,
	).Scan(&auditCount))
	require.Greater(t, auditCount, 0, "迁移必须写 system.membership_migrated 审计")
}

// seedT01SingleSpaceData 造出 T01 迁移需要覆盖的存量边界数据。
func seedT01SingleSpaceData(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, tenant := range []struct {
		id   int
		name string
	}{
		{t01TenantA, "t01-space-a"},
		{t01TenantB, "t01-space-b"},
		{t01TenantC, "t01-space-c"},
	} {
		_, err := db.Exec(
			`INSERT INTO tenants (id, name, business) VALUES (?, ?, ?)`,
			tenant.id, tenant.name, "t01-migration-test")
		require.NoError(t, err)
	}

	for _, user := range []struct {
		id       string
		username string
		email    string
		tenantID any
	}{
		{"u-owner-early", "t01-owner-early", "t01-owner-early@example.com", t01TenantA},
		{"u-owner-late", "t01-owner-late", "t01-owner-late@example.com", t01TenantA},
		{"u-contrib", "t01-contrib", "t01-contrib@example.com", t01TenantA},
		{"u-viewer", "t01-viewer", "t01-viewer@example.com", t01TenantA},
		// 故意把 X 的 home 写成 C（X 在 C 没有任何成员关系），
		// 验证迁移会按 joined_at 保留 A 并把 users.tenant_id 修正为 A。
		{"u-x", "t01-x", "t01-x@example.com", t01TenantC},
		{"u-invitee", "t01-invitee", "t01-invitee@example.com", nil},
	} {
		_, err := db.Exec(
			`INSERT INTO users (id, username, email, password_hash, tenant_id)
			 VALUES (?, ?, ?, 'x', ?)`,
			user.id, user.username, user.email, user.tenantID)
		require.NoError(t, err)
	}

	for _, membership := range []struct {
		userID   string
		tenantID int
		role     string
		status   string
		joinedAt string
	}{
		{"u-owner-early", t01TenantA, "owner", "active", "2024-01-01 00:00:00"},
		{"u-owner-late", t01TenantA, "owner", "active", "2024-06-01 00:00:00"},
		{"u-contrib", t01TenantA, "contributor", "active", "2024-02-01 00:00:00"},
		{"u-viewer", t01TenantA, "viewer", "active", "2024-02-15 00:00:00"},
		{"u-x", t01TenantA, "viewer", "active", "2024-03-01 00:00:00"},
		{"u-x", t01TenantB, "viewer", "active", "2024-08-01 00:00:00"},
	} {
		_, err := db.Exec(
			`INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at)
			 VALUES (?, ?, ?, ?, ?)`,
			membership.userID, membership.tenantID, membership.role, membership.status, membership.joinedAt)
		require.NoError(t, err)
	}

	_, err := db.Exec(
		`INSERT INTO tenant_invitations (tenant_id, invitee_user_id, role, status, expires_at)
		 VALUES (?, ?, 'contributor', 'pending', '2099-01-01 00:00:00')`,
		t01TenantA, "u-invitee")
	require.NoError(t, err)
}

// sqliteHasSingleOwnerIndex 检查 tenant_members 上是否存在
// 「(tenant_id) WHERE role='owner' AND deleted_at IS NULL」的部分唯一索引。
func sqliteHasSingleOwnerIndex(t *testing.T, db *sql.DB) bool {
	t.Helper()
	rows, err := db.Query(
		`SELECT sql FROM sqlite_master
		 WHERE type = 'index' AND tbl_name = 'tenant_members' AND sql IS NOT NULL`,
	)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var stmt string
		require.NoError(t, rows.Scan(&stmt))
		normalized := strings.ToLower(stmt)
		if strings.Contains(normalized, "unique") &&
			strings.Contains(normalized, "tenant_id") &&
			strings.Contains(normalized, "role") &&
			strings.Contains(normalized, "owner") &&
			strings.Contains(normalized, "deleted_at") {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
}
