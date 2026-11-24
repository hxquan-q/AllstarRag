package service

// T06 TDD 红阶段：账号生命周期（AC-10 / AC-11 / AC-12、INV-2/3/4/5）。
//
// 本轮锁定的实现契约（票面 + 01-spec §10.3–10.6 + 02-plan §8.3/§8.4）：
//   NewYunzhijiaLifecycle(cfg, provisioner, userRepo, memberService, employeeRepo)
//     (*YunzhijiaLifecycle).ApplyEmployee(ctx, *types.YunzhijiaEmployee)
//       (types.YunzhijiaAccountResult, error)
//     (*YunzhijiaLifecycle).ApplyInactive(ctx, *types.YunzhijiaEmployee)
//       (types.YunzhijiaAccountResult, error)
//     (*YunzhijiaLifecycle).MarkAdminDisabled(ctx, userID) error
//     (*YunzhijiaLifecycle).MarkBlockedAfterPurge(ctx, userID, actorID) error
//   *YunzhijiaLifecycle 满足 interfaces.YunzhijiaAccountLifecycle 与
//   interfaces.YunzhijiaEmployeeState。
//
// 约定：
//   - blocked 员工直接跳过：不写账号/成员关系/员工状态，返回 nil；
//   - 管理员封禁（IsActive=false 且 disabled_by_sync=false）返回
//     ErrYunzhijiaAccountBanned，不自动解封；
//   - 关联本地用户缺失返回 ErrYunzhijiaLocalUserMissing。
//
// token 吊销由真实 RemoveMember / SetUserActive 仓储完成；本轮替身只验证
// 生命周期确实调用了这些入口（见 05-tests 15.7）。
// 所有账号、姓名、open_id 均为占位数据，不使用真实员工信息。

import (
	"context"
	"errors"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

const (
	yzjT06TenantDefault uint64 = 10024
	yzjT06TenantHR      uint64 = 10022
)

func yzjT06Config(enabled bool) *config.YunzhijiaSSOConfig {
	return &config.YunzhijiaSSOConfig{
		Enabled:             enabled,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/employees",
		TicketUserPath:      "/queryUserName",
		AppID:               "app-placeholder",
		TenantDefault:       yzjT06TenantDefault,
		TenantHR:            yzjT06TenantHR,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            500,
		HTTPTimeoutSeconds:  10,
		StartupCatchupHours: 24,
	}
}

func yzjT06Ptr(s string) *string { return &s }

// T08 起生命周期接口返回 (结果, error)；下列辅助保持 T06 用例原逻辑，
// 只把单返回值断言适配为双返回值。
func yzjT06ApplyEmployeeOK(
	t *testing.T, lifecycle *YunzhijiaLifecycle, employee *types.YunzhijiaEmployee,
) {
	t.Helper()
	_, err := lifecycle.ApplyEmployee(context.Background(), employee)
	require.NoError(t, err)
}

func yzjT06ApplyInactiveOK(
	t *testing.T, lifecycle *YunzhijiaLifecycle, employee *types.YunzhijiaEmployee,
) {
	t.Helper()
	_, err := lifecycle.ApplyInactive(context.Background(), employee)
	require.NoError(t, err)
}

func yzjT06Employee(openID, name string, tenantID uint64) *types.YunzhijiaEmployee {
	return &types.YunzhijiaEmployee{
		OpenID:       openID,
		Name:         name,
		Department:   "占位部门",
		TenantID:     tenantID,
		Status:       types.YunzhijiaEmployeeStatusActive,
		LastSyncedAt: time.Now(),
	}
}

func yzjT06CloneEmployee(e *types.YunzhijiaEmployee) *types.YunzhijiaEmployee {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

// ---------------------------------------------------------------------------
// 用户仓储替身
// ---------------------------------------------------------------------------

type yzjT06SetActiveCall struct {
	UserID string
	Actor  string
	Active bool
}

type yzjT06UserRepo struct {
	interfaces.UserRepository

	users          map[string]*types.User
	getErr         map[string]error
	createCalls    []*types.User
	setActiveCalls []yzjT06SetActiveCall
	updateCalls    []*types.User
}

func newYZJT06UserRepo() *yzjT06UserRepo {
	return &yzjT06UserRepo{
		users:  map[string]*types.User{},
		getErr: map[string]error{},
	}
}

func (r *yzjT06UserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if err, ok := r.getErr[id]; ok {
		return nil, err
	}
	user, ok := r.users[id]
	if !ok {
		return nil, apprepo.ErrUserNotFound
	}
	return user, nil
}

func (r *yzjT06UserRepo) GetUserByEmail(_ context.Context, _ string) (*types.User, error) {
	return nil, apprepo.ErrUserNotFound
}

func (r *yzjT06UserRepo) CreateUser(_ context.Context, user *types.User) error {
	r.createCalls = append(r.createCalls, user)
	r.users[user.ID] = user
	return nil
}

func (r *yzjT06UserRepo) SetUserActive(
	_ context.Context, userID, actorID string, active bool,
) (*types.User, error) {
	r.setActiveCalls = append(r.setActiveCalls, yzjT06SetActiveCall{
		UserID: userID,
		Actor:  actorID,
		Active: active,
	})
	user, ok := r.users[userID]
	if !ok {
		return &types.User{ID: userID, IsActive: active}, nil
	}
	user.IsActive = active
	return user, nil
}

func (r *yzjT06UserRepo) UpdateUser(_ context.Context, user *types.User) error {
	r.updateCalls = append(r.updateCalls, user)
	r.users[user.ID] = user
	return nil
}

// ---------------------------------------------------------------------------
// 成员服务替身
// ---------------------------------------------------------------------------

type yzjT06MemberCall struct {
	UserID   string
	TenantID uint64
	Role     types.TenantRole
}

type yzjT06MemberService struct {
	interfaces.TenantMemberService

	members     []*types.TenantMember
	addCalls    []yzjT06MemberCall
	removeCalls []yzjT06MemberCall
	events      []string

	getMembershipErr error
	addErr           error
	removeErr        error
}

func newYZJT06MemberService() *yzjT06MemberService {
	return &yzjT06MemberService{}
}

func (m *yzjT06MemberService) GetMembership(
	_ context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	if m.getMembershipErr != nil {
		return nil, m.getMembershipErr
	}
	for _, member := range m.members {
		if member.UserID == userID && member.TenantID == tenantID {
			return member, nil
		}
	}
	return nil, nil
}

func (m *yzjT06MemberService) ListByUser(
	_ context.Context, userID string,
) ([]*types.TenantMember, error) {
	out := make([]*types.TenantMember, 0, len(m.members))
	for _, member := range m.members {
		if member.UserID == userID {
			out = append(out, member)
		}
	}
	return out, nil
}

func (m *yzjT06MemberService) AddMember(
	_ context.Context, userID string, tenantID uint64,
	role types.TenantRole, _ *string,
) (*types.TenantMember, error) {
	if m.addErr != nil {
		return nil, m.addErr
	}
	m.addCalls = append(m.addCalls, yzjT06MemberCall{
		UserID: userID, TenantID: tenantID, Role: role,
	})
	m.events = append(m.events, "add")
	member := &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	}
	m.members = append(m.members, member)
	return member, nil
}

func (m *yzjT06MemberService) RemoveMember(
	_ context.Context, userID string, tenantID uint64,
) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	m.removeCalls = append(m.removeCalls, yzjT06MemberCall{
		UserID: userID, TenantID: tenantID,
	})
	m.events = append(m.events, "remove")
	kept := m.members[:0]
	for _, member := range m.members {
		if member.UserID == userID && member.TenantID == tenantID {
			continue
		}
		kept = append(kept, member)
	}
	m.members = kept
	return nil
}

func (m *yzjT06MemberService) hasMembership(userID string, tenantID uint64) bool {
	for _, member := range m.members {
		if member.UserID == userID && member.TenantID == tenantID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 员工仓储替身
// ---------------------------------------------------------------------------

type yzjT06DisabledCall struct {
	OpenID   string
	Disabled bool
}

type yzjT06MarkCall struct {
	OpenID string
	Actor  string
}

type yzjT06SetUserIDCall struct {
	OpenID string
	UserID string
}

type yzjT06EmployeeRepo struct {
	interfaces.YunzhijiaEmployeeRepository

	byOpenID map[string]*types.YunzhijiaEmployee

	setDisabledCalls []yzjT06DisabledCall
	markBlockedCalls []yzjT06MarkCall
	setUserIDCalls   []yzjT06SetUserIDCall
	getByUserErr     error
	setDisabledErr   error
	markBlockedErr   error
}

func newYZJT06EmployeeRepo(rows ...*types.YunzhijiaEmployee) *yzjT06EmployeeRepo {
	repo := &yzjT06EmployeeRepo{byOpenID: map[string]*types.YunzhijiaEmployee{}}
	for _, row := range rows {
		repo.byOpenID[row.OpenID] = yzjT06CloneEmployee(row)
	}
	return repo
}

func (r *yzjT06EmployeeRepo) GetByOpenID(
	_ context.Context, openID string,
) (*types.YunzhijiaEmployee, error) {
	return yzjT06CloneEmployee(r.byOpenID[openID]), nil
}

func (r *yzjT06EmployeeRepo) GetByUserID(
	_ context.Context, userID string,
) (*types.YunzhijiaEmployee, error) {
	if r.getByUserErr != nil {
		return nil, r.getByUserErr
	}
	for _, row := range r.byOpenID {
		if row.UserID != nil && *row.UserID == userID {
			return yzjT06CloneEmployee(row), nil
		}
	}
	return nil, nil
}

func (r *yzjT06EmployeeRepo) SetDisabledBySync(
	_ context.Context, openID string, disabled bool,
) error {
	if r.setDisabledErr != nil {
		return r.setDisabledErr
	}
	r.setDisabledCalls = append(r.setDisabledCalls, yzjT06DisabledCall{
		OpenID: openID, Disabled: disabled,
	})
	if row := r.byOpenID[openID]; row != nil {
		row.DisabledBySync = disabled
	}
	return nil
}

func (r *yzjT06EmployeeRepo) MarkBlocked(
	_ context.Context, openID, blockedBy string,
) error {
	if r.markBlockedErr != nil {
		return r.markBlockedErr
	}
	r.markBlockedCalls = append(r.markBlockedCalls, yzjT06MarkCall{
		OpenID: openID, Actor: blockedBy,
	})
	if row := r.byOpenID[openID]; row != nil {
		// 与真实仓储对齐：MarkBlocked 保留 user_id 供审计，Unblock 时才清。
		row.Blocked = true
	}
	return nil
}

func (r *yzjT06EmployeeRepo) SetUserID(
	_ context.Context, openID, userID string,
) error {
	r.setUserIDCalls = append(r.setUserIDCalls, yzjT06SetUserIDCall{
		OpenID: openID, UserID: userID,
	})
	if row := r.byOpenID[openID]; row != nil {
		row.UserID = yzjT06Ptr(userID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 员工状态 / 删除钩子替身
// ---------------------------------------------------------------------------

type yzjT06State struct {
	adminCalls   []string
	blockedCalls []yzjT06MarkCall

	adminErr   error
	blockedErr error
	// onMarkBlocked 在 MarkBlockedAfterPurge 内执行，便于断言「先标记后 purge」。
	onMarkBlocked func()
}

func (s *yzjT06State) MarkAdminDisabled(_ context.Context, userID string) error {
	s.adminCalls = append(s.adminCalls, userID)
	return s.adminErr
}

func (s *yzjT06State) MarkBlockedAfterPurge(
	_ context.Context, userID, actorID string,
) error {
	s.blockedCalls = append(s.blockedCalls, yzjT06MarkCall{
		OpenID: userID, Actor: actorID,
	})
	if s.onMarkBlocked != nil {
		s.onMarkBlocked()
	}
	return s.blockedErr
}

// ---------------------------------------------------------------------------
// 新建账号回滚器替身
// ---------------------------------------------------------------------------

type yzjT06Remover struct {
	calls []string
}

func (r *yzjT06Remover) RemoveNewlyCreatedUser(_ context.Context, id string) error {
	r.calls = append(r.calls, id)
	return nil
}

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

func yzjT06NewLifecycle(
	cfg *config.YunzhijiaSSOConfig,
	users *yzjT06UserRepo,
	members *yzjT06MemberService,
	employees *yzjT06EmployeeRepo,
) *YunzhijiaLifecycle {
	provisioner := NewYunzhijiaProvisioner(cfg, users, members, employees, &yzjT06Remover{})
	return NewYunzhijiaLifecycle(cfg, provisioner, users, members, employees)
}

func yzjT06SeedActiveUser(users *yzjT06UserRepo, userID string) *types.User {
	user := &types.User{
		ID:           userID,
		Username:     userID,
		Email:        userID + "@example.com",
		PasswordHash: "",
		TenantID:     yzjT06TenantDefault,
		IsActive:     true,
	}
	users.users[userID] = user
	return user
}

func yzjT06SeedMembership(
	members *yzjT06MemberService, userID string, tenantID uint64, role types.TenantRole,
) {
	members.members = append(members.members, &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	})
}

func yzjT06RequireNoMutations(
	t *testing.T,
	users *yzjT06UserRepo,
	members *yzjT06MemberService,
	employees *yzjT06EmployeeRepo,
) {
	t.Helper()
	require.Empty(t, users.createCalls, "不应创建账号")
	require.Empty(t, users.setActiveCalls, "不应改账号状态")
	require.Empty(t, users.updateCalls, "不应更新账号")
	require.Empty(t, members.addCalls, "不应加入空间")
	require.Empty(t, members.removeCalls, "不应移除空间")
	require.Empty(t, employees.setDisabledCalls, "不应改 disabled_by_sync")
	require.Empty(t, employees.markBlockedCalls, "不应标记 blocked")
}

// ---------------------------------------------------------------------------
// ApplyEmployee
// ---------------------------------------------------------------------------

func TestYunzhijiaT06ApplyEmployeeNewAccountProvisionsViewer(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	employee := yzjT06Employee("open-new-0001", "占位员工甲", yzjT06TenantDefault)
	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, users.createCalls, 1, "新员工必须走 provisioner 建号")
	created := users.createCalls[0]
	require.True(t, created.IsActive)
	require.Empty(t, created.PasswordHash, "员工账号不得有密码")
	require.Equal(t, yzjT06TenantDefault, created.TenantID)

	require.Len(t, members.addCalls, 1, "新员工必须加入目标空间")
	require.Equal(t, created.ID, members.addCalls[0].UserID)
	require.Equal(t, yzjT06TenantDefault, members.addCalls[0].TenantID)
	require.Equal(t, types.TenantRoleViewer, members.addCalls[0].Role)

	require.Len(t, employees.setUserIDCalls, 1, "开号后必须回填 user_id")
	require.Equal(t, employee.OpenID, employees.setUserIDCalls[0].OpenID)
	require.Equal(t, created.ID, employees.setUserIDCalls[0].UserID)
	require.Empty(t, users.setActiveCalls, "新建账号不应再调 SetUserActive")
}

func TestYunzhijiaT06ApplyEmployeeExistingActiveEnsuresViewer(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-existing"
	user := yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-existing-0001", "占位员工乙", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, users.createCalls, 0, "已有账号不应重复建号")
	require.True(t, members.hasMembership(userID, yzjT06TenantDefault), "应保留 viewer 成员关系")
	require.Len(t, members.addCalls, 0, "已是 viewer 不应重复加入")
	require.Empty(t, members.removeCalls, "同空间不应移除成员")
	require.Empty(t, users.setActiveCalls, "在职账号不应改状态")
	require.True(t, user.IsActive)
}

func TestYunzhijiaT06ApplyEmployeeActiveWithoutMembershipAddsViewer(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-no-membership"
	yzjT06SeedActiveUser(users, userID)
	employee := yzjT06Employee("open-no-membership-0001", "占位员工午", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, members.addCalls, 1, "在职账号缺少成员关系时必须补 viewer")
	require.Equal(t, userID, members.addCalls[0].UserID)
	require.Equal(t, yzjT06TenantDefault, members.addCalls[0].TenantID)
	require.Equal(t, types.TenantRoleViewer, members.addCalls[0].Role)
	require.Empty(t, members.removeCalls, "同空间补齐不应移除成员")
	require.Empty(t, users.setActiveCalls, "在职账号不应改状态")
}

func TestYunzhijiaT06ApplyEmployeeRehireRestoresAccount(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-rehire"
	yzjT06SeedActiveUser(users, userID).IsActive = false
	employee := yzjT06Employee("open-rehire-0001", "占位员工丙", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.DisabledBySync = true

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, users.setActiveCalls, 1, "同步停用的账号再入职必须重新启用")
	require.Equal(t, userID, users.setActiveCalls[0].UserID)
	require.True(t, users.setActiveCalls[0].Active)

	require.Len(t, members.addCalls, 1, "再入职必须回到目标空间")
	require.Equal(t, userID, members.addCalls[0].UserID)
	require.Equal(t, yzjT06TenantDefault, members.addCalls[0].TenantID)
	require.Equal(t, types.TenantRoleViewer, members.addCalls[0].Role)

	require.Len(t, employees.setDisabledCalls, 1, "恢复后必须清掉 disabled_by_sync")
	require.Equal(t, employee.OpenID, employees.setDisabledCalls[0].OpenID)
	require.False(t, employees.setDisabledCalls[0].Disabled)
	require.Empty(t, members.removeCalls, "同空间再入职不应移除成员")
}

// M2：在职但 disabled_by_sync=true（如管理员先封禁再解封、上一轮离职停用后
// 人工恢复）必须走恢复路径清标记，否则登录侧会永久拒绝。
func TestYunzhijiaT06ApplyEmployeeActiveRehireClearsDisabledFlag(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()

	userID := "u-t06-rehire-flag"
	user := yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-rehire-flag-0001", "占位员工未", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.DisabledBySync = true
	employees := newYZJT06EmployeeRepo(employee)
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, users.setActiveCalls, 1, "标记为 true 时即使账号 active 也必须走恢复路径")
	require.True(t, users.setActiveCalls[0].Active)
	require.True(t, user.IsActive, "恢复后账号必须保持可用")
	require.Empty(t, users.createCalls, "已有账号不应重复建号")

	require.Len(t, employees.setDisabledCalls, 1, "恢复后必须清掉 disabled_by_sync，登录才不会被拒")
	require.Equal(t, employee.OpenID, employees.setDisabledCalls[0].OpenID)
	require.False(t, employees.setDisabledCalls[0].Disabled)
	require.False(t, employees.byOpenID[employee.OpenID].DisabledBySync,
		"仓储中的标记必须已清空（登录读取的就是这一列）")

	require.Empty(t, members.addCalls, "已在本空间不应重复加入")
	require.Empty(t, members.removeCalls)
}

func TestYunzhijiaT06ApplyEmployeeTransferMovesSpace(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-transfer"
	yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)

	employee := yzjT06Employee("open-transfer-0001", "占位员工丁", yzjT06TenantHR)
	employee.UserID = yzjT06Ptr(userID)

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)

	require.Len(t, members.removeCalls, 1, "调岗必须先移除旧空间")
	require.Equal(t, yzjT06TenantDefault, members.removeCalls[0].TenantID)
	require.Len(t, members.addCalls, 1, "调岗必须加入新空间")
	require.Equal(t, yzjT06TenantHR, members.addCalls[0].TenantID)
	require.Equal(t, types.TenantRoleViewer, members.addCalls[0].Role)

	require.Len(t, members.events, 2, "调岗只应有一移一加")
	require.Equal(t, "remove", members.events[0], "必须先移除旧空间再加新空间")
	require.Equal(t, "add", members.events[1])
	require.False(t, members.hasMembership(userID, yzjT06TenantDefault), "旧空间成员关系必须移除")
	require.True(t, members.hasMembership(userID, yzjT06TenantHR))
}

func TestYunzhijiaT06ApplyEmployeeBlockedIsSkipped(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	employee := yzjT06Employee("open-blocked-0001", "占位员工戊", yzjT06TenantDefault)
	employee.Blocked = true

	yzjT06ApplyEmployeeOK(t, lifecycle, employee)
	yzjT06RequireNoMutations(t, users, members, employees)
}

func TestYunzhijiaT06ApplyEmployeeAdminBannedNotUnbanned(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-banned"
	yzjT06SeedActiveUser(users, userID).IsActive = false
	employee := yzjT06Employee("open-banned-0001", "占位员工己", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.DisabledBySync = false

	_, err := lifecycle.ApplyEmployee(context.Background(), employee)
	require.ErrorIs(t, err, ErrYunzhijiaAccountBanned)
	require.Empty(t, users.setActiveCalls, "管理员封禁不得自动解封")
	require.Empty(t, members.addCalls, "管理员封禁不得自动入组")
	require.Empty(t, employees.setDisabledCalls, "管理员封禁不得改写 disabled_by_sync")
}

func TestYunzhijiaT06ApplyEmployeeMissingLocalUser(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-missing"
	users.getErr[userID] = apprepo.ErrUserNotFound
	employee := yzjT06Employee("open-missing-0001", "占位员工庚", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)

	_, err := lifecycle.ApplyEmployee(context.Background(), employee)
	require.ErrorIs(t, err, ErrYunzhijiaLocalUserMissing)
	yzjT06RequireNoMutations(t, users, members, employees)
}

// ---------------------------------------------------------------------------
// ApplyInactive
// ---------------------------------------------------------------------------

func TestYunzhijiaT06ApplyInactiveDisablesAndRevokes(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-leave"
	yzjT06SeedActiveUser(users, userID)
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-leave-0001", "占位员工辛", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive

	yzjT06ApplyInactiveOK(t, lifecycle, employee)

	require.Len(t, users.setActiveCalls, 1, "离职必须停用账号（仓储内同时吊销 token）")
	require.Equal(t, userID, users.setActiveCalls[0].UserID)
	require.False(t, users.setActiveCalls[0].Active)

	require.Len(t, members.removeCalls, 1, "离职必须移除成员关系（仓储内吊销 token）")
	require.Equal(t, userID, members.removeCalls[0].UserID)
	require.Equal(t, yzjT06TenantDefault, members.removeCalls[0].TenantID)

	require.Len(t, employees.setDisabledCalls, 1)
	require.Equal(t, employee.OpenID, employees.setDisabledCalls[0].OpenID)
	require.True(t, employees.setDisabledCalls[0].Disabled, "同步停用必须写 disabled_by_sync=true")

	require.Empty(t, users.createCalls, "离职不应建号")
	require.Empty(t, members.addCalls, "离职不应加入空间")
}

func TestYunzhijiaT06ApplyInactiveBlockedUnchanged(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-blocked-leave"
	yzjT06SeedActiveUser(users, userID)
	employee := yzjT06Employee("open-blocked-leave-0001", "占位员工壬", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive
	employee.Blocked = true

	yzjT06ApplyInactiveOK(t, lifecycle, employee)
	yzjT06RequireNoMutations(t, users, members, employees)
}

func TestYunzhijiaT06ApplyInactiveAdminBannedUnchanged(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-banned-leave"
	yzjT06SeedActiveUser(users, userID).IsActive = false
	yzjT06SeedMembership(members, userID, yzjT06TenantDefault, types.TenantRoleViewer)
	employee := yzjT06Employee("open-banned-leave-0001", "占位员工癸", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive
	employee.DisabledBySync = false

	yzjT06ApplyInactiveOK(t, lifecycle, employee)
	require.Empty(t, users.setActiveCalls, "已封禁账号不应重复停用")
	require.Empty(t, members.removeCalls, "已封禁账号成员关系不变")
	require.Empty(t, employees.setDisabledCalls, "已封禁账号不得写 disabled_by_sync")
}

func TestYunzhijiaT06ApplyInactiveMissingLocalUser(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(true), users, members, employees)

	userID := "u-t06-leave-missing"
	users.getErr[userID] = apprepo.ErrUserNotFound
	employee := yzjT06Employee("open-leave-missing-0001", "占位员工子", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.Status = types.YunzhijiaEmployeeStatusInactive

	_, err := lifecycle.ApplyInactive(context.Background(), employee)
	require.ErrorIs(t, err, ErrYunzhijiaLocalUserMissing)
	yzjT06RequireNoMutations(t, users, members, employees)
}

// ---------------------------------------------------------------------------
// YunzhijiaEmployeeState（生命周期自身实现，不受 enabled 门控）
// ---------------------------------------------------------------------------

func TestYunzhijiaT06MarkAdminDisabled(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	// enabled=false 也必须生效：钩子不受同步开关门控。
	lifecycle := yzjT06NewLifecycle(yzjT06Config(false), users, members, employees)

	userID := "u-t06-mark-admin"
	employee := yzjT06Employee("open-mark-admin-0001", "占位员工丑", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employee.DisabledBySync = true
	employees.byOpenID[employee.OpenID] = yzjT06CloneEmployee(employee)

	require.NoError(t, lifecycle.MarkAdminDisabled(context.Background(), userID))
	require.Len(t, employees.setDisabledCalls, 1)
	require.Equal(t, employee.OpenID, employees.setDisabledCalls[0].OpenID)
	require.False(t, employees.setDisabledCalls[0].Disabled, "管理员封禁必须把 disabled_by_sync 置 false")
}

func TestYunzhijiaT06MarkAdminDisabledUnknownUserIsNoop(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(false), users, members, employees)

	require.NoError(t, lifecycle.MarkAdminDisabled(context.Background(), "u-not-employee"))
	require.Empty(t, employees.setDisabledCalls, "非员工账号不应触碰员工表")
}

func TestYunzhijiaT06MarkBlockedAfterPurge(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(false), users, members, employees)

	userID := "u-t06-mark-blocked"
	employee := yzjT06Employee("open-mark-blocked-0001", "占位员工寅", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employees.byOpenID[employee.OpenID] = yzjT06CloneEmployee(employee)

	require.NoError(t, lifecycle.MarkBlockedAfterPurge(context.Background(), userID, "actor-placeholder"))
	require.Len(t, employees.markBlockedCalls, 1)
	require.Equal(t, employee.OpenID, employees.markBlockedCalls[0].OpenID)
	require.Equal(t, "actor-placeholder", employees.markBlockedCalls[0].Actor)
}

func TestYunzhijiaT06MarkBlockedAfterPurgeUnknownUserIsNoop(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(false), users, members, employees)

	require.NoError(t, lifecycle.MarkBlockedAfterPurge(context.Background(), "u-not-employee", "actor"))
	require.Empty(t, employees.markBlockedCalls, "非员工账号不应被标记 blocked")
}

func TestYunzhijiaT06StateHookErrorsPropagate(t *testing.T) {
	users := newYZJT06UserRepo()
	members := newYZJT06MemberService()
	employees := newYZJT06EmployeeRepo()
	lifecycle := yzjT06NewLifecycle(yzjT06Config(false), users, members, employees)

	userID := "u-t06-hook-error"
	employee := yzjT06Employee("open-hook-error-0001", "占位员工卯", yzjT06TenantDefault)
	employee.UserID = yzjT06Ptr(userID)
	employees.byOpenID[employee.OpenID] = yzjT06CloneEmployee(employee)

	boom := errors.New("employee repo unavailable")
	employees.setDisabledErr = boom
	require.ErrorIs(t, lifecycle.MarkAdminDisabled(context.Background(), userID), boom)

	employees.markBlockedErr = boom
	require.ErrorIs(t, lifecycle.MarkBlockedAfterPurge(context.Background(), userID, "actor"), boom)
}
