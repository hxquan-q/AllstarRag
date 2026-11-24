package service

// T05 TDD 红阶段的测试替身。只落在 _test.go，不提供生产实现。
//
// 红阶段契约（实现方按此对齐，见 05-tests.md 第 13 节）：
//   NewYunzhijiaSyncService(cfg, client, employeeRepo, runRepo, lifecycle)
//   (*YunzhijiaSyncService).Run(ctx, trigger) (*types.YunzhijiaSyncRun, error)
//   (*YunzhijiaSyncService).Preview(ctx) (*YunzhijiaPreviewResult, error)
//   service.ErrSyncRunning
//   service.ErrYunzhijiaAccountBanned / service.ErrYunzhijiaLocalUserMissing
//   interfaces.YunzhijiaAccountLifecycle
//   types.YunzhijiaSyncTriggerPreview
//
// 所有姓名 / open_id 均为占位符，不使用真实员工数据。

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	yzjT05TenantDefault = uint64(10024)
	yzjT05TenantHR      = uint64(10022)
)

func yzjT05Config(pageSize int, enabled bool) *config.YunzhijiaSSOConfig {
	return &config.YunzhijiaSSOConfig{
		Enabled:             enabled,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/employees",
		TicketUserPath:      "/queryUserName",
		AppID:               "app-placeholder",
		TenantDefault:       yzjT05TenantDefault,
		TenantHR:            yzjT05TenantHR,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            pageSize,
		HTTPTimeoutSeconds:  10,
		StartupCatchupHours: 0,
	}
}

// ---------------------------------------------------------------------------
// 云之家客户端替身：按 status/page 提供分页，可注入单页错误与首调用阻塞。
// ---------------------------------------------------------------------------

type yzjT05ClientCall struct {
	Status   yunzhijia.Status
	Page     int
	PageSize int
}

type yzjT05Client struct {
	mu      sync.Mutex
	pages   map[yunzhijia.Status]map[int]*yunzhijia.EmployeePage
	errs    map[yunzhijia.Status]map[int]error
	calls   []yzjT05ClientCall
	maxPage int

	// block 非 nil 时，第一次 ListEmployees 进入后关闭 entered，并阻塞到
	// block 关闭或 ctx 取消；用于验证单运行互斥。后续调用不再阻塞。
	block     chan struct{}
	entered   chan struct{}
	blockOnce sync.Once
}

func newYZJT05Client() *yzjT05Client {
	return &yzjT05Client{
		pages:   map[yunzhijia.Status]map[int]*yunzhijia.EmployeePage{},
		errs:    map[yunzhijia.Status]map[int]error{},
		maxPage: 100,
	}
}

func (c *yzjT05Client) setPage(status yunzhijia.Status, page int, p *yunzhijia.EmployeePage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pages[status] == nil {
		c.pages[status] = map[int]*yunzhijia.EmployeePage{}
	}
	c.pages[status][page] = p
}

func (c *yzjT05Client) setErr(status yunzhijia.Status, page int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errs[status] == nil {
		c.errs[status] = map[int]error{}
	}
	c.errs[status][page] = err
}

func (c *yzjT05Client) QueryUserName(context.Context, string) (string, error) {
	return "", errors.New("T05 同步用例不应调用 ticket 换名接口")
}

func (c *yzjT05Client) ListEmployees(
	ctx context.Context, status yunzhijia.Status, page, pageSize int,
) (*yunzhijia.EmployeePage, error) {
	c.mu.Lock()
	c.calls = append(c.calls, yzjT05ClientCall{Status: status, Page: page, PageSize: pageSize})
	pages := c.pages[status]
	errs := c.errs[status]
	c.mu.Unlock()

	if c.block != nil {
		c.blockOnce.Do(func() {
			if c.entered != nil {
				close(c.entered)
			}
			select {
			case <-c.block:
			case <-ctx.Done():
			}
		})
	}

	if err := errs[page]; err != nil {
		return nil, err
	}
	if p, ok := pages[page]; ok {
		cp := *p
		cp.List = append([]yunzhijia.Employee(nil), p.List...)
		return &cp, nil
	}
	if c.maxPage > 0 && page > c.maxPage {
		return nil, errors.New("T05 fake client: 抓取页数超过上限，疑似未按 total/pageSize 收敛")
	}
	if first, ok := pages[1]; ok {
		// 未显式配置的页返回空页，但保留首页的 total，便于构造“总数不一致”。
		return &yunzhijia.EmployeePage{Page: page, PageSize: first.PageSize, Total: first.Total}, nil
	}
	return &yunzhijia.EmployeePage{Page: page, PageSize: pageSize}, nil
}

func (c *yzjT05Client) callSnapshot() []yzjT05ClientCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]yzjT05ClientCall(nil), c.calls...)
}

func (c *yzjT05Client) callsFor(status yunzhijia.Status) []yzjT05ClientCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []yzjT05ClientCall
	for _, call := range c.calls {
		if call.Status == status {
			out = append(out, call)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 员工仓储替身：Upsert 只覆盖同步字段，保留 user_id / disabled_by_sync / blocked，
// 与真实仓储契约一致，用来验证“upsert 后用库里读出的行判断”。
// ---------------------------------------------------------------------------

type yzjT05EmployeeRepo struct {
	interfaces.YunzhijiaEmployeeRepository

	mu       sync.Mutex
	byOpenID map[string]*types.YunzhijiaEmployee
	upserts  int
	upserted []*types.YunzhijiaEmployee
}

func newYZJT05EmployeeRepo(rows ...*types.YunzhijiaEmployee) *yzjT05EmployeeRepo {
	r := &yzjT05EmployeeRepo{byOpenID: map[string]*types.YunzhijiaEmployee{}}
	for _, row := range rows {
		r.byOpenID[row.OpenID] = yzjT05CloneEmployee(row)
	}
	return r
}

func yzjT05CloneEmployee(e *types.YunzhijiaEmployee) *types.YunzhijiaEmployee {
	if e == nil {
		return nil
	}
	cp := *e
	if e.Mobile != nil {
		v := *e.Mobile
		cp.Mobile = &v
	}
	if e.UserID != nil {
		v := *e.UserID
		cp.UserID = &v
	}
	if e.BlockedAt != nil {
		v := *e.BlockedAt
		cp.BlockedAt = &v
	}
	if e.BlockedBy != nil {
		v := *e.BlockedBy
		cp.BlockedBy = &v
	}
	return &cp
}

func (r *yzjT05EmployeeRepo) applySyncedLocked(cur, employee *types.YunzhijiaEmployee) {
	cur.Name = employee.Name
	cur.Department = employee.Department
	cur.Mobile = yzjT05CloneEmployee(employee).Mobile
	cur.TenantID = employee.TenantID
	cur.Status = employee.Status
	cur.LastSyncedAt = employee.LastSyncedAt
}

func (r *yzjT05EmployeeRepo) UpsertByOpenID(
	_ context.Context, employee *types.YunzhijiaEmployee,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts++
	r.upserted = append(r.upserted, yzjT05CloneEmployee(employee))
	cur, ok := r.byOpenID[employee.OpenID]
	if !ok {
		r.byOpenID[employee.OpenID] = yzjT05CloneEmployee(employee)
		return nil
	}
	r.applySyncedLocked(cur, employee)
	return nil
}

func (r *yzjT05EmployeeRepo) GetByOpenID(
	_ context.Context, openID string,
) (*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.byOpenID[openID]
	if !ok {
		return nil, nil
	}
	return yzjT05CloneEmployee(row), nil
}

func (r *yzjT05EmployeeRepo) ListByStatus(
	_ context.Context, status types.YunzhijiaEmployeeStatus,
) ([]*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*types.YunzhijiaEmployee
	for _, row := range r.byOpenID {
		if row.Status == status {
			out = append(out, yzjT05CloneEmployee(row))
		}
	}
	return out, nil
}

func (r *yzjT05EmployeeRepo) FindActiveByName(
	_ context.Context, name string,
) ([]*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*types.YunzhijiaEmployee
	for _, row := range r.byOpenID {
		if row.Status == types.YunzhijiaEmployeeStatusActive && row.Name == name {
			out = append(out, yzjT05CloneEmployee(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *yzjT05EmployeeRepo) GetByUserID(
	_ context.Context, userID string,
) (*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.byOpenID {
		if row.UserID != nil && *row.UserID == userID {
			return yzjT05CloneEmployee(row), nil
		}
	}
	return nil, nil
}

func (r *yzjT05EmployeeRepo) ListBlocked(
	_ context.Context,
) ([]*types.YunzhijiaEmployee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*types.YunzhijiaEmployee
	for _, row := range r.byOpenID {
		if row.Blocked {
			out = append(out, yzjT05CloneEmployee(row))
		}
	}
	return out, nil
}

func (r *yzjT05EmployeeRepo) MarkBlocked(
	_ context.Context, openID, blockedBy string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.byOpenID[openID]
	if !ok {
		return repository.ErrYunzhijiaEmployeeNotFound
	}
	now := time.Now()
	by := blockedBy
	row.Blocked = true
	row.BlockedAt = &now
	row.BlockedBy = &by
	row.UserID = nil
	return nil
}

func (r *yzjT05EmployeeRepo) Unblock(_ context.Context, openID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.byOpenID[openID]
	if !ok || !row.Blocked {
		return repository.ErrYunzhijiaEmployeeNotFound
	}
	row.Blocked = false
	row.BlockedAt = nil
	row.BlockedBy = nil
	row.UserID = nil
	return nil
}

func (r *yzjT05EmployeeRepo) UpdateSyncedFields(
	_ context.Context, employee *types.YunzhijiaEmployee,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.byOpenID[employee.OpenID]
	if !ok {
		return repository.ErrYunzhijiaEmployeeNotFound
	}
	r.applySyncedLocked(cur, employee)
	return nil
}

func (r *yzjT05EmployeeRepo) SetDisabledBySync(
	_ context.Context, openID string, disabled bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.byOpenID[openID]
	if !ok {
		return repository.ErrYunzhijiaEmployeeNotFound
	}
	row.DisabledBySync = disabled
	return nil
}

func (r *yzjT05EmployeeRepo) Create(
	_ context.Context, employee *types.YunzhijiaEmployee,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byOpenID[employee.OpenID]; ok {
		return repository.ErrYunzhijiaEmployeeExists
	}
	r.byOpenID[employee.OpenID] = yzjT05CloneEmployee(employee)
	return nil
}

func (r *yzjT05EmployeeRepo) SetUserID(_ context.Context, openID, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.byOpenID[openID]
	if !ok {
		return repository.ErrYunzhijiaEmployeeNotFound
	}
	id := userID
	row.UserID = &id
	return nil
}

func (r *yzjT05EmployeeRepo) snapshot(openID string) *types.YunzhijiaEmployee {
	r.mu.Lock()
	defer r.mu.Unlock()
	return yzjT05CloneEmployee(r.byOpenID[openID])
}

func (r *yzjT05EmployeeRepo) upsertCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upserts
}

func (r *yzjT05EmployeeRepo) upsertCountFor(openID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.upserted {
		if e.OpenID == openID {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 账号生命周期窄接口替身（T06 才实现真实逻辑）。
// ---------------------------------------------------------------------------

type yzjT05Lifecycle struct {
	interfaces.YunzhijiaAccountLifecycle

	mu            sync.Mutex
	employeeCalls []*types.YunzhijiaEmployee
	inactiveCalls []*types.YunzhijiaEmployee
	employeeErrs  map[string]error
	inactiveErrs  map[string]error
}

func newYZJT05Lifecycle() *yzjT05Lifecycle {
	return &yzjT05Lifecycle{
		employeeErrs: map[string]error{},
		inactiveErrs: map[string]error{},
	}
}

func (l *yzjT05Lifecycle) ApplyEmployee(
	_ context.Context, employee *types.YunzhijiaEmployee,
) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.employeeCalls = append(l.employeeCalls, yzjT05CloneEmployee(employee))
	return l.employeeErrs[employee.OpenID]
}

func (l *yzjT05Lifecycle) ApplyInactive(
	_ context.Context, employee *types.YunzhijiaEmployee,
) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inactiveCalls = append(l.inactiveCalls, yzjT05CloneEmployee(employee))
	return l.inactiveErrs[employee.OpenID]
}

func (l *yzjT05Lifecycle) setEmployeeErr(openID string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.employeeErrs[openID] = err
}

func (l *yzjT05Lifecycle) employeeSnapshot() []*types.YunzhijiaEmployee {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*types.YunzhijiaEmployee, 0, len(l.employeeCalls))
	for _, e := range l.employeeCalls {
		out = append(out, yzjT05CloneEmployee(e))
	}
	return out
}

func (l *yzjT05Lifecycle) inactiveSnapshot() []*types.YunzhijiaEmployee {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*types.YunzhijiaEmployee, 0, len(l.inactiveCalls))
	for _, e := range l.inactiveCalls {
		out = append(out, yzjT05CloneEmployee(e))
	}
	return out
}

func (l *yzjT05Lifecycle) employeeCall(openID string) (*types.YunzhijiaEmployee, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.employeeCalls {
		if e.OpenID == openID {
			return yzjT05CloneEmployee(e), true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// 同步运行记录替身。
// ---------------------------------------------------------------------------

type yzjT05RunRepo struct {
	mu            sync.Mutex
	runs          []*types.YunzhijiaSyncRun
	nextID        uint64
	createCalls   int
	finishCalls   int
	createdState  []types.YunzhijiaSyncStatus
	finishedState []types.YunzhijiaSyncStatus
}

func yzjT05CloneRun(run *types.YunzhijiaSyncRun) *types.YunzhijiaSyncRun {
	if run == nil {
		return nil
	}
	cp := *run
	if run.FinishedAt != nil {
		t := *run.FinishedAt
		cp.FinishedAt = &t
	}
	if len(run.Anomalies) > 0 {
		cp.Anomalies = append(types.JSON(nil), run.Anomalies...)
	}
	return &cp
}

func (r *yzjT05RunRepo) CreateRunning(_ context.Context, run *types.YunzhijiaSyncRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	run.ID = r.nextID
	run.Status = types.YunzhijiaSyncStatusRunning
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	r.createCalls++
	r.createdState = append(r.createdState, run.Status)
	r.runs = append(r.runs, yzjT05CloneRun(run))
	return nil
}

func (r *yzjT05RunRepo) Finish(_ context.Context, run *types.YunzhijiaSyncRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishCalls++
	r.finishedState = append(r.finishedState, run.Status)
	for i := range r.runs {
		if r.runs[i].ID == run.ID {
			r.runs[i] = yzjT05CloneRun(run)
			return nil
		}
	}
	return repository.ErrYunzhijiaSyncRunNotFound
}

func (r *yzjT05RunRepo) Latest(_ context.Context) (*types.YunzhijiaSyncRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.runs) - 1; i >= 0; i-- {
		if r.runs[i].Trigger == types.YunzhijiaSyncTrigger("preview") {
			continue
		}
		return yzjT05CloneRun(r.runs[i]), nil
	}
	return nil, nil
}

func (r *yzjT05RunRepo) ListRecent(
	_ context.Context, limit int,
) ([]*types.YunzhijiaSyncRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	var out []*types.YunzhijiaSyncRun
	for i := len(r.runs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, yzjT05CloneRun(r.runs[i]))
	}
	return out, nil
}

func (r *yzjT05RunRepo) latestPreview() *types.YunzhijiaSyncRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.runs) - 1; i >= 0; i-- {
		if r.runs[i].Trigger == types.YunzhijiaSyncTrigger("preview") {
			return yzjT05CloneRun(r.runs[i])
		}
	}
	return nil
}

func (r *yzjT05RunRepo) createCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createCalls
}

func (r *yzjT05RunRepo) finishCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finishCalls
}

func (r *yzjT05RunRepo) createdStatuses() []types.YunzhijiaSyncStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.YunzhijiaSyncStatus(nil), r.createdState...)
}
