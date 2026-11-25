package service

// R6/F10 同步层回归：真实生命周期注入 YunzhijiaSyncService，验证两条已链接
// 账号路径的同步行为：
//   - 在职：标记缺失 → 锁内闸门拒绝，运行记录出现 provision_failed 异常、
//     计数不增，账号/成员/标记零写入；
//   - 离职（方向修正）：标记缺失仍必须停用账号、写 disabled_by_sync、移除
//     成员，不能把离职员工留在 active，也不记标记缺失异常。
//
// 所有姓名 / open_id 均为占位数据，不使用真实员工信息。

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestYunzhijiaR6SyncApplyEmployeeLinkedAccountWithoutMarkerRecordsProvisionFailed(
	t *testing.T,
) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-r6-sync-active", "占位员工同步甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	userID := "u-r6-sync-active"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-r6-sync-active",
		Name:       "占位员工同步甲",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &userID,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})

	users := newYZJT06UserRepo()
	user := yzjT06SeedActiveUser(users, userID) // 故意不写员工标记
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	yzjT06SeedEmployeeRow(employees, repo.snapshot("open-r6-sync-active"))
	life := yzjT06NewLifecycle(cfg, users, members, employees)
	runs := &yzjT05RunRepo{}
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	require.NoError(t, err, "单条标记缺失不得中断整轮")

	anomalies := yzjT05RunAnomalies(t, run)
	require.Len(t, anomalies, 1, "标记缺失的已链接账号应记且只记一条异常")
	require.Equal(t, types.YunzhijiaAnomalyProvisionFailed, anomalies[0].Kind)
	require.Equal(t, "open-r6-sync-active", anomalies[0].OpenID)
	require.Equal(t, userID, anomalies[0].UserID)
	require.Contains(t, anomalies[0].Detail, "缺少员工标记")

	require.Equal(t, 0, run.CreatedCount)
	require.Equal(t, 0, run.UpdatedCount, "闸门拒绝不得计为已核对的在职账号")
	require.Equal(t, 0, run.DisabledCount)
	require.Equal(t, 1, run.AnomalyCount)

	yzjT06RequireNoMutations(t, users, members, employees)
	require.True(t, user.IsActive, "闸门失败后账号仍为 active")
	require.Nil(t, user.Preferences.YunzhijiaEmployee, "零写入：员工标记保持缺失")
}

// F10 方向修正：离职是收权方向，标记缺失/有密码不得让离职账号 fail-closed
// 留在 active。同步层应正常停用、写 disabled_by_sync、移除成员，且不记
// 标记缺失异常。
func TestYunzhijiaR6SyncApplyInactiveLinkedAccountWithoutMarkerStillDeactivates(
	t *testing.T,
) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-r6-sync-active-ok", "占位员工同步乙", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-r6-sync-inactive-bad", "占位员工同步丙", "金蝶社区"),
	))

	activeUserID := "u-r6-sync-active-ok"
	inactiveUserID := "u-r6-sync-inactive-bad"
	repo := newYZJT05EmployeeRepo(
		&types.YunzhijiaEmployee{
			OpenID:     "open-r6-sync-active-ok",
			Name:       "占位员工同步乙",
			Department: "金蝶社区",
			TenantID:   yzjT05TenantDefault,
			UserID:     &activeUserID,
			Status:     types.YunzhijiaEmployeeStatusActive,
		},
		&types.YunzhijiaEmployee{
			OpenID:     "open-r6-sync-inactive-bad",
			Name:       "占位员工同步丙",
			Department: "金蝶社区",
			TenantID:   yzjT05TenantDefault,
			UserID:     &inactiveUserID,
			Status:     types.YunzhijiaEmployeeStatusInactive,
		},
	)

	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	// 在职账号带标记，作为本轮正常收敛的对照。
	yzjT06MarkUserEmployee(yzjT06SeedActiveUser(users, activeUserID))
	yzjT06SeedMembership(members, activeUserID, yzjT05TenantDefault, types.TenantRoleViewer)
	// 离职账号已链接但缺少员工标记：仍必须停用与移除成员。
	inactiveUser := yzjT06SeedActiveUser(users, inactiveUserID)
	yzjT06SeedMembership(members, inactiveUserID, yzjT05TenantDefault, types.TenantRoleViewer)

	employees := newYZJT06EmployeeRepo()
	yzjT06SeedEmployeeRow(employees, repo.snapshot("open-r6-sync-active-ok"))
	yzjT06SeedEmployeeRow(employees, repo.snapshot("open-r6-sync-inactive-bad"))
	life := yzjT06NewLifecycle(cfg, users, members, employees)
	runs := &yzjT05RunRepo{}
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	require.NoError(t, err, "离职收权路径不得因标记缺失中断整轮")

	anomalies := yzjT05RunAnomalies(t, run)
	require.Empty(t, anomalies, "离职收权不得记标记缺失异常: %+v", anomalies)

	require.Equal(t, 0, run.CreatedCount)
	require.Equal(t, 1, run.UpdatedCount, "带标记的在职对照账号正常计入 updated")
	require.Equal(t, 1, run.DisabledCount, "缺少标记的离职账号仍应计入 disabled")
	require.Equal(t, 0, run.AnomalyCount)

	require.Len(t, users.setActiveCalls, 1, "只应停用离职账号")
	require.Equal(t, inactiveUserID, users.setActiveCalls[0].UserID)
	require.False(t, users.setActiveCalls[0].Active)
	require.False(t, inactiveUser.IsActive)

	require.Len(t, employees.setDisabledCalls, 1, "离职必须写 disabled_by_sync=true")
	require.Equal(t, "open-r6-sync-inactive-bad", employees.setDisabledCalls[0].OpenID)
	require.True(t, employees.setDisabledCalls[0].Disabled)

	require.Len(t, members.removeCalls, 1, "离职必须移除成员关系")
	require.Equal(t, inactiveUserID, members.removeCalls[0].UserID)
	require.False(t, members.hasMembership(inactiveUserID, yzjT05TenantDefault))
	require.Nil(t, inactiveUser.Preferences.YunzhijiaEmployee,
		"离职路径不负责回填标记，仍保持缺失")
}
