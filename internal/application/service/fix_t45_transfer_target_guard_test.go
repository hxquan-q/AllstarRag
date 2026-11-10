package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T45 红阶段（service 层）：T40 把交接改为仓储单事务后，旧的
// ValidateRoleChangeTarget 目标保护没有接回——普通 owner 可以把空间
// 交给系统管理员 / 跨空间超管 / 在别处持有 owner 的账号。
//
// 本文件要求：调用者非系统管理员时，交接必须在进入仓储事务之前被
// ErrTargetPrivileged / ErrTargetOwnsWorkspace 拒绝（与改角色路径同一套
// 哨兵），且双方角色零变化、审计零写入；系统管理员发起交接不受限。
//
// 预期红：当前 TransferOwner 对上述目标不做判定、直接进事务并返回 nil。

// t45AuditCount 统计全部审计行数，用于证明拒绝路径零审计写入。
func t45AuditCount(t *testing.T, env *t40Env) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.AuditLog{}).Count(&n).Error)
	return n
}

// T45-1：普通 owner 交接给「本空间普通成员 + B 空间 active owner」。
// 期望 ErrTargetOwnsWorkspace；from/to 在两个空间的角色零变化；
// 仓储事务零调用；审计零写入。
func TestT45TransferOwnerRejectsTargetOwningAnotherWorkspace(t *testing.T) {
	env := newT40Env(t)
	tenantA := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
	tenantB := omOwnerSeedTenant(t, env.omOwnerEnv, "B")
	omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenantA.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env.omOwnerEnv, "target", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantA.ID, types.TenantRoleAdmin)
	omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantB.ID, types.TenantRoleOwner)

	auditsBefore := t45AuditCount(t, env)
	ctx := umCallerCtx("owner1", tenantA.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenantA.ID, "owner1", "target")
	require.ErrorIs(t, err, ErrTargetOwnsWorkspace,
		"普通 owner 不能把空间交给在别的空间持有 owner 的账号")

	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenantA.ID).Role)
	require.Equal(t, types.TenantRoleAdmin,
		omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantA.ID).Role)
	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantB.ID).Role)
	require.Zero(t, env.transferRepo.callCount(),
		"目标保护必须在仓储事务之前拒绝")
	require.Equal(t, auditsBefore, t45AuditCount(t, env),
		"被拒绝的交接不得写任何审计")
}

// T45-2：普通 owner 交接给 is_system_admin=true 的本空间成员。
// 期望 ErrTargetPrivileged；零变化、零事务、零审计。
func TestT45TransferOwnerRejectsSystemAdminTarget(t *testing.T) {
	env := newT40Env(t)
	tenantA := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
	omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenantA.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env.omOwnerEnv, "target", func(u *types.User) {
		u.IsSystemAdmin = true
	})
	omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantA.ID, types.TenantRoleViewer)

	auditsBefore := t45AuditCount(t, env)
	ctx := umCallerCtx("owner1", tenantA.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenantA.ID, "owner1", "target")
	require.ErrorIs(t, err, ErrTargetPrivileged,
		"普通 owner 不能把空间交给系统管理员账号")

	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenantA.ID).Role)
	require.Equal(t, types.TenantRoleViewer,
		omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantA.ID).Role)
	require.Zero(t, env.transferRepo.callCount())
	require.Equal(t, auditsBefore, t45AuditCount(t, env))
}

// T45-3：普通 owner 交接给 can_access_all_tenants=true 的目标。
// 期望 ErrTargetPrivileged；零变化、零事务、零审计。
func TestT45TransferOwnerRejectsCrossWorkspaceSuperuserTarget(t *testing.T) {
	env := newT40Env(t)
	tenantA := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
	omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
	omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenantA.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env.omOwnerEnv, "target", func(u *types.User) {
		u.CanAccessAllTenants = true
	})
	omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantA.ID, types.TenantRoleAdmin)

	auditsBefore := t45AuditCount(t, env)
	ctx := umCallerCtx("owner1", tenantA.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenantA.ID, "owner1", "target")
	require.ErrorIs(t, err, ErrTargetPrivileged,
		"普通 owner 不能把空间交给跨空间超管账号")

	require.Equal(t, types.TenantRoleOwner,
		omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenantA.ID).Role)
	require.Equal(t, types.TenantRoleAdmin,
		omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantA.ID).Role)
	require.Zero(t, env.transferRepo.callCount())
	require.Equal(t, auditsBefore, t45AuditCount(t, env))
}

// T45-4：系统管理员发起交接不受目标保护限制（正向基线，预期绿）。
// ②实现时不得把系统管理员路径一起挡掉。
func TestT45TransferOwnerSystemAdminCallerUnrestricted(t *testing.T) {
	t.Run("to_owner_of_another_workspace", func(t *testing.T) {
		env := newT40Env(t)
		tenantA := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
		tenantB := omOwnerSeedTenant(t, env.omOwnerEnv, "B")
		omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenantA.ID, types.TenantRoleOwner)
		omOwnerSeedUser(t, env.omOwnerEnv, "target", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantA.ID, types.TenantRoleAdmin)
		omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantB.ID, types.TenantRoleOwner)

		ctx := umCallerCtx("sysadmin", tenantA.ID, types.TenantRoleViewer, true)
		require.NoError(t, env.svc.TransferOwner(ctx, tenantA.ID, "owner1", "target"))

		require.Equal(t, types.TenantRoleOwner,
			omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantA.ID).Role)
		require.Equal(t, types.TenantRoleAdmin,
			omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenantA.ID).Role)
		require.Equal(t, types.TenantRoleOwner,
			omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantB.ID).Role,
			"B 空间的 owner 身分不受 A 空间交接影响")
		require.Equal(t, 1, env.transferRepo.callCount())
		require.Len(t, omOwnerAuditsByAction(t, env.omOwnerEnv, types.AuditActionOwnerTransferred),
			1, "系统管理员交接成功后写一条 rbac.owner_transferred")
	})

	t.Run("to_system_admin_account", func(t *testing.T) {
		env := newT40Env(t)
		tenantA := omOwnerSeedTenant(t, env.omOwnerEnv, "A")
		omOwnerSeedUser(t, env.omOwnerEnv, "owner1", nil)
		omOwnerSeedMembership(t, env.omOwnerEnv, "owner1", tenantA.ID, types.TenantRoleOwner)
		omOwnerSeedUser(t, env.omOwnerEnv, "target", func(u *types.User) {
			u.IsSystemAdmin = true
		})
		omOwnerSeedMembership(t, env.omOwnerEnv, "target", tenantA.ID, types.TenantRoleViewer)

		ctx := umCallerCtx("sysadmin", tenantA.ID, types.TenantRoleViewer, true)
		require.NoError(t, env.svc.TransferOwner(ctx, tenantA.ID, "owner1", "target"))

		require.Equal(t, types.TenantRoleOwner,
			omOwnerLoadMember(t, env.omOwnerEnv, "target", tenantA.ID).Role)
		require.Equal(t, types.TenantRoleAdmin,
			omOwnerLoadMember(t, env.omOwnerEnv, "owner1", tenantA.ID).Role)
		require.Equal(t, 1, env.transferRepo.callCount())
		require.Len(t, omOwnerAuditsByAction(t, env.omOwnerEnv, types.AuditActionOwnerTransferred), 1)
	})
}
