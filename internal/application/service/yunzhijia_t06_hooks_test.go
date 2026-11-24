package service

// T06 TDD 红阶段：管理端钩子（AC-10 / AC-11 / AC-12、INV-4 / INV-5）。
//
// 本轮锁定的实现契约（02-plan §8.4）：
//   - interfaces.YunzhijiaEmployeeState {
//         MarkAdminDisabled(ctx, userID) error
//         MarkBlockedAfterPurge(ctx, userID, actorID) error
//     }
//   - userManagementService.WithYunzhijiaEmployeeState(state) 可选注入；
//   - userService.WithYunzhijiaEmployeeState(state) 可选注入。
//
// 行为：
//   - BanUser / PlatformBanUser 成功后 MarkAdminDisabled；
//   - PurgeUser / PlatformPurgeUser 授权通过后先 MarkBlockedAfterPurge，再
//     PurgeUserTx；标记失败返回错误且不 purge；
//   - userService.SetUserActive（人工入口）调用 MarkAdminDisabled；
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
	"github.com/stretchr/testify/require"
)

func yzjT06InjectState(t *testing.T, env *umTestEnv, state *yzjT06State) {
	t.Helper()
	svc, ok := env.svc.(*userManagementService)
	require.True(t, ok, "NewUserManagementService 应返回 *userManagementService")
	svc.WithYunzhijiaEmployeeState(state)
}

func TestYunzhijiaT06TenantBanMarksAdminDisabled(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
	umSeedMembership(t, env, "target-t06", umTenantA, types.TenantRoleViewer)

	state := &yzjT06State{}
	yzjT06InjectState(t, env, state)

	ctx := umCallerCtx("owner-t06", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-t06", "placeholder reason")
	require.NoError(t, err)
	require.Equal(t, []string{"target-t06"}, state.adminCalls,
		"租户级封禁成功后必须把员工 disabled_by_sync 置 false")
}

func TestYunzhijiaT06PlatformBanMarksAdminDisabled(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin-t06", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedUser(t, env, "target-t06", nil)

	state := &yzjT06State{}
	yzjT06InjectState(t, env, state)

	_, err := env.svc.PlatformBanUser(t35SystemAdminCtx("sysadmin-t06"), "target-t06", "placeholder reason")
	require.NoError(t, err)
	require.Equal(t, []string{"target-t06"}, state.adminCalls,
		"平台封禁成功后同样必须把员工 disabled_by_sync 置 false")
}

func TestYunzhijiaT06PurgeMarksBlockedBeforePurge(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
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

func TestYunzhijiaT06PurgeMarkFailureBlocksPurge(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "owner-t06", nil)
	umSeedMembership(t, env, "owner-t06", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-t06", nil)
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
	umSeedUser(t, env, "target-t06", nil)
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

func TestYunzhijiaT06UserServiceSetActiveMarksAdminDisabled(t *testing.T) {
	repo := newYZJT06UserRepo()
	yzjT06SeedActiveUser(repo, "u-t06-manual")

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

	svc := NewUserService(&config.Config{YunzhijiaSSO: cfg}, users, nil, nil, nil, nil)
	state := &yzjT06State{}
	svc.(*userService).WithYunzhijiaEmployeeState(state)

	require.NoError(t, lifecycle.ApplyInactive(context.Background(), employee))

	require.Len(t, users.setActiveCalls, 1, "同步离职应直接调用仓储停用")
	require.False(t, users.setActiveCalls[0].Active)
	require.Empty(t, state.adminCalls,
		"同步侧直接调 userRepo.SetUserActive，不得触发人工入口钩子")
}
