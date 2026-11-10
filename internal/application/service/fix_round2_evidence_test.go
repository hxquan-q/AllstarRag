package service

import (
	"context"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// F3：改角色目标保护——系统管理员/跨空间超管/别处 owner 不能被普通 owner 改角色。
func TestValidateRoleChangeTargetProtectsPrivilegedTargets(t *testing.T) {
	env := newUMTestEnv(t)
	svc := NewTenantMemberService(env.memberRepo, nil, env.userRepo, nil).(interface {
		ValidateRoleChangeTarget(context.Context, uint64, string) error
	})

	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "privileged", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "privileged", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "cross", func(u *types.User) { u.CanAccessAllTenants = true })
	umSeedMembership(t, env, "cross", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "elsewhere-owner", nil)
	umSeedMembership(t, env, "elsewhere-owner", umTenantB, types.TenantRoleOwner)
	umSeedMembership(t, env, "elsewhere-owner", umTenantA, types.TenantRoleViewer)

	caller := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	require.ErrorIs(t, svc.ValidateRoleChangeTarget(caller, umTenantA, "privileged"), ErrTargetPrivileged)
	require.ErrorIs(t, svc.ValidateRoleChangeTarget(caller, umTenantA, "cross"), ErrTargetPrivileged)
	require.ErrorIs(t, svc.ValidateRoleChangeTarget(caller, umTenantA, "elsewhere-owner"), ErrTargetOwnsWorkspace)
	require.NoError(t, svc.ValidateRoleChangeTarget(caller, umTenantA, "actor"),
		"normal target must pass")

	admin := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	require.NoError(t, svc.ValidateRoleChangeTarget(admin, umTenantA, "privileged"),
		"system administrator may manage privileged targets")
}

// F18：封禁/解封/注销审计必须能被空间操作记录读接口读到（scope_type 口径一致）。
func TestStatusAuditsVisibleToOperationLogReader(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)

	_, err := env.svc.BanUser(ctx, umTenantA, "target", "evidence")
	require.NoError(t, err)
	_, err = env.svc.UnbanUser(ctx, umTenantA, "target", "evidence")
	require.NoError(t, err)

	// 注销审计走同一 emitStatusAudit 写入口；这里直接调用它，避免为完整
	// purge 事务额外迁移 sessions/invitations 等表（写侧口径一致即可）。
	svc := env.svc.(*userManagementService)
	svc.emitStatusAudit(ctx, umTenantA, &managedTarget{
		user:   &types.User{ID: "purged"},
		member: &types.TenantMember{UserID: "purged", TenantID: umTenantA, Role: types.TenantRoleViewer},
	}, types.AuditActionMemberPurged, "evidence")

	rows, err := env.audit.List(context.Background(), umTenantA, &interfaces.AuditLogQuery{
		UnscopedOnly: true,
		Limit:        100,
	})
	require.NoError(t, err)
	var banned, unbanned, purged bool
	for _, row := range rows {
		switch row.Action {
		case types.AuditActionMemberBanned:
			banned = true
		case types.AuditActionMemberUnbanned:
			unbanned = true
		case types.AuditActionMemberPurged:
			purged = true
		}
	}
	require.True(t, banned, "operation log reader must see rbac.member_banned")
	require.True(t, unbanned, "operation log reader must see rbac.member_unbanned")
	require.True(t, purged, "operation log reader must see rbac.member_purged")
}

// F29：墓碑用户不能被整行 UpdateUser 写回 PII。
func TestUpdateUserRejectsTombstonedRow(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "target", nil)
	require.NoError(t, env.userRepo.DeleteUser(context.Background(), "target"))

	stale, err := env.userRepo.GetUserByID(context.Background(), "target")
	require.Error(t, err)
	require.Nil(t, stale)

	// 模拟旧快照整行保存：直接构造带原 id 的 User。
	err = env.userRepo.UpdateUser(context.Background(), &types.User{
		ID:       "target",
		Username: "target",
		Email:    "target@example.com",
	})
	require.ErrorIs(t, err, apprepo.ErrUserNotFound,
		"tombstoned users must reject full-row updates")
}

// F30：不能把空间交接给被封禁账号。
func TestTransferOwnerRejectsBannedTarget(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "banned", func(u *types.User) { u.IsActive = false })
	umSeedMembership(t, env, "banned", umTenantA, types.TenantRoleViewer)

	svc := NewUserManagementService(env.userRepo, env.memberRepo, env.audit).(*userManagementService)
	svc.SetOwnerManagementDeps(nil, &transferMemberStub{memberships: map[string]*types.TenantMember{
		"actor":  {UserID: "actor", TenantID: umTenantA, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive},
		"banned": {UserID: "banned", TenantID: umTenantA, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
	}}, nil, nil)

	err := svc.TransferOwner(umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false), umTenantA, "actor", "banned")
	umRequireHTTPStatus(t, err, 400)
}

type transferMemberStub struct {
	interfaces.TenantMemberService
	memberships map[string]*types.TenantMember
}

func (s *transferMemberStub) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	m := s.memberships[userID]
	if m == nil || m.TenantID != tenantID {
		return nil, nil
	}
	return m, nil
}

func (s *transferMemberStub) UpdateRole(context.Context, string, uint64, types.TenantRole) error {
	return nil
}
