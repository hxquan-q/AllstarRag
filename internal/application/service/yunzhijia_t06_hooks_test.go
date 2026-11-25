package service

// T06：管理端钩子（AC-10 / AC-11 / AC-12、INV-4 / INV-5）。
//
// R4（F1/F2）后的实现契约：
//   - interfaces.YunzhijiaEmployeeState {
//         MarkAdminDisabled(ctx, userID) (wasDisabled bool, err error)
//         RestoreAdminDisabled(ctx, userID, wasDisabled) error
//         MarkBlockedAfterPurge(ctx, userID, actorID) error
//     }
//   - 钩子只看已加载 users 行的 preferences.yunzhijia_employee 标记：
//     非员工直接跳过、不触碰员工目录；员工操作失败仍然 fail-closed；
//   - BanUser / PlatformBanUser 先 MarkAdminDisabled 再封禁；标记失败返回错误
//     且不执行封禁；封禁事务失败时 RestoreAdminDisabled 恢复原标记；
//   - PurgeUser / PlatformPurgeUser 授权通过后先 MarkBlockedAfterPurge，再
//     PurgeUserTx；标记失败返回错误且不 purge；
//   - userService.SetUserActive（人工入口）同样先标记、失败停机、失败补偿；
//     同步侧直接调 userRepo.SetUserActive，不经过该钩子；
//   - 钩子不受 enabled 门控。
//
// 所有账号均为占位数据；不写真实邮箱/员工信息。

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func yzjT06InjectState(t *testing.T, env *umTestEnv, state *yzjT06State) {
	t.Helper()
	svc, ok := env.svc.(*userManagementService)
	require.True(t, ok, "NewUserManagementService 应返回 *userManagementService")
	svc.WithYunzhijiaEmployeeState(state)
}

// yzjT06MarkUserEmployee 给 users 行写员工标记（模拟同步开号）。
func yzjT06MarkUserEmployee(u *types.User) {
	marker := true
	u.Preferences.YunzhijiaEmployee = &marker
}

func TestYunzhijiaT06TenantBanMarksAdminDisabled(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	state := &yzjT06State{}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.NoError(t, err)
	require.Equal(t, []string{"target-t06"}, state.adminCalls,
		"租户级封禁成功后必须把员工 disabled_by_sync 置 false")
}

// F1：非员工账号的封禁不得触碰员工目录（迁移缺失/员工表不可用也不受影响）。
func TestYunzhijiaT06TenantBanSkipsEmployeeDirectoryForOrdinaryUser(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	// 员工钩子即使注入且会报错，普通账号也不得调用它。
	state := &yzjT06State{adminErr: errors.New("employee directory unavailable")}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.NoError(t, err, "普通账号封禁不得依赖员工目录")
	require.Empty(t, state.adminCalls, "非员工不得调用 MarkAdminDisabled")
	require.False(t, umLoadUser(t, env, "target-t06").IsActive)
}

func TestYunzhijiaT06PlatformBanMarksAdminDisabled(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)

	state := &yzjT06State{}
	yzjT06InjectState(t, env, state)

	_, err := env.svc.PlatformBanUser(t35SystemAdminCtx("sysadmin-t06"), "target-t06", "placeholder reason")
	require.NoError(t, err)
	require.Equal(t, []string{"target-t06"}, state.adminCalls,
		"平台封禁成功后同样必须把员工 disabled_by_sync 置 false")
}

func TestYunzhijiaT06PlatformBanSkipsEmployeeDirectoryForOrdinaryUser(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", nil)

	state := &yzjT06State{adminErr: errors.New("employee directory unavailable")}
	yzjT06InjectState(t, env, state)

	_, err := env.svc.PlatformBanUser(t35SystemAdminCtx("sysadmin-t06"), "target-t06", "placeholder reason")
	require.NoError(t, err, "普通账号平台封禁不得依赖员工目录")
	require.Empty(t, state.adminCalls)
	require.False(t, umLoadUser(t, env, "target-t06").IsActive)
}

func TestYunzhijiaT06TenantBanMarkFailureReturnsErrorBlocksBan(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	boom := errors.New("employee state unavailable")
	state := &yzjT06State{adminErr: boom}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.ErrorIs(t, err, boom, "标记 disabled_by_sync 失败必须向上返回错误")
	require.Len(t, state.adminCalls, 1)
	require.True(t, umLoadUser(t, env, "target-t06").IsActive,
		"先标记后封禁：标记失败必须阻断封禁，账号保持可用（INV-4）")
}

func TestYunzhijiaT06PlatformBanMarkFailureReturnsErrorBlocksBan(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)

	boom := errors.New("employee state unavailable")
	state := &yzjT06State{adminErr: boom}
	yzjT06InjectState(t, env, state)

	_, err := env.svc.PlatformBanUser(
		t35SystemAdminCtx("sysadmin-t06"), "target-t06", "placeholder reason")
	require.ErrorIs(t, err, boom, "平台标记失败同样必须向上返回错误")
	require.Len(t, state.adminCalls, 1)
	require.True(t, umLoadUser(t, env, "target-t06").IsActive,
		"平台封禁同样先标记后封禁：标记失败必须阻断封禁（INV-4）")
}

// F2：BanUserTx 失败后必须把 disabled_by_sync 恢复到清除前的值。
type yzjT06BanTxFailRepo struct {
	interfaces.UserRepository
	err error
}

func (r *yzjT06BanTxFailRepo) BanUserTx(
	context.Context, uint64, string, string, types.TenantRole, *types.AuditLog,
) (*types.User, error) {
	return nil, r.err
}

func TestYunzhijiaT06TenantBanTransactionFailureRestoresMarker(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	boom := errors.New("ban transaction failed")
	svc := NewUserManagementService(
		&yzjT06BanTxFailRepo{UserRepository: env.userRepo, err: boom},
		env.memberRepo, env.audit,
	)
	state := &yzjT06State{wasDisabled: true}
	svc.(*userManagementService).WithYunzhijiaEmployeeState(state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.Error(t, err, "BanUserTx 失败必须返回错误")
	require.Equal(t, []yzjT06RestoreCall{{UserID: "target-t06", WasDisabled: true}},
		state.restoreCalls, "封禁失败必须补偿恢复 disabled_by_sync")
	require.True(t, umLoadUser(t, env, "target-t06").IsActive,
		"封禁事务失败不得留下停用账号")
}

func TestYunzhijiaT06PurgeMarksBlockedBeforePurge(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	userExistedAtMark := false
	state := &yzjT06State{
		onMarkBlocked: func() {
			_, lookupErr := env.userRepo.GetUserByID(context.Background(), "target-t06")
			userExistedAtMark = lookupErr == nil
		},
	}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	err := env.svc.PurgeUser(ctx, umTenantA, "target-t06", "target-t06@example.com", "placeholder reason")
	require.NoError(t, err)

	require.Len(t, state.blockedCalls, 1, "租户级删除必须先标记 blocked")
	require.Equal(t, "target-t06", state.blockedCalls[0].OpenID)
	require.True(t, userExistedAtMark, "MarkBlockedAfterPurge 必须在 purge 事务之前执行")
	require.True(t, t35UserDeleted(t, env, "target-t06"), "标记成功后仍须完成 tombstone")
}

func TestYunzhijiaT06PurgeSkipsEmployeeDirectoryForOrdinaryUser(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	state := &yzjT06State{blockedErr: errors.New("employee directory unavailable")}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	err := env.svc.PurgeUser(ctx, umTenantA, "target-t06", "target-t06@example.com", "placeholder reason")
	require.NoError(t, err, "普通账号删除不得依赖员工目录")
	require.Empty(t, state.blockedCalls)
	require.True(t, t35UserDeleted(t, env, "target-t06"))
}

func TestYunzhijiaT06PurgeMarkFailureBlocksPurge(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	boom := errors.New("employee state unavailable")
	state := &yzjT06State{blockedErr: boom}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	err := env.svc.PurgeUser(ctx, umTenantA, "target-t06", "target-t06@example.com", "placeholder reason")
	require.ErrorIs(t, err, boom, "标记 blocked 失败必须向上返回错误")
	require.False(t, t35UserDeleted(t, env, "target-t06"), "标记失败时不得继续 purge")
	require.True(t, umLoadUser(t, env, "target-t06").IsActive, "标记失败时账号应保持可用")
}

func TestYunzhijiaT06PlatformPurgeMarksBlockedBeforePurge(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", yzjT06MarkUserEmployee)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	userExistedAtMark := false
	state := &yzjT06State{
		onMarkBlocked: func() {
			_, lookupErr := env.userRepo.GetUserByID(context.Background(), "target-t06")
			userExistedAtMark = lookupErr == nil
		},
	}
	yzjT06InjectState(t, env, state)

	err := env.svc.PlatformPurgeUser(
		t35SystemAdminCtx("sysadmin-t06"), "target-t06", "target-t06@example.com", "placeholder reason")
	require.NoError(t, err)

	require.Len(t, state.blockedCalls, 1, "平台删除必须先标记 blocked")
	require.Equal(t, "target-t06", state.blockedCalls[0].OpenID)
	require.True(t, userExistedAtMark, "平台删除也必须在 purge 事务之前标记")
	require.True(t, t35UserDeleted(t, env, "target-t06"))
}

func TestYunzhijiaT06PlatformPurgeSkipsEmployeeDirectoryForOrdinaryUser(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", nil)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	state := &yzjT06State{blockedErr: errors.New("employee directory unavailable")}
	yzjT06InjectState(t, env, state)

	err := env.svc.PlatformPurgeUser(
		t35SystemAdminCtx("sysadmin-t06"), "target-t06", "target-t06@example.com", "placeholder reason")
	require.NoError(t, err, "普通账号平台删除不得依赖员工目录")
	require.Empty(t, state.blockedCalls)
	require.True(t, t35UserDeleted(t, env, "target-t06"))
}

// F1：Unban 是纯恢复路径，不调用任何员工目录钩子；普通账号不受影响。
func TestYunzhijiaT06UnbanDoesNotTouchEmployeeDirectory(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	state := &yzjT06State{adminErr: errors.New("employee directory unavailable")}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.NoError(t, err)
	_, err = env.svc.UnbanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.NoError(t, err)
	require.Empty(t, state.adminCalls, "unban 不得调用 MarkAdminDisabled")
	require.Empty(t, state.blockedCalls)
	require.True(t, umLoadUser(t, env, "target-t06").IsActive)
}

func TestYunzhijiaT06UserServiceSetActiveMarksAdminDisabled(t *testing.T) {
	repo := newYZJT06UserRepo()
	yzjT06MarkUserEmployee(yzjT06SeedActiveUser(repo, "u-t06-manual"))

	// enabled=false 时不门控钩子。
	svc := NewUserService(&config.Config{
		YunzhijiaSSO: yzjT06Config(false),
	}, repo, nil, nil, nil, nil)
	userSvc, ok := svc.(*userService)
	require.True(t, ok)

	state := &yzjT06State{}
	userSvc.WithYunzhijiaEmployeeState(state)

	_, err := svc.SetUserActive(context.Background(), "u-t06-manual", "admin-t06", false)
	require.NoError(t, err)
	require.Equal(t, []string{"u-t06-manual"}, state.adminCalls,
		"人工停用入口必须清 disabled_by_sync（MarkAdminDisabled）")
}

// F1：人工停用普通账号不触碰员工目录。
func TestYunzhijiaT06UserServiceSetActiveSkipsEmployeeDirectoryForOrdinaryUser(t *testing.T) {
	repo := newYZJT06UserRepo()
	yzjT06SeedActiveUser(repo, "u-t06-ordinary")

	svc := NewUserService(&config.Config{YunzhijiaSSO: yzjT06Config(false)},
		repo, nil, nil, nil, nil)
	state := &yzjT06State{adminErr: errors.New("employee directory unavailable")}
	svc.(*userService).WithYunzhijiaEmployeeState(state)

	_, err := svc.SetUserActive(context.Background(), "u-t06-ordinary", "admin-t06", false)
	require.NoError(t, err, "普通账号人工停用不得依赖员工目录")
	require.Empty(t, state.adminCalls)
	require.False(t, repo.users["u-t06-ordinary"].IsActive)
}

// F1：员工目标的人工停用，钩子失败必须 fail-closed（不停用）。
func TestYunzhijiaT06UserServiceSetActiveMarkerFailureBlocksDeactivation(t *testing.T) {
	repo := newYZJT06UserRepo()
	yzjT06MarkUserEmployee(yzjT06SeedActiveUser(repo, "u-t06-manual-fail"))

	svc := NewUserService(&config.Config{YunzhijiaSSO: yzjT06Config(false)},
		repo, nil, nil, nil, nil)
	boom := errors.New("employee state unavailable")
	state := &yzjT06State{adminErr: boom}
	svc.(*userService).WithYunzhijiaEmployeeState(state)

	_, err := svc.SetUserActive(context.Background(), "u-t06-manual-fail", "admin-t06", false)
	require.ErrorIs(t, err, boom)
	require.Empty(t, repo.setActiveCalls, "标记失败不得继续停用")
	require.True(t, repo.users["u-t06-manual-fail"].IsActive)
}

// F2：人工停用的仓储写入失败后恢复 disabled_by_sync。
func TestYunzhijiaT06UserServiceSetActiveFailureRestoresMarker(t *testing.T) {
	repo := newYZJT06UserRepo()
	yzjT06MarkUserEmployee(yzjT06SeedActiveUser(repo, "u-t06-manual-compensate"))
	repo.setActiveErr = errors.New("repository unavailable")

	svc := NewUserService(&config.Config{YunzhijiaSSO: yzjT06Config(false)},
		repo, nil, nil, nil, nil)
	state := &yzjT06State{wasDisabled: true}
	svc.(*userService).WithYunzhijiaEmployeeState(state)

	_, err := svc.SetUserActive(context.Background(), "u-t06-manual-compensate", "admin-t06", false)
	require.Error(t, err)
	require.Equal(t, []yzjT06RestoreCall{
		{UserID: "u-t06-manual-compensate", WasDisabled: true},
	}, state.restoreCalls, "停用失败必须补偿恢复 disabled_by_sync")
}

func TestYunzhijiaT06SyncPathBypassesUserServiceHook(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	cfg := yzjT06Config(false)
	lifecycle := yzjT06NewLifecycle(cfg, users, members, employees)

	userID := "u-t06-sync-leave"
	yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-sync-leave-0001", "占位员工辰", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive
	yzjT06SeedEmployeeRow(employees, employee)

	svc := NewUserService(&config.Config{YunzhijiaSSO: cfg}, users, nil, nil, nil, nil)
	state := &yzjT06State{}
	svc.(*userService).WithYunzhijiaEmployeeState(state)

	yzjT06ApplyInactiveOK(t, lifecycle, employee)

	require.Len(t, users.setActiveCalls, 1, "同步离职应直接调用仓储停用")
	require.False(t, users.setActiveCalls[0].Active)
	require.Empty(t, state.adminCalls,
		"同步侧直接调 userRepo.SetUserActive，不得触发人工入口钩子")
}
