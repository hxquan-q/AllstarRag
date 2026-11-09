package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// t31BlockingLatestBan 让 A 的解封在“读到旧 ban 记录”之后暂停，
// 以便在检查与写入之间插入 B 的新封禁，稳定复现 F-UNBAN 的竞态窗口。
type t31BlockingLatestBan struct {
	entry   *types.AuditLog
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *t31BlockingLatestBan) LatestBanAuditForTarget(
	ctx context.Context,
	targetUserID string,
) (*types.AuditLog, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.entry, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return b.entry, nil
	}
}

func t31CountAudit(t *testing.T, env *umTestEnv, tenantID uint64, action types.AuditAction) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.AuditLog{}).
		Where("tenant_id = ? AND action = ? AND outcome = ?",
			tenantID, action, types.AuditOutcomeSuccess).
		Count(&n).Error)
	return n
}

// 目标初始 active；A 的历史 ban 审计已存在（stub 返回它）。A 的解封停在
// 检查之后；此时 B 对同一目标发起新封禁并成功。无论执行顺序如何，最终
// 状态都必须是“封禁”，A 的旧解封不得把 B 的新封禁覆盖掉。
func TestT31ConcurrentBanDoesNotGetOverwrittenByStaleUnban(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedUser(t, env, "actor-b", nil)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedMembership(t, env, "actor-b", umTenantB, types.TenantRoleOwner)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)

	stale := &types.AuditLog{
		TenantID:     umTenantA,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now().Add(-time.Hour),
	}
	require.NoError(t, env.db.Create(stale).Error)

	stub := &t31BlockingLatestBan{
		entry:   stale,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	umSvc := env.svc.(*userManagementService)
	umSvc.WithLatestBanLookup(stub)

	unbanDone := make(chan error, 1)
	go func() {
		_, err := umSvc.UnbanUser(
			umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false),
			umTenantA, "target", "stale unban")
		unbanDone <- err
	}()

	entered := false
	select {
	case <-stub.entered:
		entered = true
	case <-time.After(2 * time.Second):
		// 实现若不再调用注入的预检查（例如已下沉到仓储事务），
		// 直接进入后面的并发封禁步骤，仍按最终状态断言。
	}

	_, err := umSvc.BanUser(
		umCallerCtx("actor-b", umTenantB, types.TenantRoleOwner, false),
		umTenantB, "target", "newer ban")
	require.NoError(t, err, "B 的有效封禁应成功")

	if entered {
		close(stub.release)
	}
	unbanErr := <-unbanDone

	user := umLoadUser(t, env, "target")
	require.False(t, user.IsActive,
		"newer ban must not be overwritten by a stale unban")
	if entered {
		// 旧来源的解封必须被拒绝；串行化后 A 应看到 B 的最新 ban。
		umRequireHTTPStatus(t, unbanErr, 403)
		require.Zero(t, t31CountAudit(t, env, umTenantA, types.AuditActionMemberUnbanned),
			"rejected unban must not write a success audit")
	}
}

func TestT31UnbanRejectsNonLatestTenantSerial(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedUser(t, env, "target", func(u *types.User) { u.IsActive = false })
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantB,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now(),
	}).Error)

	_, err := env.svc.UnbanUser(
		umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "wrong tenant")
	umRequireHTTPStatus(t, err, 403)
	require.False(t, umLoadUser(t, env, "target").IsActive)
	require.Zero(t, t31CountAudit(t, env, umTenantA, types.AuditActionMemberUnbanned))
}

func TestT31SystemAdminUnbanIgnoresLatestTenant(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", func(u *types.User) { u.IsActive = false })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantB,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now(),
	}).Error)

	user, err := env.svc.UnbanUser(
		umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true),
		umTenantA, "target", "platform unban")
	require.NoError(t, err)
	require.True(t, user.IsActive)
	require.EqualValues(t, 1, t31CountAudit(t, env, umTenantA, types.AuditActionMemberUnbanned))
}

func TestT31UnbanFailureZeroSideEffects(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedUser(t, env, "target", func(u *types.User) { u.IsActive = false })
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantB,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now(),
	}).Error)

	before := umLoadUser(t, env, "target")
	_, err := env.svc.UnbanUser(
		umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "denied")
	umRequireHTTPStatus(t, err, 403)

	after := umLoadUser(t, env, "target")
	require.False(t, after.IsActive)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt, "rejected unban must not touch the user row")
	require.Zero(t, t31CountAudit(t, env, umTenantA, types.AuditActionMemberUnbanned))
}
