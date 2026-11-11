package database

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// CP3 B1：交接必须在「迁移后 schema」上验证，而不是 AutoMigrate 测试库。
//
// 迁移 000054 建立的唯一 owner 部分索引 idx_tenant_members_tenant_owner_unique
// 是语句级立即检查；AutoMigrate 的 TenantMember 模型没有该索引，所以旧的
// transfer 测试看不到「先升后降」撞索引的问题（PITFALLS D-12）。
// 本文件先跑真实 SQLite 迁移到 54，再在该库上执行交接：
//   (a) from=owner + to=admin → 成功，目标 owner、源 admin、全空间仅一个 owner；
//   (c) 并发双交接 → 恰好一个成功，另一个 ErrRoleChanged（无半迁移）。
// (b) from==to 的服务层 400 校验在同轮 service 用例中覆盖。

const cp3B1Tenant = 70201

func cp3OpenMigratedSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)

	dbPath := filepath.Join(t.TempDir(), "cp3-b1-transfer.db")
	require.NoError(t, RunMigrationsWithOptions(
		"sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	gdb, err := gorm.Open(
		sqlite.Open(dbPath+"?_busy_timeout=5000&_journal_mode=WAL"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 先证明迁移确实建了唯一 owner 索引，否则本文件会退化成旧的盲区测试。
	require.True(t, sqliteHasSingleOwnerIndex(t, sqlDB),
		"迁移后必须有 (tenant_id) WHERE role='owner' 的唯一索引")
	return gdb
}

func cp3SeedTransferTenant(t *testing.T, db *gorm.DB, users map[string]types.TenantRole) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, name, business) VALUES (?, ?, ?)`,
		cp3B1Tenant, "cp3-b1-transfer", "cp3-verify").Error)
	for userID := range users {
		require.NoError(t, db.Exec(
			`INSERT INTO users (id, username, email, password_hash, tenant_id)
			 VALUES (?, ?, ?, 'x', ?)`,
			userID, "cp3-"+userID, userID+"@example.com", cp3B1Tenant).Error)
	}
	for userID, role := range users {
		require.NoError(t, db.Create(&types.TenantMember{
			UserID:   userID,
			TenantID: cp3B1Tenant,
			Role:     role,
			Status:   types.TenantMemberStatusActive,
		}).Error)
	}
}

func cp3MemberRole(t *testing.T, repo interface {
	Get(context.Context, string, uint64) (*types.TenantMember, error)
}, userID string) types.TenantRole {
	t.Helper()
	member, err := repo.Get(context.Background(), userID, cp3B1Tenant)
	require.NoError(t, err)
	require.NotNil(t, member)
	return member.Role
}

// TestCP3B1TransferOnMigratedSchema 覆盖交接三面中的 (a) 正常交接与并发不变量。
func TestCP3B1TransferOnMigratedSchema(t *testing.T) {
	db := cp3OpenMigratedSQLite(t)
	repo := repository.NewTenantMemberRepository(db)
	ctx := context.Background()

	t.Run("owner_to_admin_succeeds_with_unique_owner_index", func(t *testing.T) {
		cp3SeedTransferTenant(t, db, map[string]types.TenantRole{
			"cp3-a-owner": types.TenantRoleOwner,
			"cp3-a-admin": types.TenantRoleAdmin,
		})

		require.NoError(t, repo.TransferOwnershipTx(ctx, cp3B1Tenant, "cp3-a-owner", "cp3-a-admin"),
			"迁移后 schema 上 from=owner、to=admin 的交接必须成功")
		require.Equal(t, types.TenantRoleAdmin, cp3MemberRole(t, repo, "cp3-a-owner"))
		require.Equal(t, types.TenantRoleOwner, cp3MemberRole(t, repo, "cp3-a-admin"))

		owners, err := repo.CountActiveOwners(ctx, cp3B1Tenant)
		require.NoError(t, err)
		require.EqualValues(t, 1, owners, "交接后空间必须恰好一个 active owner")
	})

	t.Run("concurrent_transfers_leave_exactly_one_winner", func(t *testing.T) {
		const tenant = cp3B1Tenant + 1
		require.NoError(t, db.Exec(
			`INSERT INTO tenants (id, name, business) VALUES (?, ?, ?)`,
			tenant, "cp3-b1-race", "cp3-verify").Error)
		for _, userID := range []string{"cp3-r-owner", "cp3-r-admin1", "cp3-r-admin2"} {
			require.NoError(t, db.Exec(
				`INSERT INTO users (id, username, email, password_hash, tenant_id)
				 VALUES (?, ?, ?, 'x', ?)`,
				userID, "cp3-"+userID, userID+"@example.com", tenant).Error)
		}
		for _, m := range []struct {
			userID string
			role   types.TenantRole
		}{
			{"cp3-r-owner", types.TenantRoleOwner},
			{"cp3-r-admin1", types.TenantRoleAdmin},
			{"cp3-r-admin2", types.TenantRoleAdmin},
		} {
			require.NoError(t, db.Create(&types.TenantMember{
				UserID:   m.userID,
				TenantID: tenant,
				Role:     m.role,
				Status:   types.TenantMemberStatusActive,
			}).Error)
		}

		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, target := range []string{"cp3-r-admin1", "cp3-r-admin2"} {
			i, target := i, target
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = repo.TransferOwnershipTx(ctx, tenant, "cp3-r-owner", target)
			}()
		}
		close(start)
		wg.Wait()

		success, changed := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				success++
			case errors.Is(err, repository.ErrRoleChanged):
				changed++
			default:
				t.Fatalf("concurrent transfer returned unexpected error: %v", err)
			}
		}
		require.Equal(t, 1, success, "并发双交接必须恰好一个成功")
		require.Equal(t, 1, changed, "失败方必须返回 ErrRoleChanged（角色已被并发修改）")

		var ownerCount int64
		require.NoError(t, db.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND role = ? AND status = ? AND deleted_at IS NULL",
				tenant, types.TenantRoleOwner, types.TenantMemberStatusActive).
			Count(&ownerCount).Error)
		require.EqualValues(t, 1, ownerCount, "并发结束后仍必须恰好一个 owner")
	})
}
