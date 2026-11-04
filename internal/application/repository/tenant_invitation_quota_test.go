package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件覆盖 T01 的注册配额仓储原子方法：
//   - ReserveRegistrationQuota：满额/过期/撤销/租户已删拒绝，并发不超卖；
//   - ReleaseRegistrationQuota：注册失败退回名额，且不会减成负数；
//   - ApplyShareLinkOptions：只覆盖非空字段，非 pending 行返回未找到。
// SQLite 只允许一个写者，这里把连接池限制为 1 并用 busy_timeout 排队，
// 真实的行锁并发在 Postgres 上验证（无测试库时如实跳过）。
// F14 起共享链接查询带租户存活 EXISTS 条件，因此夹具同时迁移
// tenants 表并插入 id=1 的存活租户（newQuotaInvitation 的 tenant_id）。

func newQuotaRepo(t *testing.T) (*tenantInvitationRepository, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.TenantInvitation{}, &types.Tenant{}))
	require.NoError(t, db.Create(&types.Tenant{ID: 1, Name: "quota-tenant"}).Error)
	return NewTenantInvitationRepository(db).(*tenantInvitationRepository), db
}

// newQuotaInvitation 造一条 pending 共享邀请行，默认 24 小时后过期。
func newQuotaInvitation(t *testing.T, db *gorm.DB, expireAt time.Time) *types.TenantInvitation {
	t.Helper()
	inv := &types.TenantInvitation{
		TenantID:  1,
		Role:      types.TenantRoleViewer,
		Status:    types.TenantInvitationStatusPending,
		Token:     uuid.NewString(),
		ExpiresAt: expireAt,
	}
	require.NoError(t, db.Create(inv).Error)
	return inv
}

func reloadQuotaInvitation(t *testing.T, db *gorm.DB, id uint64) *types.TenantInvitation {
	t.Helper()
	var inv types.TenantInvitation
	require.NoError(t, db.Where("id = ?", id).First(&inv).Error)
	return &inv
}

func TestReserveRegistrationQuotaUnlimited(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))

	for i := 0; i < 3; i++ {
		ok, err := repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
		require.NoError(t, err)
		require.True(t, ok, "不限名额时第 %d 次预占应成功", i+1)
	}
	require.Equal(t, 3, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount)
}

func TestReserveRegistrationQuotaStopsAtLimit(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))
	limit := 2
	require.NoError(t, db.Model(inv).Update("max_registrations", limit).Error)

	for i := 0; i < limit; i++ {
		ok, err := repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "达到上限后必须拒绝预占")
	require.Equal(t, limit, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount, "被拒绝的预占不能计数")
}

func TestReserveRegistrationQuotaRejectsExpiredAndRevoked(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()

	expired := newQuotaInvitation(t, db, time.Now().Add(-time.Hour))
	ok, err := repo.ReserveRegistrationQuota(ctx, expired.ID, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "过期链接不能预占")
	require.Zero(t, reloadQuotaInvitation(t, db, expired.ID).RegisteredCount)

	revoked := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))
	require.NoError(t, db.Model(revoked).Update("status", types.TenantInvitationStatusRevoked).Error)
	ok, err = repo.ReserveRegistrationQuota(ctx, revoked.ID, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "撤销链接不能预占")
	require.Zero(t, reloadQuotaInvitation(t, db, revoked.ID).RegisteredCount)
}

// TestReserveRegistrationQuotaRejectsDeletedTenant 覆盖 F14 仓储行为：
// 租户软删（DeleteTenant 的租户侧效果）后，其名下共享链接既不能再
// 预占注册名额，也不能被 GetActiveByToken 命中（lookup 与
// accept-by-token 共用该查询，统一按未命中 → 410 处理）。
func TestReserveRegistrationQuotaRejectsDeletedTenant(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))

	// 对照：租户存活时同一链接可以预占。
	ok, err := repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
	require.NoError(t, err)
	require.True(t, ok, "租户存活时应可预占")

	// 软删租户 1（等价 DeleteTenant 对 tenants 表的效果）。
	require.NoError(t, db.Delete(&types.Tenant{}, uint64(1)).Error)

	ok, err = repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "租户软删后不能再预占名额")
	require.Equal(t, 1, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount, "被拒预占不能计数")

	got, err := repo.GetActiveByToken(ctx, inv.Token)
	require.NoError(t, err)
	require.Nil(t, got, "租户软删后 GetActiveByToken 不应再命中该链接")
}

func TestReserveRegistrationQuotaConcurrentDoesNotOversell(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))
	limit := 5
	require.NoError(t, db.Model(inv).Update("max_registrations", limit).Error)

	const racers = 16
	var wg sync.WaitGroup
	var succeeded atomic.Int64
	errCh := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.ReserveRegistrationQuota(ctx, inv.ID, time.Now())
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				succeeded.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发预占出现错误：%v", err)
	}
	require.Equal(t, int64(limit), succeeded.Load(), "成功预占数必须等于上限")
	require.Equal(t, limit, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount, "计数不能超过上限")
}

func TestReleaseRegistrationQuotaNeverGoesNegative(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))
	require.NoError(t, db.Model(inv).Update("registered_count", 1).Error)

	require.NoError(t, repo.ReleaseRegistrationQuota(ctx, inv.ID))
	require.Zero(t, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount)

	require.NoError(t, repo.ReleaseRegistrationQuota(ctx, inv.ID))
	require.Zero(t, reloadQuotaInvitation(t, db, inv.ID).RegisteredCount, "计数为 0 时释放不能减成负数")
}

func TestApplyShareLinkOptionsOnlyWritesProvidedFields(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	originalExpiry := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	inv := newQuotaInvitation(t, db, originalExpiry)

	limit := 10
	require.NoError(t, repo.ApplyShareLinkOptions(ctx, inv.ID, &limit, nil))
	afterLimit := reloadQuotaInvitation(t, db, inv.ID)
	require.NotNil(t, afterLimit.MaxRegistrations)
	require.Equal(t, limit, *afterLimit.MaxRegistrations)
	require.True(t, afterLimit.ExpiresAt.Equal(originalExpiry), "未提供有效期时不应覆盖原值")

	newExpiry := originalExpiry.Add(23 * 24 * time.Hour)
	require.NoError(t, repo.ApplyShareLinkOptions(ctx, inv.ID, nil, &newExpiry))
	afterExpiry := reloadQuotaInvitation(t, db, inv.ID)
	require.NotNil(t, afterExpiry.MaxRegistrations)
	require.Equal(t, limit, *afterExpiry.MaxRegistrations, "未提供人数时不应覆盖原值")
	require.True(t, afterExpiry.ExpiresAt.Equal(newExpiry))
}

func TestApplyShareLinkOptionsRejectsNonPendingRow(t *testing.T) {
	repo, db := newQuotaRepo(t)
	ctx := context.Background()
	inv := newQuotaInvitation(t, db, time.Now().Add(24*time.Hour))
	require.NoError(t, db.Model(inv).Update("status", types.TenantInvitationStatusRevoked).Error)

	limit := 3
	err := repo.ApplyShareLinkOptions(ctx, inv.ID, &limit, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "已撤销的链接不能再补写选项")
}
