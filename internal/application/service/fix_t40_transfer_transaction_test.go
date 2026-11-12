package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T40 TDD 红阶段（service 层）：交接必须走仓储单事务，服务层不再做
// “先提升、再降级、失败再补偿”的两步序列。
//
// 本文件用可控仓储替身做 seam：替身的 TransferOwnershipTx 可注入
// 交错结果/失败点；同时记录 memberService 的角色写调用。旧实现会调用
// memberService.UpdateRoleGuarded 并在降级失败时补偿降级目标，因此当前
// 预期红；新实现只调用一次仓储事务并在成功后写审计。

type t40OwnershipTransferTx interface {
	TransferOwnershipTx(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error
}

// t40TransferRepo 包装真实成员仓储，暴露可注入的 TransferOwnershipTx。
// hook 为 nil 时委托给真实仓储（实现缺失时返回明确错误，便于红阶段定位）。
type t40TransferRepo struct {
	interfaces.TenantMemberRepository

	mu    sync.Mutex
	calls int
	hook  func(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error
}

func (r *t40TransferRepo) TransferOwnershipTx(
	ctx context.Context, tenantID uint64, fromUserID, toUserID string,
) error {
	r.mu.Lock()
	r.calls++
	hook := r.hook
	r.mu.Unlock()

	if hook != nil {
		return hook(ctx, tenantID, fromUserID, toUserID)
	}
	impl, ok := r.TenantMemberRepository.(t40OwnershipTransferTx)
	if !ok {
		return errors.New("underlying repository does not implement TransferOwnershipTx")
	}
	return impl.TransferOwnershipTx(ctx, tenantID, fromUserID, toUserID)
}

func (r *t40TransferRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *t40TransferRepo) setHook(
	hook func(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook = hook
}

type t40RoleUpdate struct {
	UserID             string
	TenantID           uint64
	Expected           types.TenantRole
	New                types.TenantRole
	AllowOwnerDemotion bool
}

// t40RecordingMemberService 记录并可按条件拒绝角色写调用；未命中 hook
// 时委托真实成员服务（真实仓储的 CAS 逻辑）。
type t40RecordingMemberService struct {
	interfaces.TenantMemberService

	mu       sync.Mutex
	calls    []t40RoleUpdate
	failWhen func(t40RoleUpdate) error
}

func (s *t40RecordingMemberService) UpdateRoleGuarded(
	ctx context.Context,
	userID string,
	tenantID uint64,
	expectedRole, newRole types.TenantRole,
	allowOwnerDemotion bool,
) error {
	call := t40RoleUpdate{
		UserID:             userID,
		TenantID:           tenantID,
		Expected:           expectedRole,
		New:                newRole,
		AllowOwnerDemotion: allowOwnerDemotion,
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	failWhen := s.failWhen
	s.mu.Unlock()

	if failWhen != nil {
		if err := failWhen(call); err != nil {
			return err
		}
	}
	guarded, ok := s.TenantMemberService.(interface {
		UpdateRoleGuarded(
			context.Context, string, uint64, types.TenantRole, types.TenantRole, bool,
		) error
	})
	if !ok {
		return s.TenantMemberService.UpdateRole(ctx, userID, tenantID, newRole)
	}
	return guarded.UpdateRoleGuarded(ctx, userID, tenantID, expectedRole, newRole, allowOwnerDemotion)
}

func (s *t40RecordingMemberService) roleUpdates() []t40RoleUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]t40RoleUpdate(nil), s.calls...)
}

type t40Env struct {
	*omOwnerEnv
	transferRepo *t40TransferRepo
	memberSvc    *t40RecordingMemberService
	svc          interfaces.UserManagementService
}

// newT40Env 在 T04 真实 sqlite 夹具基础上替换成员仓储与成员服务：
// 仓储替身提供 TransferOwnershipTx seam，成员服务替身记录角色写。
func newT40Env(t *testing.T) *t40Env {
	t.Helper()
	base := newOMOwnerEnv(t)

	repo := &t40TransferRepo{TenantMemberRepository: base.memberRepo}
	memberSvc := &t40RecordingMemberService{TenantMemberService: base.memberSvc}
	svc := NewUserManagementService(base.userRepo, repo, base.audit)
	setter, ok := svc.(omOwnerDepsSetter)
	require.True(t, ok, "UserManagementService must expose SetOwnerManagementDeps")
	setter.SetOwnerManagementDeps(base.tenantSvc, memberSvc, base.inviteSvc, base.userSvc)

	return &t40Env{
		omOwnerEnv:   base,
		transferRepo: repo,
		memberSvc:    memberSvc,
		svc:          svc,
	}
}

func t40SeedInactiveUser(t *testing.T, env *t40Env, userID string) {
	t.Helper()
	omOwnerSeedUser(t, env.omOwnerEnv, userID, nil)
	// users.is_active 带 gorm:"default:true"，必须显式关闭。
	_, err := env.userRepo.SetUserActive(context.Background(), userID, "t40-fixture", false)
	require.NoError(t, err)
}

// E6：正常交接由仓储单事务完成；服务层不再自行写角色；成功后写一条审计。
func TestT40TransferOwnerUsesTransaction(t *testing.T) {
	env := newT40Env(t)
	tenant := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
	omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env.omOwnerEnv, "successor", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "successor", tenant.ID, types.TenantRoleAdmin)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.TransferOwner(ctx, tenant.ID, "owner1", "successor"))

	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "successor", tenant.ID).Role)
	require.Equal(t, types.TenantRoleAdmin,
		omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenant.ID).Role)

	require.Equal(t, 1, env.transferRepo.callCount(),
		"TransferOwner must hand over through the repository transaction exactly once")
	require.Empty(t, env.memberSvc.roleUpdates(),
		"TransferOwner must not write roles through memberService (no two-step path)")

	audits := omOwnerAuditsByAction(t, env.omOwnerEnv, types.AuditActionOwnerTransferred)
	require.Len(t, audits, 1, "a successful handover writes exactly one rbac.owner_transferred audit")
}

// E7：补偿撤销回归。构造交错：
//   - A→B 到达事务时，D→B 已成功（B 已是 owner）；
//   - A→C 已成功（A 已不再是 owner）；
//   - 仓储事务复核失败、整体回滚。
//
// 旧实现会先提升 B，再因 A 降级失败而用“目标当前角色”补偿，把 B 从
// owner 降回 admin，撤销 D→B 的成功交接。新实现必须只调用一次仓储
// 事务、不做任何服务层角色写，B 保持 owner。
func TestT40TransferOwnerFailureDoesNotUndoAnotherTransfer(t *testing.T) {
	env := newT40Env(t)
	tenant := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
	for _, id := range []string{"A", "B", "C", "D"} {
		omOwnerSeedUser(t, env.omOwnerEnv, id, nil)
	}
	omOwnerSeedMembership(t, env.omOwnerEnv, "A", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedMembership(t, env.omOwnerEnv, "B", tenant.ID, types.TenantRoleAdmin)
	omOwnerSeedMembership(t, env.omOwnerEnv, "C", tenant.ID, types.TenantRoleAdmin)
	omOwnerSeedMembership(t, env.omOwnerEnv, "D", tenant.ID, types.TenantRoleAdmin)

	// seam：A→B 进入事务时，并发交接已经改变了角色。
	env.transferRepo.setHook(func(
		ctx context.Context, tenantID uint64, fromUserID, toUserID string,
	) error {
		require.Equal(t, tenant.ID, tenantID)
		require.Equal(t, "A", fromUserID)
		require.Equal(t, "B", toUserID)
		require.NoError(t, env.db.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ?", tenant.ID, "B").
			Update("role", types.TenantRoleOwner).Error)
		require.NoError(t, env.db.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ?", tenant.ID, "C").
			Update("role", types.TenantRoleOwner).Error)
		require.NoError(t, env.db.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ?", tenant.ID, "A").
			Update("role", types.TenantRoleAdmin).Error)
		return apprepo.ErrRoleChanged
	})
	// 旧实现路径下，A 的降级失败；随后补偿会尝试把 B 降回。
	env.memberSvc.failWhen = func(call t40RoleUpdate) error {
		if call.UserID == "A" && call.Expected == types.TenantRoleOwner &&
			call.New == types.TenantRoleAdmin {
			return ErrMembershipChanged
		}
		return nil
	}

	ctx := umCallerCtx("A", tenant.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenant.ID, "A", "B")
	require.Error(t, err, "the handover must fail because A is no longer the owner")

	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "B", tenant.ID).Role,
		"a failed A->B must never demote B after another transfer already promoted it")
	require.Equal(t, types.TenantRoleAdmin,
		omOwnerLoadMember(t, env.omOwnerEnv, "A", tenant.ID).Role)

	require.Equal(t, 1, env.transferRepo.callCount(),
		"the service must attempt exactly one repository transaction")
	require.Empty(t, env.memberSvc.roleUpdates(),
		"the old promote/demote/compensate sequence must be removed")
	require.Empty(t, omOwnerAuditsByAction(t, env.omOwnerEnv, types.AuditActionOwnerTransferred),
		"a failed handover must not write rbac.owner_transferred")
}

// E8：既有保护不回归。三类拒绝都必须在触达仓储事务前完成，且零角色变化。
func TestT40TransferOwnerPreconditionsStillGuarded(t *testing.T) {
	t.Run("banned_target_400", func(t *testing.T) {
		env := newT40Env(t)
		tenant := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
		omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenant.ID, types.TenantRoleOwner)
		t40SeedInactiveUser(t, env, "banned")
		omOwnerSeedMembership(t, env.omOwnerEnv, "banned", tenant.ID, types.TenantRoleViewer)

		ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
		err := env.svc.TransferOwner(ctx, tenant.ID, "owner1", "banned")
		omOwnerRequireAppStatus(t, err, 400)

		require.Equal(t, types.TenantRoleOwner,
			omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenant.ID).Role)
		require.Equal(t, types.TenantRoleViewer,
			omOwnerLoadMember(t, env.omOwnerEnv, "banned", tenant.ID).Role)
		require.Zero(t, env.transferRepo.callCount(),
			"a banned target must be rejected before the transaction")
	})

	t.Run("non_owner_source_404", func(t *testing.T) {
		env := newT40Env(t)
		tenant := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
		omOwnerSeedUser(t, env.omOwnerEnv, "sysadmin", func(u *types.User) {
			u.IsSystemAdmin = true
		})
		omOwnerSeedUser(t, env.omOwnerEnv, "not-owner", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "not-owner", tenant.ID, types.TenantRoleAdmin)
		omOwnerSeedUser(t, env.omOwnerEnv, "target", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenant.ID, types.TenantRoleAdmin)

		ctx := umCallerCtx("sysadmin", tenant.ID, types.TenantRoleViewer, true)
		err := env.svc.TransferOwner(ctx, tenant.ID, "not-owner", "target")
		omOwnerRequireAppStatus(t, err, 404)

		require.Equal(t, types.TenantRoleAdmin,
			omOwnerLoadMember(t, env.omOwnerEnv, "not-owner", tenant.ID).Role)
		require.Equal(t, types.TenantRoleAdmin,
			omOwnerLoadMember(t, env.omOwnerEnv, "target", tenant.ID).Role)
		require.Zero(t, env.transferRepo.callCount(),
			"a non-owner source must be rejected before the transaction")
	})

	t.Run("target_not_member_404", func(t *testing.T) {
		env := newT40Env(t)
		tenant := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
		omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenant.ID, types.TenantRoleOwner)
		omOwnerSeedUser(t, env.omOwnerEnv, "outsider", nil)

		ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
		err := env.svc.TransferOwner(ctx, tenant.ID, "owner1", "outsider")
		omOwnerRequireAppStatus(t, err, 404)

		require.Equal(t, types.TenantRoleOwner,
			omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenant.ID).Role)
		require.Zero(t, env.transferRepo.callCount(),
			"a target without membership must be rejected before the transaction")
	})
}
