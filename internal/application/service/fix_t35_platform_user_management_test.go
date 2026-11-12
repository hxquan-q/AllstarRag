package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T35 TDD 红阶段：平台级封禁 / 解封 / 注销的服务层契约。
//
// 假定契约（详见 05-tests §54）：
//   - interfaces.UserManagementService 新增
//     PlatformBanUser(ctx, targetUserID, reason) (*types.User, error)
//     PlatformUnbanUser(ctx, targetUserID, reason) (*types.User, error)
//     PlatformPurgeUser(ctx, targetUserID, confirmEmail, reason) error
//   - 平台方法不要求目标与调用者当前工作区存在成员关系，直接按 users.id
//     定位账号；
//   - 仅系统管理员可用（service 层再证一次，C4），违规 403；
//   - 操作自己 403；封禁系统管理员时复用 last active system admin 保护；
//   - purge 仍要求目标先完成 owner 交接、先撤销系统管理员 / 跨空间特权；
//   - 审计写 tenant_id=0（平台作用域），可通过系统审计读接口查到，
//     不写调用者当前空间的操作记录。

func t35UserDeleted(t *testing.T, env *umTestEnv, userID string) bool {
	t.Helper()
	var row types.User
	err := env.db.Unscoped().Where("id = ?", userID).First(&row).Error
	require.NoError(t, err, "user row must still exist as a tombstone")
	return row.DeletedAt.Valid
}

func t35SystemAdminCtx(userID string) context.Context {
	// 调用者可在任意工作区上下文执行平台操作；这里故意选一个目标没有
	// 成员关系的租户，证明平台接口不依赖成员关系。
	return umCallerCtx(userID, umTenantA, types.TenantRoleViewer, true)
}

// 平台封禁在目标无任何成员关系、且调用者当前工作区也不是目标空间时
// 仍然成功：账号级 is_active=false、token 全吊销。
func TestT35PlatformBanWorksWithoutMembership(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	// target 不属于 umTenantA，也没有任何其它成员关系。
	umSeedUser(t, env, "target", nil)
	umSeedTokens(t, env, "target", 2)

	ctx := t35SystemAdminCtx("sysadmin")
	user, err := env.svc.PlatformBanUser(ctx, "target", "platform policy violation")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.False(t, user.IsActive)

	require.False(t, umLoadUser(t, env, "target").IsActive,
		"platform ban must persist is_active=false without a membership")
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"),
		"platform ban must revoke every outstanding token")

	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditActionMemberBanned),
		"platform action must not be filed under the caller's workspace")

	rows := umAuditRows(t, env, 0, types.AuditActionMemberBanned)
	require.Len(t, rows, 1, "platform ban must write a system-scope audit row")
	require.Equal(t, "sysadmin", rows[0].ActorUserID)
	// F40：平台审计口径固定 system_admin，不取调用者租户角色（无角色时会
	// 落成 viewer），与既有 tenant_id=0 系统审计一致。
	require.Equal(t, "system_admin", rows[0].ActorRole)
	require.Equal(t, "target", rows[0].TargetUserID)
	require.Equal(t, types.AuditOutcomeSuccess, rows[0].Outcome)

	// 审计必须可查：系统审计读接口用 List(ctx, tenant_id=0, query)。
	entries, err := env.audit.List(context.Background(), 0, &interfaces.AuditLogQuery{
		Action: types.AuditActionMemberBanned,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1, "platform audit must be queryable from the system feed")
	require.Equal(t, "target", entries[0].TargetUserID)
}

// 非系统管理员即使在本空间是 owner，也不能调用平台账号级方法。
func TestT35PlatformMethodsRejectNonSystemAdmin(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner", nil)
	umSeedMembership(t, env, "owner", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)

	ctx := umCallerCtx("owner", umTenantA, types.TenantRoleOwner, false)

	_, err := env.svc.PlatformBanUser(ctx, "target", "nope")
	umRequireHTTPStatus(t, err, 403)
	_, err = env.svc.PlatformUnbanUser(ctx, "target", "nope")
	umRequireHTTPStatus(t, err, 403)
	err = env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "nope")
	umRequireHTTPStatus(t, err, 403)

	require.True(t, umLoadUser(t, env, "target").IsActive,
		"rejected platform operations must leave the target untouched")
	require.False(t, t35UserDeleted(t, env, "target"))
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberBanned))
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged))
}

// 任何人都不能通过平台接口封禁 / 注销自己。
func TestT35PlatformRejectsSelfTarget(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := t35SystemAdminCtx("sysadmin")
	_, err := env.svc.PlatformBanUser(ctx, "sysadmin", "self")
	umRequireHTTPStatus(t, err, 403)
	err = env.svc.PlatformPurgeUser(ctx, "sysadmin", "sysadmin@example.com", "self")
	umRequireHTTPStatus(t, err, 403)

	require.True(t, umLoadUser(t, env, "sysadmin").IsActive)
	require.False(t, t35UserDeleted(t, env, "sysadmin"))
}

// 平台封禁复用「最后一名活跃系统管理员」保护。
func TestT35PlatformBanProtectsLastActiveSystemAdmin(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "target", func(u *types.User) { u.IsSystemAdmin = true })
	// actor 带系统管理员上下文，但库中不是管理员：目标就是最后一名活跃管理员。
	ctx := t35SystemAdminCtx("actor")

	_, err := env.svc.PlatformBanUser(ctx, "target", "should be blocked")
	umRequireHTTPStatus(t, err, 403)

	require.True(t, umLoadUser(t, env, "target").IsActive,
		"the last active system administrator must stay active")
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberBanned))
}

// 平台解封不受「最近一次 ban 必须在本租户」限制：目标在别的空间被封、
// 调用者没有该空间成员关系时仍可解封。
func TestT35PlatformUnbanBypassesLatestBanTenant(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", func(u *types.User) { u.IsActive = false })
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantB,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
	}).Error)

	ctx := t35SystemAdminCtx("sysadmin")
	user, err := env.svc.PlatformUnbanUser(ctx, "target", "platform appeal")
	require.NoError(t, err)
	require.True(t, user.IsActive)
	require.True(t, umLoadUser(t, env, "target").IsActive)

	unbanRows := umAuditRows(t, env, 0, types.AuditActionMemberUnbanned)
	require.Len(t, unbanRows, 1, "platform unban must write a system-scope audit row")
	// F40：平台 unban 审计同样固定 system_admin。
	require.Equal(t, "system_admin", unbanRows[0].ActorRole)
}

// purge 跨空间 / 无成员关系账号允许；owner 目标必须先交接。
func TestT35PlatformPurgeRequiresOwnerHandover(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleOwner)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)

	ctx := t35SystemAdminCtx("sysadmin")
	err := env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr")
	umRequireHTTPStatus(t, err, 403)
	require.False(t, t35UserDeleted(t, env, "target"),
		"purge must be rejected while the target still owns a workspace")
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged))

	// 完成交接后（角色不再是 owner）平台注销成功。
	require.NoError(t, env.memberRepo.UpdateRole(
		context.Background(), "target", umTenantA, types.TenantRoleAdmin))
	err = env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr")
	require.NoError(t, err)
	require.True(t, t35UserDeleted(t, env, "target"),
		"platform purge must tombstone the account after handover")
	purgeRows := umAuditRows(t, env, 0, types.AuditActionMemberPurged)
	require.Len(t, purgeRows, 1)
	// F40：平台 purge 审计同样固定 system_admin。
	require.Equal(t, "system_admin", purgeRows[0].ActorRole)
}

// 平台注销完全没有成员关系的普通账号也允许（全平台视图里的无空间账号）。
func TestT35PlatformPurgeWorksWithoutMembership(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil) // 无任何 tenant_members 行

	ctx := t35SystemAdminCtx("sysadmin")
	require.NoError(t, env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr"))
	require.True(t, t35UserDeleted(t, env, "target"),
		"platform purge must work for accounts without memberships")
	require.Len(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged), 1)
}

// 平台注销跨空间的普通账号允许。
func TestT35PlatformPurgeAllowsCrossSpaceTarget(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleContributor)

	ctx := t35SystemAdminCtx("sysadmin")
	require.NoError(t, env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr"))
	require.True(t, t35UserDeleted(t, env, "target"))
	require.Len(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged), 1)
}

// 系统管理员 / 跨空间超管目标必须先撤权；撤权后平台注销成功。
func TestT35PlatformPurgeRequiresRevokingPrivileges(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := t35SystemAdminCtx("sysadmin")
	err := env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr")
	umRequireHTTPStatus(t, err, 403)
	require.False(t, t35UserDeleted(t, env, "target"))

	require.NoError(t, env.db.Model(&types.User{}).
		Where("id = ?", "target").Update("is_system_admin", false).Error)
	require.NoError(t, env.svc.PlatformPurgeUser(ctx, "target", "target@example.com", "gdpr"))
	require.True(t, t35UserDeleted(t, env, "target"))
}

// F41：共享 PurgeUserTx 恢复 fail-closed 后，缺 sessions 表不允许“静默跳过”
// 并返回成功——整单事务必须回滚，账号不得被墓碑化、不得写 purge 审计。
func TestT35PlatformPurgeFailsClosedWhenSessionsTableMissing(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil)
	require.NoError(t, env.db.Create(&types.Session{
		TenantID: umTenantA, UserID: "target", Title: "session before purge",
	}).Error)
	require.NoError(t, env.db.Migrator().DropTable("sessions"))

	err := env.svc.PlatformPurgeUser(t35SystemAdminCtx("sysadmin"),
		"target", "target@example.com", "gdpr")
	require.Error(t, err, "sessions 缺表必须让整单 purge 事务失败（fail-closed）")
	require.False(t, t35UserDeleted(t, env, "target"),
		"purge 失败时账号不得被墓碑化")
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged),
		"purge 失败时不得写成功审计")
}

// F41：个人数据表同样 fail-closed。用 raw SQL 建的 user_kb_pins 缺表时，
// 之前已执行的删除（如 sessions）必须一并回滚，不能留下“PII 删了一半”。
func TestT35PlatformPurgeFailsClosedWhenPersonalDataTableMissing(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil)
	require.NoError(t, env.db.Create(&types.Session{
		TenantID: umTenantA, UserID: "target", Title: "session before purge",
	}).Error)
	require.NoError(t, env.db.Migrator().DropTable("user_kb_pins"))

	err := env.svc.PlatformPurgeUser(t35SystemAdminCtx("sysadmin"),
		"target", "target@example.com", "gdpr")
	require.Error(t, err, "个人数据表缺表必须让整单 purge 事务失败（fail-closed）")
	require.False(t, t35UserDeleted(t, env, "target"))
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged))

	var sessions int64
	require.NoError(t, env.db.Model(&types.Session{}).
		Where("user_id = ?", "target").Count(&sessions).Error)
	require.EqualValues(t, 1, sessions,
		"后续步骤失败必须回滚先前已执行的 sessions 删除，避免半清理")
}

// confirm_email 仍必须与目标邮箱一致；不匹配 400 且零副作用。
func TestT35PlatformPurgeRequiresMatchingConfirmEmail(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target", nil)

	ctx := t35SystemAdminCtx("sysadmin")
	err := env.svc.PlatformPurgeUser(ctx, "target", "wrong@example.com", "gdpr")
	umRequireHTTPStatus(t, err, 400)

	require.True(t, umLoadUser(t, env, "target").IsActive)
	require.False(t, t35UserDeleted(t, env, "target"))
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberPurged))
}
