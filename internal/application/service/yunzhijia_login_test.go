package service

// T03 TDD 红阶段：登录服务（AC-3、AC-5、AC-6、AC-16）。
//
// 约定（红阶段锁定，实现方对齐）：
//   - 业务拒绝（无效 ticket / 上游失败 / 0 条 / 同名 / blocked / 离职 / 封禁 /
//     配置缺失）返回非 nil 的 LoginResponse，Success=false + 中文 message，
//     同时 error 为 nil；只有未预期内部错误才返回 error。
//   - HTTP 层对 Success=false 统一回 401（handler 用例锁定）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	yzjT03MsgInvalidTicket  = "请从云之家重新打开"
	yzjT03MsgUpstream       = "稍后再试"
	yzjT03MsgProvisioning   = "账号开通中，请明天再试"
	yzjT03MsgDuplicateName  = "在职员工存在同名，请联系管理员"
	yzjT03MsgAccountBlocked = "账号不可用，请联系管理员"
	yzjT03MsgConfig         = "系统暂不可用，请联系管理员"
)

func yzjT03RequireRejected(t *testing.T, resp *types.LoginResponse, err error, want string) {
	t.Helper()
	require.NoError(t, err, "业务拒绝应通过 Success=false 表达，而不是 error")
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, want)
	assert.Empty(t, resp.Token)
	assert.Empty(t, resp.RefreshToken)
}

func TestYunzhijiaT03LoginRejectsMissingOrDisabledConfig(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-config-001", "测试员工甲", yzjT03TenantDefault)

	t.Run("nil config", func(t *testing.T) {
		env := newYZJT03Env(nil, employee)
		resp, err := env.login.Login(ctx, "ticket-placeholder-001")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgConfig)
		assert.Equal(t, 0, env.client.calls, "配置缺失时不得调用云之家接口")
	})

	t.Run("disabled config", func(t *testing.T) {
		cfg := yzjT03Config()
		cfg.Enabled = false
		env := newYZJT03Env(cfg, employee)
		resp, err := env.login.Login(ctx, "ticket-placeholder-001")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgConfig)
		assert.Equal(t, 0, env.client.calls, "配置关闭时不得调用云之家接口")
	})

	t.Run("invalid config fail-closed", func(t *testing.T) {
		cfg := yzjT03Config()
		cfg.TenantHR = cfg.TenantDefault // Validate 明确拒绝相同空间
		env := newYZJT03Env(cfg, employee)
		resp, err := env.login.Login(ctx, "ticket-placeholder-001")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgConfig)
		assert.Equal(t, 0, env.client.calls, "配置非法时不得调用云之家接口")
	})
}

func TestYunzhijiaT03LoginMapsInvalidTicketAndUpstreamFailure(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-ticket-001", "测试员工甲", yzjT03TenantDefault)

	t.Run("invalid ticket", func(t *testing.T) {
		env := newYZJT03Env(yzjT03Config(), employee)
		env.client.err = yunzhijia.ErrInvalidTicket
		resp, err := env.login.Login(ctx, "ticket-placeholder-invalid")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgInvalidTicket)
		assert.Equal(t, 1, env.client.calls)
	})

	t.Run("upstream failure", func(t *testing.T) {
		env := newYZJT03Env(yzjT03Config(), employee)
		env.client.err = yunzhijia.ErrUpstream
		resp, err := env.login.Login(ctx, "ticket-placeholder-upstream")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgUpstream)
	})
}

func TestYunzhijiaT03LoginRejectsZeroOrDuplicateNames(t *testing.T) {
	ctx := context.Background()

	t.Run("zero active employees", func(t *testing.T) {
		employee := yzjT03Employee("openid-zero-001", "测试员工甲", yzjT03TenantDefault)
		env := newYZJT03Env(yzjT03Config(), employee)
		env.client.name = "不存在的占位姓名"
		resp, err := env.login.Login(ctx, "ticket-placeholder-001")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgProvisioning)
		assert.Equal(t, 0, env.users.created())
	})

	t.Run("duplicate active names", func(t *testing.T) {
		e1 := yzjT03Employee("openid-dup-001", "测试员工同名", yzjT03TenantDefault)
		e2 := yzjT03Employee("openid-dup-002", "测试员工同名", yzjT03TenantDefault)
		env := newYZJT03Env(yzjT03Config(), e1, e2)
		env.client.name = "测试员工同名"
		resp, err := env.login.Login(ctx, "ticket-placeholder-002")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgDuplicateName)
		assert.Equal(t, 0, env.users.created())
	})
}

func TestYunzhijiaT03LoginRejectsBlockedInactiveAndDisabledBySync(t *testing.T) {
	ctx := context.Background()

	t.Run("blocked employee", func(t *testing.T) {
		employee := yzjT03Employee("openid-blocked-001", "测试员工己", yzjT03TenantDefault)
		employee.Blocked = true
		env := newYZJT03Env(yzjT03Config(), employee)
		env.client.name = employee.Name
		resp, err := env.login.Login(ctx, "ticket-placeholder-003")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgAccountBlocked)
		assert.Equal(t, 0, env.users.created(), "blocked 员工不得开号")
		assert.Equal(t, 0, env.members.addCallCount(), "blocked 员工登录拒绝时不得调用 AddMember")
		assert.Equal(t, 0, env.employees.setUserIDCallCount(), "blocked 员工登录拒绝时不得回填 user_id")
	})

	t.Run("associated user inactive", func(t *testing.T) {
		userID := "inactive-user-1"
		employee := yzjT03Employee("openid-inactive-001", "测试员工庚", yzjT03TenantDefault)
		employee.UserID = &userID
		env := newYZJT03Env(yzjT03Config(), employee)
		env.users = newYZJT03UserRepo(&types.User{
			ID: userID, Username: "测试员工庚", Email: "inactive-placeholder@example.invalid",
			TenantID: yzjT03TenantDefault, IsActive: false,
		})
		env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
		env.provisioner = NewYunzhijiaProvisioner(
			env.cfg, env.users, env.members, env.employees, env.remover)
		env.login = NewYunzhijiaLoginService(
			env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)
		env.client.name = employee.Name

		resp, err := env.login.Login(ctx, "ticket-placeholder-004")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgAccountBlocked)
		assert.Equal(t, 0, env.tokens.createCalls, "停用账号不得签发 token")
		assert.Equal(t, 0, env.members.addCallCount(), "停用账号登录拒绝时不得调用 AddMember")
		assert.Equal(t, 0, env.employees.setUserIDCallCount(), "停用账号登录拒绝时不得回填 user_id")
	})

	t.Run("disabled by sync", func(t *testing.T) {
		userID := "sync-disabled-user-1"
		employee := yzjT03Employee("openid-sync-disabled-001", "测试员工辛", yzjT03TenantDefault)
		employee.UserID = &userID
		employee.DisabledBySync = true
		env := newYZJT03Env(yzjT03Config(), employee)
		env.users = newYZJT03UserRepo(&types.User{
			ID: userID, Username: "测试员工辛", Email: "sync-disabled@example.invalid",
			TenantID: yzjT03TenantDefault, IsActive: true,
		})
		env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
		env.provisioner = NewYunzhijiaProvisioner(
			env.cfg, env.users, env.members, env.employees, env.remover)
		env.login = NewYunzhijiaLoginService(
			env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)
		env.client.name = employee.Name

		resp, err := env.login.Login(ctx, "ticket-placeholder-005")
		yzjT03RequireRejected(t, resp, err, yzjT03MsgAccountBlocked)
		assert.Equal(t, 0, env.tokens.createCalls, "同步停用账号不得签发 token")
		assert.Equal(t, 0, env.members.addCallCount(), "同步停用账号登录拒绝时不得调用 AddMember")
		assert.Equal(t, 0, env.employees.setUserIDCallCount(), "同步停用账号登录拒绝时不得回填 user_id")
	})
}

func TestYunzhijiaT03LoginCreatesViewerSession(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-login-001", "测试员工甲", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)
	env.client.name = "测试员工甲"
	const ticket = "ticket-placeholder-login-001"

	resp, err := env.login.Login(ctx, ticket)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// AC-6：成功响应形状。
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.Token)
	assert.NotEmpty(t, resp.RefreshToken)
	require.NotNil(t, resp.User)
	assert.Equal(t, "yzj_openid-login-001@yunzhijia.local", resp.User.Email)
	assert.Empty(t, resp.User.PasswordHash)
	require.NotNil(t, resp.ActiveTenant)
	assert.Equal(t, yzjT03TenantDefault, resp.ActiveTenant.ID)
	require.Len(t, resp.Memberships, 1, "员工只应有一个空间成员关系")
	assert.Equal(t, yzjT03TenantDefault, resp.Memberships[0].TenantID)
	assert.Equal(t, types.TenantRoleViewer, resp.Memberships[0].Role)
	require.NotNil(t, resp.Capabilities)
	assert.False(t, resp.Capabilities.CanCreateTenant, "员工不得拥有建空间能力")
	assert.False(t, resp.Capabilities.AccessRevoked)

	// AC-4：
	assert.Equal(t, 1, env.users.created())
	assert.Equal(t, 1, env.members.addCallCount())
	assert.Equal(t, types.TenantRoleViewer, mustYZJT03Role(t, env.members, resp.User.ID, yzjT03TenantDefault))
	assert.Equal(t, 1, env.employees.setUserIDCallCount())
	assert.Equal(t, 0, env.tenants.createCallCount(), "不得创建租户")
	assert.Equal(t, 0, env.members.ensureOwnerCallCount(), "不得走 owner 开通")
	assert.Equal(t, 2, env.tokens.createCalls, "应写入 access + refresh 两条 token")

	// AC-16：响应不得回显 ticket。
	body, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(body), ticket)
}

func TestYunzhijiaT03LoginReusesExistingAccount(t *testing.T) {
	ctx := context.Background()
	userID := "existing-login-user-1"
	employee := yzjT03Employee("openid-login-existing-001", "测试员工壬", yzjT03TenantDefault)
	employee.UserID = &userID

	env := newYZJT03Env(yzjT03Config(), employee)
	env.client.name = employee.Name
	env.users = newYZJT03UserRepo(&types.User{
		ID: userID, Username: "测试员工壬", Email: "existing-login@example.invalid",
		TenantID: yzjT03TenantDefault, IsActive: true,
		Preferences: yzjT03EmployeePreferences(),
	})
	_, err := env.members.AddMember(ctx, userID, yzjT03TenantDefault, types.TenantRoleViewer, nil)
	require.NoError(t, err)
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)

	resp, err := env.login.Login(ctx, "ticket-placeholder-existing")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	require.NotNil(t, resp.User)
	assert.Equal(t, userID, resp.User.ID)
	assert.Empty(t, resp.User.PasswordHash, "员工账号密码保持为空，登录不得写入密码")
	assert.Equal(t, 0, env.users.created(), "已有账号不得重复建号")
	assert.Equal(t, 0, env.tenants.createCallCount())
}

// TestYunzhijiaT03LoginRejectsSyntheticAccountWithoutMarker 覆盖 F6 的
// reuseExistingAccount 面：合成邮箱命中的已有账号缺少员工标记时登录
// fail-closed，不补成员、不回填、不签 token、不新建账号。
func TestYunzhijiaT03LoginRejectsSyntheticAccountWithoutMarker(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-reuse-no-marker-001", "测试员工复用", yzjT03TenantDefault)
	const syntheticEmail = "yzj_openid-reuse-no-marker-001@yunzhijia.local"
	existing := &types.User{
		ID:       "existing-reuse-no-marker",
		Username: "历史占位账号",
		Email:    syntheticEmail,
		TenantID: yzjT03TenantDefault,
		IsActive: true,
	}

	env := newYZJT03Env(yzjT03Config(), employee)
	env.client.name = employee.Name
	env.users = newYZJT03UserRepo(existing)
	env.remover = &yzjT03Remover{repo: env.users}
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)

	resp, err := env.login.Login(ctx, "ticket-placeholder-reuse-no-marker")
	require.Error(t, err, "无员工标记的合成邮箱账号必须 fail-closed")
	require.ErrorIs(t, err, ErrYunzhijiaEmployeeMarkerMissing)
	assert.Nil(t, resp)
	assert.Equal(t, 0, env.members.addCallCount(), "不得把无标记账号拉进员工空间")
	assert.Equal(t, 0, env.employees.setUserIDCallCount(), "不得回填 user_id")
	assert.Equal(t, 0, env.tokens.createCalls, "不得签发 token")
	assert.Equal(t, 0, env.users.created(), "不得新建账号")
	assert.Equal(t, 0, env.users.updateCalls, "不得回写用户行")

	stored, getErr := env.users.GetUserByID(ctx, existing.ID)
	require.NoError(t, getErr)
	assert.Nil(t, stored.Preferences.YunzhijiaEmployee, "零写入：员工标记保持缺失")
	storedEmployee, getErr := env.employees.GetByOpenID(ctx, employee.OpenID)
	require.NoError(t, getErr)
	require.NotNil(t, storedEmployee)
	assert.Nil(t, storedEmployee.UserID, "员工行不得回填 user_id")
}

func TestYunzhijiaT03LoginFailsClosedWhenTargetTenantMissing(t *testing.T) {
	ctx := context.Background()
	employee := yzjT03Employee("openid-tenant-missing-001", "测试员工癸", yzjT03TenantDefault)
	env := newYZJT03Env(yzjT03Config(), employee)
	env.client.name = employee.Name
	env.tenants = newYZJT03TenantService() // 目标空间不存在
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		env.cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		env.cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)

	resp, err := env.login.Login(ctx, "ticket-placeholder-tenant-missing")
	require.True(t, err != nil || (resp != nil && !resp.Success),
		"目标空间不存在时必须 fail-closed，不能返回成功登录")
	if resp != nil {
		assert.Empty(t, resp.Token)
	}
}

func mustYZJT03Role(t *testing.T, members *yzjT03MemberService, userID string, tenantID uint64) types.TenantRole {
	t.Helper()
	role, ok := members.roleIn(userID, tenantID)
	require.True(t, ok, "缺少成员关系")
	return role
}
