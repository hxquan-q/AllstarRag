package service

// T03 TDD 红阶段的测试替身。只落在 _test.go 里，不提供任何生产实现。
//
// 红阶段契约（实现方按此对齐，见 05-tests.md）：
//   NewYunzhijiaProvisioner(cfg, userRepo, memberService, employeeRepo, remover)
//   (*YunzhijiaProvisioner).EnsureAccount(ctx, *types.YunzhijiaEmployee) (*types.User, error)
//   NewYunzhijiaLoginService(cfg, client, employeeRepo, provisioner, userService, tenantService)
//   (*YunzhijiaLoginService).Login(ctx, ticket) (*types.LoginResponse, error)
//
// 所有姓名 / open_id / ticket 均为占位符，不使用真实员工数据。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

const (
	yzjT03TenantDefault = uint64(10024)
	yzjT03TenantHR      = uint64(10022)
)

func yzjT03Config() *config.YunzhijiaSSOConfig {
	return &config.YunzhijiaSSOConfig{
		Enabled:             true,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/employees",
		TicketUserPath:      "/queryUserName",
		AppID:               "app-placeholder",
		TenantDefault:       yzjT03TenantDefault,
		TenantHR:            yzjT03TenantHR,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            500,
		HTTPTimeoutSeconds:  10,
		StartupCatchupHours: 24,
	}
}

// ---------------------------------------------------------------------------
// 云之家客户端替身
// ---------------------------------------------------------------------------

type yzjT03Client struct {
	name    string
	err     error
	calls   int
	tickets []string
}

func (c *yzjT03Client) QueryUserName(_ context.Context, ticket string) (string, error) {
	c.calls++
	c.tickets = append(c.tickets, ticket)
	if c.err != nil {
		return "", c.err
	}
	return c.name, nil
}

func (c *yzjT03Client) ListEmployees(context.Context, yunzhijia.Status, int, int) (*yunzhijia.EmployeePage, error) {
	return nil, errors.New("T03 登录用例不应调用员工列表接口")
}

// ---------------------------------------------------------------------------
// 员工仓储替身
// ---------------------------------------------------------------------------

type yzjT03SetUserIDCall struct {
	OpenID string
	UserID string
}

type yzjT03EmployeeRepo struct {
	interfaces.YunzhijiaEmployeeRepository

	mu             sync.Mutex
	byOpenID       map[string]*types.YunzhijiaEmployee
	setUserIDCalls []yzjT03SetUserIDCall
}

func newYZJT03EmployeeRepo(employees ...*types.YunzhijiaEmployee) *yzjT03EmployeeRepo {
	r := &yzjT03EmployeeRepo{byOpenID: map[string]*types.YunzhijiaEmployee{}}
	for _, e := range employees {
		r.byOpenID[e.OpenID] = cloneYZJT03Employee(e)
	}
	return r
}

func cloneYZJT03Employee(e *types.YunzhijiaEmployee) *types.YunzhijiaEmployee {
	if e == nil {
		return nil
	}
	cp := *e
	if e.UserID != nil {
		id := *e.UserID
		cp.UserID = &id
	}
	return &cp
}

func (r *yzjT03EmployeeRepo) FindActiveByName(_ context.Context, name string) ([]*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*types.YunzhijiaEmployee
	openIDs := make([]string, 0, len(r.byOpenID))
	for openID := range r.byOpenID {
		openIDs = append(openIDs, openID)
	}
	sort.Strings(openIDs)
	for _, openID := range openIDs {
		e := r.byOpenID[openID]
		if e.Name == name && e.Status == types.YunzhijiaEmployeeStatusActive {
			out = append(out, cloneYZJT03Employee(e))
		}
	}
	return out, nil
}

func (r *yzjT03EmployeeRepo) GetByOpenID(_ context.Context, openID string) (*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneYZJT03Employee(r.byOpenID[openID]), nil
}

// SetUserID 是 T03 期望新增的回填入口：开号成功后把 users.id 写回员工行。
func (r *yzjT03EmployeeRepo) SetUserID(_ context.Context, openID string, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byOpenID[openID]
	if !ok {
		return fmt.Errorf("employee %s not found", openID)
	}
	id := userID
	e.UserID = &id
	r.setUserIDCalls = append(r.setUserIDCalls, yzjT03SetUserIDCall{OpenID: openID, UserID: userID})
	return nil
}

func (r *yzjT03EmployeeRepo) setUserIDCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.setUserIDCalls)
}

// ---------------------------------------------------------------------------
// 用户仓储替身（带 email / username 唯一约束）
// ---------------------------------------------------------------------------

type yzjT03UserRepo struct {
	interfaces.UserRepository

	mu             sync.Mutex
	users          map[string]*types.User
	byEmail        map[string]string
	byUsername     map[string]string
	nextID         int
	createCalls    int
	updateCalls    int
	hardDeleteCall int
}

func newYZJT03UserRepo(users ...*types.User) *yzjT03UserRepo {
	r := &yzjT03UserRepo{
		users:      map[string]*types.User{},
		byEmail:    map[string]string{},
		byUsername: map[string]string{},
	}
	for _, u := range users {
		r.storeLocked(u)
	}
	return r
}

func (r *yzjT03UserRepo) storeLocked(u *types.User) {
	cp := *u
	cp.Preferences = u.Preferences
	r.users[cp.ID] = &cp
	if cp.Email != "" {
		r.byEmail[cp.Email] = cp.ID
	}
	if cp.Username != "" {
		r.byUsername[cp.Username] = cp.ID
	}
}

func (r *yzjT03UserRepo) CreateUser(_ context.Context, user *types.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	if _, ok := r.byEmail[user.Email]; ok {
		return gorm.ErrDuplicatedKey
	}
	if _, ok := r.byUsername[user.Username]; ok {
		return gorm.ErrDuplicatedKey
	}
	r.nextID++
	user.ID = fmt.Sprintf("yzj-user-%d", r.nextID)
	r.storeLocked(user)
	return nil
}

func (r *yzjT03UserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *yzjT03UserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byEmail[email]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	u := r.users[id]
	cp := *u
	return &cp, nil
}

func (r *yzjT03UserRepo) GetUserByUsername(_ context.Context, username string) (*types.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byUsername[username]
	if !ok {
		return nil, nil
	}
	u := r.users[id]
	cp := *u
	return &cp, nil
}

func (r *yzjT03UserRepo) UpdateUser(_ context.Context, user *types.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	r.storeLocked(user)
	return nil
}

func (r *yzjT03UserRepo) HardDeleteUser(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hardDeleteCall++
	u, ok := r.users[id]
	if !ok {
		return nil
	}
	delete(r.byEmail, u.Email)
	delete(r.byUsername, u.Username)
	delete(r.users, id)
	return nil
}

func (r *yzjT03UserRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.users)
}

func (r *yzjT03UserRepo) created() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createCalls
}

// ---------------------------------------------------------------------------
// 成员服务替身
// ---------------------------------------------------------------------------

type yzjT03MemberService struct {
	interfaces.TenantMemberService

	mu               sync.Mutex
	rows             map[string]map[uint64]*types.TenantMember
	addCalls         int
	ensureOwnerCalls int
	failAdd          error
}

func newYZJT03MemberService() *yzjT03MemberService {
	return &yzjT03MemberService{rows: map[string]map[uint64]*types.TenantMember{}}
}

func (m *yzjT03MemberService) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byTenant, ok := m.rows[userID]
	if !ok {
		return nil, nil
	}
	row, ok := byTenant[tenantID]
	if !ok {
		return nil, nil
	}
	cp := *row
	return &cp, nil
}

func (m *yzjT03MemberService) AddMember(
	_ context.Context, userID string, tenantID uint64, role types.TenantRole, invitedBy *string,
) (*types.TenantMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls++
	if m.failAdd != nil {
		return nil, m.failAdd
	}
	if byTenant, ok := m.rows[userID]; ok {
		if _, exists := byTenant[tenantID]; exists {
			return nil, errors.New("tenant member already exists")
		}
	}
	row := &types.TenantMember{
		UserID:    userID,
		TenantID:  tenantID,
		Role:      role,
		Status:    types.TenantMemberStatusActive,
		InvitedBy: invitedBy,
		JoinedAt:  time.Now(),
	}
	if m.rows[userID] == nil {
		m.rows[userID] = map[uint64]*types.TenantMember{}
	}
	m.rows[userID][tenantID] = row
	cp := *row
	return &cp, nil
}

func (m *yzjT03MemberService) ListByUser(_ context.Context, userID string) ([]*types.TenantMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byTenant := m.rows[userID]
	tenantIDs := make([]uint64, 0, len(byTenant))
	for tenantID := range byTenant {
		tenantIDs = append(tenantIDs, tenantID)
	}
	sort.Slice(tenantIDs, func(i, j int) bool { return tenantIDs[i] < tenantIDs[j] })
	out := make([]*types.TenantMember, 0, len(tenantIDs))
	for _, tenantID := range tenantIDs {
		row := byTenant[tenantID]
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (m *yzjT03MemberService) EnsureOwner(context.Context, string, uint64) (*types.TenantMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureOwnerCalls++
	return nil, errors.New("T03 员工开号不得调用 EnsureOwner")
}

func (m *yzjT03MemberService) ensureOwnerCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureOwnerCalls
}

func (m *yzjT03MemberService) addCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addCalls
}

func (m *yzjT03MemberService) roleIn(userID string, tenantID uint64) (types.TenantRole, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byTenant, ok := m.rows[userID]
	if !ok {
		return "", false
	}
	row, ok := byTenant[tenantID]
	if !ok {
		return "", false
	}
	return row.Role, true
}

// ---------------------------------------------------------------------------
// 空间服务替身
// ---------------------------------------------------------------------------

type yzjT03TenantService struct {
	interfaces.TenantService

	tenants     map[uint64]*types.Tenant
	getErr      error
	createCalls int
}

func newYZJT03TenantService(tenantIDs ...uint64) *yzjT03TenantService {
	s := &yzjT03TenantService{tenants: map[uint64]*types.Tenant{}}
	for _, id := range tenantIDs {
		s.tenants[id] = &types.Tenant{ID: id, Name: fmt.Sprintf("space-%d", id), Status: "active"}
	}
	return s
}

func (s *yzjT03TenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	t, ok := s.tenants[id]
	if !ok {
		return nil, fmt.Errorf("tenant %d not found", id)
	}
	cp := *t
	return &cp, nil
}

func (s *yzjT03TenantService) CreateTenant(context.Context, *types.Tenant) (*types.Tenant, error) {
	s.createCalls++
	return nil, errors.New("T03 员工登录不得创建空间")
}

func (s *yzjT03TenantService) createCallCount() int {
	return s.createCalls
}

// ---------------------------------------------------------------------------
// 新用户回滚替身
// ---------------------------------------------------------------------------

type yzjT03Remover struct {
	repo    *yzjT03UserRepo
	removed []string
	err     error
}

func (r *yzjT03Remover) RemoveNewlyCreatedUser(ctx context.Context, id string) error {
	r.removed = append(r.removed, id)
	if r.err != nil {
		return r.err
	}
	return r.repo.HardDeleteUser(ctx, id)
}

// ---------------------------------------------------------------------------
// 登录服务测试环境
// ---------------------------------------------------------------------------

type yzjT03Env struct {
	cfg         *config.YunzhijiaSSOConfig
	client      *yzjT03Client
	employees   *yzjT03EmployeeRepo
	users       *yzjT03UserRepo
	members     *yzjT03MemberService
	tenants     *yzjT03TenantService
	tokens      *stubAuthTokenRepo
	remover     *yzjT03Remover
	userService interfaces.UserService
	provisioner *YunzhijiaProvisioner
	login       *YunzhijiaLoginService
}

func newYZJT03Env(cfg *config.YunzhijiaSSOConfig, employees ...*types.YunzhijiaEmployee) *yzjT03Env {
	env := &yzjT03Env{
		cfg:       cfg,
		client:    &yzjT03Client{name: "测试员工甲"},
		employees: newYZJT03EmployeeRepo(employees...),
		users:     newYZJT03UserRepo(),
		members:   newYZJT03MemberService(),
		tenants:   newYZJT03TenantService(yzjT03TenantDefault, yzjT03TenantHR),
		tokens:    &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}},
	}
	env.remover = &yzjT03Remover{repo: env.users}
	env.userService = NewUserService(&config.Config{}, env.users, env.tokens, env.tenants, env.members, nil)
	env.provisioner = NewYunzhijiaProvisioner(
		cfg, env.users, env.members, env.employees, env.remover)
	env.login = NewYunzhijiaLoginService(
		cfg, env.client, env.employees, env.provisioner, env.userService, env.tenants)
	return env
}

func yzjT03Employee(openID, name string, tenantID uint64) *types.YunzhijiaEmployee {
	return &types.YunzhijiaEmployee{
		OpenID:       openID,
		Name:         name,
		Department:   "测试部门",
		TenantID:     tenantID,
		Status:       types.YunzhijiaEmployeeStatusActive,
		LastSyncedAt: time.Now(),
	}
}

// yzjT03EmployeePreferences 返回带员工标记 true 的 preferences。
// AC-7 起标记是密码守卫/管理钩子的唯一判据，已有员工账号夹具必须显式 seed，
// 否则开号/登录路径会按 fail-closed 拒绝。
func yzjT03EmployeePreferences() types.UserPreferences {
	marker := true
	return types.UserPreferences{YunzhijiaEmployee: &marker}
}
