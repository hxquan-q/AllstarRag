package database

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

// t01pg 边界造数用的空间 ID（避开 agent-tools 用例的 990116）。
const (
	t01pgTenantA = 30101
	t01pgTenantB = 30102
	t01pgTenantC = 30103
)

// TestPostgresSingleSpaceMigration 在真实 PostgreSQL 上执行 versioned 迁移
// 000137；启动一次性库的方式见 TestPostgresMigrationsServeAgentHistory 头注。
//
// 它是 TestT01SingleSpaceMigration（SQLite 000054）的 PG 方言对齐版：
// 先迁到 136，造出与 SQLite 用例相同的边界数据（多 owner、非超管多空间、
// contributor、pending + 跨空间邀请、零 owner 空间），再迁到 137 并核对
// 迁移不变量（AC-3/4/21/22/23/24/25）。
func TestPostgresSingleSpaceMigration(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to run PostgreSQL migration tests")
	}
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, root)

	m, err := migrate.New("file://migrations/versioned", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, ignoreNoChange(m.Migrate(versionedMigrationBefore(t, root, 137))))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	seedT01PostgresSingleSpaceData(t, db)

	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))

	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, int(versionedMigrationBefore(t, root, 138)), version,
		"迁移后 versioned 版本应为 000137")
	require.False(t, dirty)

	// AC-21：A 空间只保留 joined_at 最早的 active owner，其余 owner 降为 admin。
	rows, err := db.Query(
		`SELECT user_id FROM tenant_members
		 WHERE tenant_id = $1 AND role = 'owner' AND status = 'active' AND deleted_at IS NULL
		 ORDER BY joined_at ASC, id ASC`, t01pgTenantA)
	require.NoError(t, err)
	var owners []string
	for rows.Next() {
		var userID string
		require.NoError(t, rows.Scan(&userID))
		owners = append(owners, userID)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"t01pg-owner-early"}, owners,
		"A 空间迁移后必须只剩 joined_at 最早的 1 个 active owner")

	var lateRole string
	require.NoError(t, db.QueryRow(
		`SELECT role FROM tenant_members WHERE tenant_id = $1 AND user_id = $2`,
		t01pgTenantA, "t01pg-owner-late",
	).Scan(&lateRole))
	require.Equal(t, "admin", lateRole, "多余的 owner 必须降为 admin")

	// AC-3：contributor 全部迁移为 admin。
	var contributorCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members WHERE role = 'contributor'`,
	).Scan(&contributorCount))
	require.Zero(t, contributorCount, "迁移后不得再存在 contributor 成员关系")

	// AC-22：X 只保留 1 条 active 成员关系（A），B 置 suspended；home 修正为 A。
	var activeCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members
		 WHERE user_id = $1 AND status = 'active' AND deleted_at IS NULL`, "t01pg-x",
	).Scan(&activeCount))
	require.Equal(t, 1, activeCount, "多空间账号迁移后只能保留 1 条 active 成员关系")

	var keptTenant int
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id FROM tenant_members
		 WHERE user_id = $1 AND status = 'active' AND deleted_at IS NULL`, "t01pg-x",
	).Scan(&keptTenant))
	require.Equal(t, t01pgTenantA, keptTenant, "应保留 joined_at 最早的 A 空间成员关系")

	var suspendedStatus string
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant_members WHERE user_id = $1 AND tenant_id = $2`,
		"t01pg-x", t01pgTenantB,
	).Scan(&suspendedStatus))
	require.Equal(t, "suspended", suspendedStatus, "其余空间成员关系必须挂起而不是硬删")

	var homeTenant sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id FROM users WHERE id = $1`, "t01pg-x",
	).Scan(&homeTenant))
	require.True(t, homeTenant.Valid)
	require.Equal(t, int64(t01pgTenantA), homeTenant.Int64,
		"users.tenant_id 必须指向保留的 active 成员空间")

	// AC-23：pending contributor 邀请撤销，且记录响应时间。
	var inviteeStatus string
	var inviteeRespondedAt sql.NullTime
	require.NoError(t, db.QueryRow(
		`SELECT status, responded_at FROM tenant_invitations
		 WHERE invitee_user_id = $1 AND tenant_id = $2`,
		"t01pg-invitee", t01pgTenantA,
	).Scan(&inviteeStatus, &inviteeRespondedAt))
	require.Equal(t, "revoked", inviteeStatus, "pending contributor 邀请必须置为 revoked")
	require.True(t, inviteeRespondedAt.Valid, "邀请撤销时必须写入 responded_at")

	// AC-23 补充：已在 A 空间 active 的 viewer 收到 B 空间 pending 邀请，
	// 属于跨空间邀请，迁移后必须撤销并记录响应时间。
	var crossSpaceStatus string
	var crossSpaceRespondedAt sql.NullTime
	require.NoError(t, db.QueryRow(
		`SELECT status, responded_at FROM tenant_invitations
		 WHERE invitee_user_id = $1 AND tenant_id = $2`,
		"t01pg-viewer", t01pgTenantB,
	).Scan(&crossSpaceStatus, &crossSpaceRespondedAt))
	require.Equal(t, "revoked", crossSpaceStatus,
		"已在其他空间 active 的用户收到的跨空间 pending 邀请必须置为 revoked")
	require.True(t, crossSpaceRespondedAt.Valid, "跨空间邀请撤销时必须写入 responded_at")

	// AC-24：零 owner 空间保持现状，不自动补 owner。
	var zeroOwnerCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM tenant_members
		 WHERE tenant_id = $1 AND role = 'owner' AND deleted_at IS NULL`, t01pgTenantC,
	).Scan(&zeroOwnerCount))
	require.Zero(t, zeroOwnerCount, "零 owner 空间不得被自动补 owner")

	// AC-4：唯一 owner 部分索引存在且唯一，插入第二个 owner 必须失败。
	var indexDef string
	require.NoError(t, db.QueryRow(
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_tenant_members_tenant_owner_unique'`,
	).Scan(&indexDef))
	require.Contains(t, strings.ToUpper(indexDef), "UNIQUE",
		"唯一 owner 索引必须是 UNIQUE 索引：%s", indexDef)
	_, err = db.Exec(
		`INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at)
		 VALUES ($1, $2, 'owner', 'active', CURRENT_TIMESTAMP)`,
		"t01pg-extra-owner", t01pgTenantA)
	require.Error(t, err, "唯一 owner 索引必须阻止同空间插入第二个 owner")
	require.Contains(t, err.Error(), "idx_tenant_members_tenant_owner_unique",
		"插入冲突应来自唯一 owner 索引：%v", err)

	// AC-25：迁移写审计。details 是 jsonb，只核对关键列与关键键值。
	var auditCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM audit_logs
		 WHERE action = 'system.membership_migrated' AND tenant_id = 0
		   AND actor_role = 'system_admin' AND scope_id = 'migration:single-space-per-account'`,
	).Scan(&auditCount))
	require.Greater(t, auditCount, 0, "迁移必须写 system.membership_migrated 审计")

	auditRows, err := db.Query(
		`SELECT DISTINCT details->>'change' FROM audit_logs
		 WHERE action = 'system.membership_migrated'
		   AND scope_id = 'migration:single-space-per-account'`)
	require.NoError(t, err)
	changes := map[string]bool{}
	for auditRows.Next() {
		var change sql.NullString
		require.NoError(t, auditRows.Scan(&change))
		if change.Valid {
			changes[change.String] = true
		}
	}
	require.NoError(t, auditRows.Err())
	require.NoError(t, auditRows.Close())
	for _, change := range []string{
		"owner_demoted", "contributor_to_admin", "multi_space_suspended", "invitation_revoked",
	} {
		require.True(t, changes[change], "审计必须包含 change=%s，实际：%v", change, changes)
	}

	// 跨空间撤销的审计带 reason=cross_space。
	var crossSpaceAudit int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM audit_logs
		 WHERE action = 'system.membership_migrated'
		   AND details->>'change' = 'invitation_revoked'
		   AND details->>'reason' = 'cross_space'`).Scan(&crossSpaceAudit))
	require.Equal(t, 1, crossSpaceAudit, "跨空间邀请撤销必须写 reason=cross_space 审计")
}

// seedT01PostgresSingleSpaceData 造出 000137 需要收敛的存量边界数据，
// 与 SQLite 用例 seedT01SingleSpaceData 逐行对齐；PG 侧补齐 NOT NULL 列
// （tenants.business、users.password_hash；tenants.api_key 已被 000065 删除）。
func seedT01PostgresSingleSpaceData(t *testing.T, db *sql.DB) {
	t.Helper()

	// 允许在同一个一次性库上重复执行本用例：先清上一轮的造数与审计。
	for _, stmt := range []string{
		`DELETE FROM audit_logs WHERE scope_id = 'migration:single-space-per-account'`,
		`DELETE FROM tenant_invitations WHERE tenant_id IN (30101, 30102, 30103)`,
		`DELETE FROM tenant_members WHERE tenant_id IN (30101, 30102, 30103) OR user_id LIKE 't01pg-%'`,
		`DELETE FROM users WHERE id LIKE 't01pg-%'`,
		`DELETE FROM tenants WHERE id IN (30101, 30102, 30103)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	for _, tenant := range []struct {
		id   int
		name string
	}{
		{t01pgTenantA, "t01pg-space-a"},
		{t01pgTenantB, "t01pg-space-b"},
		{t01pgTenantC, "t01pg-space-c"},
	} {
		_, err := db.Exec(
			`INSERT INTO tenants (id, name, business) VALUES ($1, $2, 't01pg-migration-test')`,
			tenant.id, tenant.name)
		require.NoError(t, err)
	}

	for _, user := range []struct {
		id       string
		username string
		email    string
		tenantID any
	}{
		{"t01pg-owner-early", "t01pg-owner-early", "t01pg-owner-early@example.com", t01pgTenantA},
		{"t01pg-owner-late", "t01pg-owner-late", "t01pg-owner-late@example.com", t01pgTenantA},
		{"t01pg-contrib", "t01pg-contrib", "t01pg-contrib@example.com", t01pgTenantA},
		{"t01pg-viewer", "t01pg-viewer", "t01pg-viewer@example.com", t01pgTenantA},
		// 故意把 X 的 home 写成 C（X 在 C 没有任何成员关系），
		// 验证迁移会按 joined_at 保留 A 并把 users.tenant_id 修正为 A。
		{"t01pg-x", "t01pg-x", "t01pg-x@example.com", t01pgTenantC},
		{"t01pg-invitee", "t01pg-invitee", "t01pg-invitee@example.com", nil},
		// 插入第二个 owner 的探针用户（AC-4）。
		{"t01pg-extra-owner", "t01pg-extra-owner", "t01pg-extra-owner@example.com", nil},
	} {
		_, err := db.Exec(
			`INSERT INTO users (id, username, email, password_hash, tenant_id)
			 VALUES ($1, $2, $3, 'x', $4)`,
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
		{"t01pg-owner-early", t01pgTenantA, "owner", "active", "2024-01-01 00:00:00+00"},
		{"t01pg-owner-late", t01pgTenantA, "owner", "active", "2024-06-01 00:00:00+00"},
		{"t01pg-contrib", t01pgTenantA, "contributor", "active", "2024-02-01 00:00:00+00"},
		{"t01pg-viewer", t01pgTenantA, "viewer", "active", "2024-02-15 00:00:00+00"},
		{"t01pg-x", t01pgTenantA, "viewer", "active", "2024-03-01 00:00:00+00"},
		{"t01pg-x", t01pgTenantB, "viewer", "active", "2024-08-01 00:00:00+00"},
	} {
		_, err := db.Exec(
			`INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			membership.userID, membership.tenantID, membership.role, membership.status, membership.joinedAt)
		require.NoError(t, err)
	}

	_, err := db.Exec(
		`INSERT INTO tenant_invitations (tenant_id, invitee_user_id, role, status, expires_at)
		 VALUES ($1, $2, 'contributor', 'pending', '2099-01-01 00:00:00+00')`,
		t01pgTenantA, "t01pg-invitee")
	require.NoError(t, err)

	// AC-23 补充：t01pg-viewer 已是 A 空间的 active 成员，B 空间又发来一条
	// pending 邀请，属于「跨空间邀请」，迁移后必须撤销。
	_, err = db.Exec(
		`INSERT INTO tenant_invitations (tenant_id, invitee_user_id, role, status, expires_at)
		 VALUES ($1, $2, 'viewer', 'pending', '2099-01-01 00:00:00+00')`,
		t01pgTenantB, "t01pg-viewer")
	require.NoError(t, err)
}
