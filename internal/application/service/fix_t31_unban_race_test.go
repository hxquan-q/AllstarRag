package service

import (
	"sync"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// F36：用仓储提供的 BanUserTx 事务内暂停点，构造确定性的“封禁与解封并发”
// 场景，而不是依赖生产路径永远进不去的阻塞桩。
//
// 复现目标（F35 残留竞态）：旧的实现把 is_active=false 放在 SetUserActive
// 独立事务里提交，ban 审计在事务提交之后才写。并发的 UnbanUserTx 可以在
// “状态已提交、审计未写入”之间读到旧 ban，从而让旧来源的解封覆盖新的封禁。
//
// 新实现把「锁 users 行 → 状态 → 吊销 token → 写 ban 审计」放进同一事务，
// 并在写审计前提供测试暂停点 BanTxPauseBeforeAudit。本用例在暂停点停住
// ban 事务，验证：
//  1. 暂停点确实被触发（若实现退回旧路径，钩子永不触发，用例在等待处失败）；
//  2. 暂停期间未提交的 ban 审计对其他连接不可见；
//  3. 另一路 unban 被 users 行锁挡住，不会读到旧 ban 后抢先解封；
//  4. ban 提交后 unban 看到最新已提交 ban（发生在租户 B），按口径 403，
//     终态保持封禁，租户 A 不产生成功的 unban 审计。

// t31CountAudit 统计指定租户下某动作的成功审计条数。
func t31CountAudit(t *testing.T, env *umTestEnv, tenantID uint64, action types.AuditAction) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.AuditLog{}).
		Where("tenant_id = ? AND action = ? AND outcome = ?",
			tenantID, action, types.AuditOutcomeSuccess).
		Count(&n).Error)
	return n
}

func TestT31ConcurrentBanDoesNotGetOverwrittenByStaleUnban(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedUser(t, env, "actor-b", nil)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedMembership(t, env, "actor-b", umTenantB, types.TenantRoleOwner)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)

	// 租户 A 的历史 ban 审计（较早，id 更小）。若解封在 ban 事务提交前
	// 读到它，就会误判“最近一次 ban 在本租户”并错误放行。
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantA,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now().Add(-time.Hour),
	}).Error)

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	hook := func(stage string) {
		if stage != apprepo.BanTxPauseBeforeAudit {
			return
		}
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			// 兜底防止用例自身永久挂起；正常路径由主 goroutine 释放。
		}
	}
	banCtx := apprepo.WithBanTxPauseHook(
		umCallerCtx("actor-b", umTenantB, types.TenantRoleOwner, false), hook)

	banDone := make(chan error, 1)
	go func() {
		_, err := env.svc.BanUser(banCtx, umTenantB, "target", "newer ban")
		banDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("BanUserTx 的 before_ban_audit 暂停点未被触发：封禁没有走" +
			"「状态变更 + ban 审计同事务」的 BanUserTx，F35 竞态会复发" +
			"（F36 要求本用例在实现退回旧路径时失败）")
	}

	// 暂停点位于审计写入前且事务尚未提交：其他连接看不到这条 ban 审计。
	require.Zero(t, t31CountAudit(t, env, umTenantB, types.AuditActionMemberBanned),
		"未提交的 ban 审计不应对其他连接可见")

	unbanDone := make(chan error, 1)
	go func() {
		_, err := env.svc.UnbanUser(
			umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false),
			umTenantA, "target", "stale unban")
		unbanDone <- err
	}()

	// ban 事务持有 users 行（SQLite 为库级写锁）时，解封必须被挡住，
	// 不能先读到旧 ban 再写入 active=true。
	select {
	case err := <-unbanDone:
		close(release)
		<-banDone
		t.Fatalf("ban 事务暂停期间解封不应返回，实际已返回 err=%v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-banDone, "B 的有效封禁应在暂停点释放后提交")

	select {
	case err := <-unbanDone:
		// ban 已提交且最近一次 ban 属于租户 B，租户 A 的普通 owner 不得解封。
		umRequireHTTPStatus(t, err, 403)
	case <-time.After(5 * time.Second):
		t.Fatal("ban 提交后解封仍未返回，疑似死锁")
	}

	require.False(t, umLoadUser(t, env, "target").IsActive,
		"newer ban must not be overwritten by a stale unban")
	require.EqualValues(t, 1, t31CountAudit(t, env, umTenantB, types.AuditActionMemberBanned),
		"ban 事务提交后应恰好有一条租户 B 的 ban 审计")
	require.Zero(t, t31CountAudit(t, env, umTenantA, types.AuditActionMemberUnbanned),
		"被拒绝的解封不得写成功审计")
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
