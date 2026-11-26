package service

// T03 TDD 红阶段：员工开号（AC-4、AC-5）。
//
// 这些用例在 T03 实现前会因为 YunzhijiaProvisioner 不存在而编译失败，
// 属于红阶段的预期失败；失败原因会记录在 05-tests.md。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYunzhijiaT03ProvisionCreatesViewerAccount(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-placeholder-001", "测试员工甲", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.NoError(t, err)
	require.NotNil(t, user)

	// AC-4：合成邮箱、用户名=姓名、空密码、目标空间、普通 viewer。
	assert.Equal(t, "yzj_openid-placeholder-001@yunzhijia.local", user.Email)
	assert.Equal(t, "测试员工甲", user.Username)
	assert.Empty(t, user.PasswordHash, "员工账号不得设置密码")
	assert.Equal(t, yzjT03TenantDefault, user.TenantID)
	assert.True(t, user.IsActive)
	assert.False(t, user.IsSystemAdmin)
	assert.False(t, user.CanAccessAllTenants)

	assert.Equal(t, 1, env.users.created(), "只应创建一次 users 行")
	assert.Equal(t, 1, env.members.addCallCount(), "应加入目标空间一次")
	role, ok := env.members.roleIn(user.ID, yzjT03TenantDefault)
	require.True(t, ok, "应存在目标空间成员关系")
	assert.Equal(t, types.TenantRoleViewer, role)

	// AC-4：回填员工行 user_id。
	require.Equal(t, 1, env.employees.setUserIDCallCount())
	stored, err := env.employees.GetByOpenID(ctx, employee.OpenID)
	require.NoError(t, err)
	require.NotNil(t, stored.UserID)
	assert.Equal(t, user.ID, *stored.UserID)

	// AC-4/AC-5：不开租户、不触发 ProvisionOwnerSpace/EnsureOwner。
	assert.Equal(t, 0, env.tenants.createCallCount(), "员工开号不得创建租户")
	assert.Equal(t, 0, env.members.ensureOwnerCallCount(), "员工开号不得走 owner 开通")
}

func TestYunzhijiaT03ProvisionUniquifiesUsernameOnConflict(t *testing.T) {
	ctx := context.Background()
	existing := &types.User{
		ID:       "existing-user",
		Username: "测试员工乙",
		Email:    "existing-placeholder@example.invalid",
	}
	employee := yzjT03Employee("abc123xyz", "测试员工乙", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(existing)
	env.remover = &yzjT03Remover{repo: env.users}
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.NoError(t, err)
	require.NotNil(t, user)

	assert.NotEqual(t, "测试员工乙", user.Username, "用户名冲突时必须唯一化")
	assert.True(t, strings.HasPrefix(user.Username, "测试员工乙-"),
		"用户名应保留姓名前缀，实际=%q", user.Username)
	assert.Contains(t, user.Username, "abc123", "唯一化后缀应取 open_id 前 6 位")
	assert.Empty(t, user.PasswordHash)
	assert.Equal(t, yzjT03TenantDefault, user.TenantID)
}

// TestYunzhijiaT03ProvisionExistingEmployeeAccountReused 覆盖 F6 的正向面：
// 已有 linked 账号带员工标记且密码为空时，正常复用并补齐 viewer，零建号/零回填。
func TestYunzhijiaT03ProvisionExistingEmployeeAccountReused(t *testing.T) {
	ctx := context.Background()
	existingTenant := yzjT03TenantDefault
	existing := &types.User{
		ID:          "existing-user-1",
		Username:    "测试员工丙",
		Email:       "existing-placeholder-1@example.invalid",
		TenantID:    existingTenant,
		IsActive:    true,
		Preferences: yzjT03EmployeePreferences(),
	}
	employee := yzjT03Employee("openid-existing-001", "测试员工丙", existingTenant)
	employee.UserID = &existing.ID

	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(existing)
	env.remover = &yzjT03Remover{repo: env.users}
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, existing.ID, user.ID, "已有 user_id 时必须复用原账号")
	assert.Empty(t, user.PasswordHash, "员工账号密码保持为空")
	assert.Equal(t, 0, env.users.created(), "已有账号不得重复建号")
	assert.Equal(t, 0, env.users.updateCalls, "复用不得回写用户行")
	role, ok := env.members.roleIn(existing.ID, existingTenant)
	require.True(t, ok, "已有账号缺少成员关系时应补齐 viewer")
	assert.Equal(t, types.TenantRoleViewer, role)
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "已有 user_id 无需回填")
}

// TestYunzhijiaT03ProvisionExistingAccountWithPasswordRejected 覆盖 F6 的
// L3 闸顺序：账号有员工标记但有密码，先过标记闸后仍被密码闸拒绝，零写入。
func TestYunzhijiaT03ProvisionExistingAccountWithPasswordRejected(t *testing.T) {
	ctx := context.Background()
	existingTenant := yzjT03TenantDefault
	existing := &types.User{
		ID:           "existing-user-with-password",
		Username:     "测试员工丙",
		Email:        "existing-placeholder-password@example.invalid",
		PasswordHash: "keep-this-hash",
		TenantID:     existingTenant,
		IsActive:     true,
		Preferences:  yzjT03EmployeePreferences(),
	}
	employee := yzjT03Employee("openid-existing-password-001", "测试员工丙", existingTenant)
	employee.UserID = &existing.ID

	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(existing)
	env.remover = &yzjT03Remover{repo: env.users}
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.Error(t, err)
	assert.Nil(t, user)
	assert.Contains(t, err.Error(), "已设置密码", "有标记的账号应被 L3 密码闸拒绝")
	assert.False(t, errors.Is(err, ErrYunzhijiaEmployeeMarkerMissing),
		"有标记的账号应先通过标记闸，再被密码闸拦截")
	assert.Equal(t, 0, env.members.addCallCount(), "拒绝时不得补成员关系")
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "拒绝时不得回填 user_id")
	assert.Equal(t, 0, env.users.updateCalls, "拒绝时不得回写用户行")
	assert.Equal(t, "keep-this-hash", env.users.users[existing.ID].PasswordHash,
		"已有密码不得被修改")
}

// TestYunzhijiaT03ProvisionExistingAccountWithoutMarkerFailsClosed 覆盖 F6：
// 已有 linked 账号缺少员工标记时 fail-closed，不补成员、不回填、不写任何数据。
func TestYunzhijiaT03ProvisionExistingAccountWithoutMarkerFailsClosed(t *testing.T) {
	ctx := context.Background()
	existingTenant := yzjT03TenantDefault
	existing := &types.User{
		ID:       "existing-user-no-marker",
		Username: "测试员工丙",
		Email:    "existing-placeholder-no-marker@example.invalid",
		TenantID: existingTenant,
		IsActive: true,
	}
	employee := yzjT03Employee("openid-existing-no-marker-001", "测试员工丙", existingTenant)
	employee.UserID = &existing.ID

	env := newYZJT03Env(yzjT03Config(), employee)
	env.users = newYZJT03UserRepo(existing)
	env.remover = &yzjT03Remover{repo: env.users}
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.Error(t, err)
	assert.Nil(t, user)
	require.ErrorIs(t, err, ErrYunzhijiaEmployeeMarkerMissing)
	assert.Equal(t, 0, env.members.addCallCount(), "标记缺失时不得补成员关系")
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "标记缺失时不得回填 user_id")
	assert.Equal(t, 0, env.users.updateCalls, "标记缺失时不得静默回写标记")

	stored, getErr := env.users.GetUserByID(ctx, existing.ID)
	require.NoError(t, getErr)
	assert.Nil(t, stored.Preferences.YunzhijiaEmployee, "零写入：员工标记保持缺失")

	storedEmployee, getErr := env.employees.GetByOpenID(ctx, employee.OpenID)
	require.NoError(t, getErr)
	require.NotNil(t, storedEmployee)
	require.NotNil(t, storedEmployee.UserID)
	assert.Equal(t, existing.ID, *storedEmployee.UserID, "员工行 user_id 保持原值")
}

func TestYunzhijiaT03ProvisionRollsBackNewUserWhenAddMemberFails(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-rollback-001", "测试员工丁", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)
	env.members.failAdd = errors.New("simulated add member failure")

	user, err := env.provisioner.EnsureAccount(ctx, employee)
	require.Error(t, err)
	assert.Nil(t, user)
	require.Len(t, env.remover.removed, 1, "入组失败必须回滚刚建的用户")
	assert.Equal(t, 0, env.users.count(), "回滚后不应残留用户行")
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "回滚后不得回填 user_id")
}

func TestYunzhijiaT03ProvisionConcurrentSameOpenIDCreatesOneAccount(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 3; attempt++ {
		employee := yzjT03Employee("openid-concurrent-001", "测试员工戊", yzjT03TenantDefault)
		env := newYZJT03Env(yzjT03Config(), employee)

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			results []*types.User
			errs    []error
		)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// 每个并发调用各自持有一份员工快照，模拟登录/同步同时进入。
				u, err := env.provisioner.EnsureAccount(ctx, yzjT03Employee(
					"openid-concurrent-001", "测试员工戊", yzjT03TenantDefault))
				mu.Lock()
				results = append(results, u)
				errs = append(errs, err)
				mu.Unlock()
			}()
		}
		wg.Wait()

		require.Len(t, results, 2)
		for i, err := range errs {
			require.NoError(t, err, "并发第 %d 个调用不应失败（唯一冲突应回读已有账号）", i)
		}
		require.NotNil(t, results[0])
		require.NotNil(t, results[1])
		assert.Equal(t, results[0].ID, results[1].ID, "同一 open_id 并发只能得到一个账号")
		assert.Equal(t, 1, env.users.created(), "同一 open_id 只应创建一次 users 行")
		assert.Equal(t, 1, env.members.addCallCount(), "同一 open_id 只应产生一条成员关系")
	}
}
