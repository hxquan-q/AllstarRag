package database

import (
	"context"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// CP3 B1（PG 方言）：在 disposable PostgreSQL 上跑完整迁移后，用真实
// 部分唯一索引验证交接顺序。守卫与既有 PG 迁移测试一致：
// 只有设置 WEKNORA_MIGRATION_TEST_POSTGRES_DSN 才执行。
func TestCP3B1PostgresTransferAfterFullMigration(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to run PostgreSQL migration tests")
	}
	chdirAndRestore(t, sqliteRepoRoot(t))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	const tenantID = 70202
	const ownerID = "cp3-pg-owner"
	const adminID = "cp3-pg-admin"
	t.Cleanup(func() {
		db.Exec(`DELETE FROM tenant_members WHERE tenant_id = ?`, tenantID)
		db.Exec(`DELETE FROM users WHERE id IN (?, ?)`, ownerID, adminID)
		db.Exec(`DELETE FROM tenants WHERE id = ?`, tenantID)
	})

	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, name, business) VALUES (?, ?, ?)`,
		tenantID, "cp3-pg-transfer", "cp3-verify").Error)
	for _, userID := range []string{ownerID, adminID} {
		require.NoError(t, db.Exec(
			`INSERT INTO users (id, username, email, password_hash, tenant_id)
			 VALUES (?, ?, ?, 'x', ?)`,
			userID, userID, userID+"@example.com", tenantID).Error)
	}
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: ownerID, TenantID: tenantID,
		Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive,
	}).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID: adminID, TenantID: tenantID,
		Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive,
	}).Error)

	repo := repository.NewTenantMemberRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.TransferOwnershipTx(ctx, tenantID, ownerID, adminID),
		"PG 迁移后 schema 上交接必须成功（先降后升）")

	var fromRole, toRole types.TenantRole
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND user_id = ?", tenantID, ownerID).
		Select("role").Row().Scan(&fromRole))
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND user_id = ?", tenantID, adminID).
		Select("role").Row().Scan(&toRole))
	require.Equal(t, types.TenantRoleAdmin, fromRole)
	require.Equal(t, types.TenantRoleOwner, toRole)

	var owners int64
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND role = ? AND status = ? AND deleted_at IS NULL",
			tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive).
		Count(&owners).Error)
	require.EqualValues(t, 1, owners)
}
