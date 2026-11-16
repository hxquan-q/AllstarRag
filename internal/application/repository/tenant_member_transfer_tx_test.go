package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// T40 TDD 红阶段：空间交接并发一致性（GPT #7 / #8）。
//
// 契约（详见 05-tests §63）：
//   - 成员仓储接口新增
//     TransferOwnershipTx(ctx, tenantID, fromUserID, toUserID) error；
//   - 单事务内按确定性顺序锁定相关 tenant_members（按 user_id 升序
//     FOR UPDATE，或 tenant 级锁）；
//   - 事务内复核 from 为 active owner、to 为 active 成员且用户未被封禁；
//   - to 提升 owner、from 降为 admin；任一步失败整体回滚，不做跨事务补偿。
//
// 本文件只写测试，不改实现；方法缺失时下面的包级断言让本测试包编译失败，
// 这是 T40 的预期红点 E9。

type t40OwnershipTransferRepo interface {
	TransferOwnershipTx(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error
}

// E9：接口缺少 TransferOwnershipTx 时编译失败（T40 验收 5）。
var _ t40OwnershipTransferRepo = interfaces.TenantMemberRepository(nil)

func t40NewRepoDB(t *testing.T) (*gorm.DB, t40OwnershipTransferRepo) {
	t.Helper()
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "t40.db")) +
		"?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		// 并发用例里 SQLite 的 SQLITE_BUSY 会由 busy_timeout 等待；
		// 这里静默 GORM 日志，保证测试输出只保留断言信息。
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantMember{},
	))

	base := NewTenantMemberRepository(db)
	transfer, ok := base.(t40OwnershipTransferRepo)
	require.True(t, ok,
		"tenant member repository must implement TransferOwnershipTx (T40 contract)")
	return db, transfer
}

func t40SeedTenant(t *testing.T, db *gorm.DB, tenantID uint64) {
	t.Helper()
	require.NoError(t, db.Create(&types.Tenant{
		ID:     tenantID,
		Name:   "t40-workspace",
		Status: "active",
	}).Error)
}

// t40SeedUser 先按默认值落库，再显式关闭 is_active：users.is_active 带
// gorm:"default:true"，直接 Create(false) 会被列默认值覆盖。
func t40SeedUser(t *testing.T, db *gorm.DB, userID string, active bool) {
	t.Helper()
	require.NoError(t, db.Create(&types.User{
		ID:           userID,
		Username:     userID,
		Email:        userID + "@example.com",
		PasswordHash: "hash-" + userID,
		IsActive:     true,
	}).Error)
	if !active {
		require.NoError(t, db.Model(&types.User{}).
			Where("id = ?", userID).
			Update("is_active", false).Error)
	}
}

func t40SeedMember(
	t *testing.T,
	db *gorm.DB,
	userID string,
	tenantID uint64,
	role types.TenantRole,
	status types.TenantMemberStatus,
) {
	t.Helper()
	require.NoError(t, db.Create(&types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   status,
		JoinedAt: time.Now(),
	}).Error)
}

func t40LoadRole(
	t *testing.T, db *gorm.DB, userID string, tenantID uint64,
) types.TenantRole {
	t.Helper()
	var member types.TenantMember
	require.NoError(t, db.
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		First(&member).Error)
	return member.Role
}

func t40CountActiveOwners(t *testing.T, db *gorm.DB, tenantID uint64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND role = ? AND status = ?",
			tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive).
		Count(&count).Error)
	return count
}

// E1：正常交接在单个事务内把 to 提升 owner、from 降为 admin。
func TestT40TransferOwnershipTxNormal(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	t40SeedUser(t, db, "A", true)
	t40SeedUser(t, db, "B", true)
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	require.NoError(t, transfer.TransferOwnershipTx(ctx, tenantID, "A", "B"))

	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "A", tenantID),
		"from must be demoted to admin")
	require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, "B", tenantID),
		"to must be promoted to owner")
	require.EqualValues(t, 1, t40CountActiveOwners(t, db, tenantID),
		"transfer must leave exactly one active owner")
}

// E2：第二步失败（from 降级被数据库拒绝）时，第一步对 to 的提升必须
// 一起回滚；不存在跨事务补偿。用 SQLite trigger 在 A 的降级 UPDATE 上
// 注入 ABORT，保证失败点在事务内部。
func TestT40TransferOwnershipTxRollsBackDemotionFailure(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	t40SeedUser(t, db, "A", true)
	t40SeedUser(t, db, "B", true)
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	require.NoError(t, db.Exec(`
		CREATE TRIGGER t40_abort_a_demotion
		BEFORE UPDATE ON tenant_members
		WHEN OLD.user_id = 'A' AND NEW.role <> 'owner'
		BEGIN
			SELECT RAISE(ABORT, 't40 injected demotion failure');
		END
	`).Error)

	err := transfer.TransferOwnershipTx(ctx, tenantID, "A", "B")
	require.Error(t, err, "demotion failure must surface as an error")

	require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, "A", tenantID),
		"from must stay owner after rollback")
	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "B", tenantID),
		"to promotion must be rolled back together with the failed demotion")
	require.EqualValues(t, 1, t40CountActiveOwners(t, db, tenantID))
}

// E3：事务内复核源必须是 active owner；源已不是 owner 时拒绝且零变化。
func TestT40TransferOwnershipTxRejectsNonOwnerSource(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	t40SeedUser(t, db, "A", true)
	t40SeedUser(t, db, "B", true)
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	err := transfer.TransferOwnershipTx(ctx, tenantID, "A", "B")
	require.Error(t, err, "a non-owner source must be rejected inside the transaction")

	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "A", tenantID))
	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "B", tenantID))
	require.EqualValues(t, 0, t40CountActiveOwners(t, db, tenantID))
}

// E4a：目标账号被封禁（users.is_active=false）时拒绝且零变化。
func TestT40TransferOwnershipTxRejectsBannedTarget(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	t40SeedUser(t, db, "A", true)
	t40SeedUser(t, db, "B", false)
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	err := transfer.TransferOwnershipTx(ctx, tenantID, "A", "B")
	require.Error(t, err, "a banned target must be rejected inside the transaction")

	require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, "A", tenantID))
	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "B", tenantID))
}

// E4b：目标成员关系不是 active（如 suspended）时拒绝且零变化。
func TestT40TransferOwnershipTxRejectsInactiveTargetMembership(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	ctx := context.Background()
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	t40SeedUser(t, db, "A", true)
	t40SeedUser(t, db, "B", true)
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusSuspended)

	err := transfer.TransferOwnershipTx(ctx, tenantID, "A", "B")
	require.Error(t, err, "an inactive target membership must be rejected")

	require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, "A", tenantID))
	require.Equal(t, types.TenantRoleAdmin, t40LoadRole(t, db, "B", tenantID))
}

// T03：交接目标必须是本空间 active 普通管理员（admin）；viewer 与
// contributor 目标必须在事务内被拒绝，且不产生任何角色变化。
func TestT03TransferOwnershipTxRejectsNonAdminTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     types.TenantRole
		tenantID uint64
	}{
		{name: "viewer", role: types.TenantRoleViewer, tenantID: 103},
		{name: "contributor", role: types.TenantRoleContributor, tenantID: 104},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db, transfer := t40NewRepoDB(t)
			ctx := context.Background()

			t40SeedTenant(t, db, tc.tenantID)
			t40SeedUser(t, db, "A", true)
			t40SeedUser(t, db, "B", true)
			t40SeedMember(t, db, "A", tc.tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
			t40SeedMember(t, db, "B", tc.tenantID, tc.role, types.TenantMemberStatusActive)

			err := transfer.TransferOwnershipTx(ctx, tc.tenantID, "A", "B")
			require.Error(t, err, "transfer target must be an active admin, got %s", tc.role)

			require.Equal(t, types.TenantRoleOwner, t40LoadRole(t, db, "A", tc.tenantID),
				"source must stay owner after a rejected transfer")
			require.Equal(t, tc.role, t40LoadRole(t, db, "B", tc.tenantID),
				"a rejected target must keep its original role")
			require.EqualValues(t, 1, t40CountActiveOwners(t, db, tc.tenantID))
		})
	}
}

// E5：并发 A→C 与 B→D。两个 goroutine 必须限时结束；允许一方返回
// 角色已变化类错误，但不允许双方都失败或卡死；最终状态一致（每对
// from/to 要么完整交换，要么保持不变）。
func TestT40TransferOwnershipTxConcurrentHandovers(t *testing.T) {
	db, transfer := t40NewRepoDB(t)
	const tenantID uint64 = 40

	t40SeedTenant(t, db, tenantID)
	for _, id := range []string{"A", "B", "C", "D"} {
		t40SeedUser(t, db, id, true)
	}
	t40SeedMember(t, db, "A", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "B", tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "C", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)
	t40SeedMember(t, db, "D", tenantID, types.TenantRoleAdmin, types.TenantMemberStatusActive)

	type handover struct {
		from string
		to   string
	}
	type outcome struct {
		label string
		err   error
	}
	pairs := []handover{{from: "A", to: "C"}, {from: "B", to: "D"}}

	start := make(chan struct{})
	results := make(chan outcome, len(pairs))
	var wg sync.WaitGroup
	for _, pair := range pairs {
		wg.Add(1)
		go func(p handover) {
			defer wg.Done()
			<-start
			err := transfer.TransferOwnershipTx(
				context.Background(), tenantID, p.from, p.to)
			results <- outcome{label: p.from + "->" + p.to, err: err}
		}(pair)
	}
	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("concurrent handovers did not finish within 15s (deadlock/timeout)")
	}
	close(results)

	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			continue
		}
		require.Truef(t,
			errors.Is(result.err, ErrRoleChanged) ||
				errors.Is(result.err, ErrOwnerDemotionRequiresTransfer) ||
				errors.Is(result.err, gorm.ErrRecordNotFound),
			"%s failed with an unexpected error (not a role-changed error): %v",
			result.label, result.err)
	}
	require.GreaterOrEqual(t, successes, 1,
		"at least one concurrent handover must succeed; both blocked/failed indicates a deallock")

	for _, pair := range pairs {
		fromRole := t40LoadRole(t, db, pair.from, tenantID)
		toRole := t40LoadRole(t, db, pair.to, tenantID)
		swapped := fromRole == types.TenantRoleAdmin && toRole == types.TenantRoleOwner
		unchanged := fromRole == types.TenantRoleOwner && toRole == types.TenantRoleAdmin
		require.Truef(t, swapped || unchanged,
			"%s left an inconsistent final state: from=%s to=%s", pair.from, fromRole, toRole)
	}
	require.GreaterOrEqual(t, t40CountActiveOwners(t, db, tenantID), int64(1),
		"a workspace must never be left without an owner")
}
