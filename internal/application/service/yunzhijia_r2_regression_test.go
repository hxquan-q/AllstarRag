package service

// R2 部署前审查修复（M1/AC-7/L1..L6/N1..N5）的回归用例。
//
// 只验证 R2 改动的可观测行为，不重复 T03–T07 已覆盖的既有矩阵：
//   - AC-7：服务端开号写入 preferences.yunzhijia_employee=true，登录响应可见；
//           已有账号（普通 viewer/owner/超管）不带该标记。
//   - L1：ApplyInactive 顺序 SetDisabledBySync(true) → SetUserActive(false) → RemoveMember；
//         标记写入失败时不得继续停用/移除。
//   - L3：合成邮箱被有密码账号占用时开号失败，不写成员、不回填 user_id、不签 token。
//   - L6：blocked_skipped 的 Detail 为「员工已删除并禁止重建，同步跳过」。
//   - N2：并发补成员冲突不记 provision_failed。
//   - N5：调度完成日志含计数与耗时。
//
// 所有姓名 / open_id / ticket 均为占位数据，不使用真实员工信息。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// AC-7：员工标记
// ---------------------------------------------------------------------------

func TestYunzhijiaR2ProvisionedEmployeeCarriesPreferenceMarker(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-r2-ac7-001", "占位员工AC7", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)
	env.client.name = employee.Name

	// 服务端开号时写入标记，客户端无法设置。
	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NotNil(t, user.Preferences.YunzhijiaEmployee,
		"开号时必须写入 preferences.yunzhijia_employee")
	assert.True(t, *user.Preferences.YunzhijiaEmployee)

	// 登录响应复用 types.User，标记必须随响应返回给前端。
	resp, err := env.login.Login(ctx, "ticket-placeholder-r2-ac7")
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.NotNil(t, resp.User)
	require.NotNil(t, resp.User.Preferences.YunzhijiaEmployee)
	assert.True(t, *resp.User.Preferences.YunzhijiaEmployee)

	body, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"yunzhijia_employee":true`,
		"登录 JSON 必须带出员工标记")
}

func TestYunzhijiaR2ExistingAccountHasNoEmployeeMarker(t *testing.T) {
	ctx := context.Background()
	userID := "u-r2-ordinary-viewer"
	employee := yzjT03Employee("openid-r2-ordinary", "占位普通员工", yzjT03TenantDefault)
	employee.UserID = &userID

	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(&types.User{
		ID:           userID,
		Username:     "占位普通员工",
		Email:        "ordinary-placeholder@example.invalid",
		PasswordHash: "",
		TenantID:     yzjT03TenantDefault,
		IsActive:     true,
	})
	env.remover = &yzjT03Remover{repo: env.users}
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)
	env.client.name = employee.Name

	resp, err := env.login.Login(ctx, "ticket-placeholder-r2-ordinary")
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.NotNil(t, resp.User)
	assert.Nil(t, resp.User.Preferences.YunzhijiaEmployee,
		"普通账号不得被写入员工标记（否则前端会误隐藏改密入口）")
}

// ---------------------------------------------------------------------------
// L1：离职处理顺序
// ---------------------------------------------------------------------------

func TestYunzhijiaR2ApplyInactiveCallOrder(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	var order []string
	users.order = &order
	members.order = &order
	employees.order = &order
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-r2-order"
	yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-r2-order-001", "占位员工顺", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive

	yzjT06ApplyInactiveOK(t, lifecycle, employee)

	want := []string{
		"set-disabled:true",
		"set-active:false",
		fmt.Sprintf("remove:%d", yzjT06TenantDefault),
	}
	require.Equal(t, want, order,
		"离职必须先写同步停用标记、再停用账号、最后移除成员关系")
}

func TestYunzhijiaR2ApplyInactiveStopsWhenMarkerWriteFails(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	var order []string
	users.order = &order
	members.order = &order
	employees.order = &order
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-r2-order-fail"
	yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-r2-order-fail", "占位员工败", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive

	boom := errors.New("employee repo unavailable")
	employees.setDisabledErr = boom

	_, err := lifecycle.ApplyInactive(context.Background(), employee)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []string{"set-disabled:true"}, order,
		"标记写入失败后不得继续停用账号或移除成员关系")
	require.Empty(t, users.setActiveCalls)
	require.Empty(t, members.removeCalls)
}

// ---------------------------------------------------------------------------
// L3：合成邮箱被有密码账号占用
// ---------------------------------------------------------------------------

func TestYunzhijiaR2RejectsSyntheticEmailOwnedByPasswordAccount(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-r2-l3-001", "占位员工L3", yzjT03TenantDefault)
	occupied := &types.User{
		ID:           "u-r2-occupied",
		Username:     "抢注占位账号",
		Email:        "yzj_openid-r2-l3-001@yunzhijia.local",
		PasswordHash: "existing-hash-placeholder",
		TenantID:     yzjT03TenantDefault,
		IsActive:     true,
	}

	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(occupied)
	env.remover = &yzjT03Remover{repo: env.users}
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)
	env.client.name = employee.Name

	resp, err := env.login.Login(ctx, "ticket-placeholder-r2-l3")
	require.Error(t, err, "有密码账号占用合成邮箱时必须 fail-closed")
	assert.Nil(t, resp)

	assert.Equal(t, 0, env.members.addCallCount(), "不得把抢注账号拉进员工空间")
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "不得回填 user_id")
	assert.Equal(t, 0, env.tokens.createCalls, "不得签发 token")
	assert.Equal(t, 0, env.users.created(), "不得再建账号")

	stored, getErr := env.employees.GetByOpenID(ctx, employee.OpenID)
	require.NoError(t, getErr)
	require.NotNil(t, stored)
	assert.Nil(t, stored.UserID, "开号失败后员工行不得回填 user_id")
	assert.Equal(t, "existing-hash-placeholder",
		env.users.users[occupied.ID].PasswordHash, "抢注账号的密码不得被修改")
}

// ---------------------------------------------------------------------------
// L6：blocked_skipped 文案
// ---------------------------------------------------------------------------

func TestYunzhijiaR2BlockedSkippedDetailText(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-r2-l6", "占位员工L6", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	blockedUID := "u-r2-l6"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-r2-l6",
		Name:       "占位员工L6",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &blockedUID,
		Status:     types.YunzhijiaEmployeeStatusActive,
		Blocked:    true,
	})
	runs := &yzjT05RunRepo{}
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, newYZJT05Lifecycle())

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	require.NoError(t, err)

	anomalies := yzjT05RunAnomalies(t, run)
	var found *types.YunzhijiaSyncAnomaly
	for i := range anomalies {
		if anomalies[i].Kind == types.YunzhijiaAnomalyBlockedSkipped {
			found = &anomalies[i]
			break
		}
	}
	require.NotNil(t, found, "应记录 blocked_skipped 异常")
	assert.Equal(t, "员工已删除并禁止重建，同步跳过", found.Detail)
}

// ---------------------------------------------------------------------------
// N2：并发补成员冲突不记 provision_failed
// ---------------------------------------------------------------------------

func TestYunzhijiaR2MembershipConflictDoesNotRecordProvisionFailed(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-r2-n2", "占位员工N2", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	userID := "u-r2-n2"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-r2-n2",
		Name:       "占位员工N2",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &userID,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})

	users := newYZJT06UserRepo()
	yzjT06SeedActiveUser(users, userID)
	members := newYZJT06MemberService()
	// 模拟登录侧已并发完成入组：同步这轮 AddMember 撞唯一约束。
	members.addErr = ErrMembershipAlreadyExists
	employees := newYZJT06EmployeeRepo()
	life := yzjT06NewLifecycle(cfg, users, members, employees)
	runs := &yzjT05RunRepo{}
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	require.NoError(t, err, "并发补成员冲突应被视为成功，不应中断整轮")

	anomalies := yzjT05RunAnomalies(t, run)
	for _, anomaly := range anomalies {
		assert.NotEqual(t, types.YunzhijiaAnomalyProvisionFailed, anomaly.Kind,
			"成员关系已存在的并发冲突不得记为 provision_failed: %+v", anomaly)
	}
	assert.Equal(t, types.YunzhijiaSyncStatusSuccess, run.Status)
	assert.Equal(t, 0, run.CreatedCount)
	assert.Equal(t, 1, run.UpdatedCount, "冲突后仍应计为已核对的在职账号")
}

// ---------------------------------------------------------------------------
// N5：调度完成日志含计数与耗时
// ---------------------------------------------------------------------------

func TestYunzhijiaR2SchedulerCompletionLogHasCountsAndDuration(t *testing.T) {
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetLogLevel(logger.LevelInfo)
	t.Cleanup(func() { logger.ConfigureFromEnv() })

	cfg := yzjT07Config(true, 0)
	s, _, _, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return yzjT07FixedNow(t) }

	ctx := context.Background()
	s.Start(ctx)
	yzjT07WaitFor(t, "启动补跑应完成", func() bool { return runs.finishCount() >= 1 })
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)

	out := logs.String()
	require.Contains(t, out, "同步完成", "完成日志原文=%q", out)
	require.Contains(t, out, "trigger=startup")
	require.Contains(t, out, "active=1")
	require.Contains(t, out, "inactive=0")
	require.Contains(t, out, "anomalies=0")
	require.Contains(t, out, "耗时=", "完成日志必须包含耗时: %q", out)
	assert.True(t, strings.Contains(out, "created="), "完成日志必须含 created 计数")
}
