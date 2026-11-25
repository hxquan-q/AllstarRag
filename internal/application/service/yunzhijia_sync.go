package service

// 云之家员工同步编排：Run 抓取后 upsert 员工并调用账号生命周期，
// Preview 只读地给出计数、空间分布与异常。单运行互斥覆盖两者。
//
// T05 只定义生命周期窄接口并用测试替身验证编排；离职/调岗/再入职的
// 具体账号动作由 T06 实现，后台调度由 T07 包装。Run/Preview 同步返回。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var (
	// ErrSyncRunning 表示已有同步或预检在进行，新的 Run/Preview 直接拒绝。
	ErrSyncRunning = errors.New("yunzhijia: 同步或预检已在进行中")
	// ErrYunzhijiaAccountBanned 由生命周期实现返回：账号被封禁，同步跳过。
	ErrYunzhijiaAccountBanned = errors.New("yunzhijia: 账号已封禁")
	// ErrYunzhijiaLocalUserMissing 由生命周期实现返回：关联本地用户不存在。
	ErrYunzhijiaLocalUserMissing = errors.New("yunzhijia: 关联本地用户不存在")
)

// YunzhijiaPreviewResult 是管理员预检的只读结果。
type YunzhijiaPreviewResult struct {
	ActiveCount        int                          `json:"active_count"`
	InactiveCount      int                          `json:"inactive_count"`
	TenantDefaultCount int                          `json:"tenant_default_count"`
	TenantHRCount      int                          `json:"tenant_hr_count"`
	Anomalies          []types.YunzhijiaSyncAnomaly `json:"anomalies"`
}

// yzjRunCounters 累计一轮同步的账号动作计数（AC-14）。
type yzjRunCounters struct {
	created  int
	updated  int
	disabled int
	migrated int
}

// add 按生命周期结果累加对应计数，none 不计。
func (c *yzjRunCounters) add(result types.YunzhijiaAccountResult) {
	switch result {
	case types.YunzhijiaAccountResultCreated:
		c.created++
	case types.YunzhijiaAccountResultUpdated:
		c.updated++
	case types.YunzhijiaAccountResultDisabled:
		c.disabled++
	case types.YunzhijiaAccountResultMigrated:
		c.migrated++
	}
}

// YunzhijiaSyncService 编排一轮员工同步或预检。
type YunzhijiaSyncService struct {
	cfg          *config.YunzhijiaSSOConfig
	client       yunzhijia.Client
	employeeRepo interfaces.YunzhijiaEmployeeRepository
	runRepo      interfaces.YunzhijiaSyncRunRepository
	lifecycle    interfaces.YunzhijiaAccountLifecycle
	running      atomic.Bool
}

// NewYunzhijiaSyncService 构造同步服务。
func NewYunzhijiaSyncService(
	cfg *config.YunzhijiaSSOConfig,
	client yunzhijia.Client,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
	runRepo interfaces.YunzhijiaSyncRunRepository,
	lifecycle interfaces.YunzhijiaAccountLifecycle,
) *YunzhijiaSyncService {
	return &YunzhijiaSyncService{
		cfg:          cfg,
		client:       client,
		employeeRepo: employeeRepo,
		runRepo:      runRepo,
		lifecycle:    lifecycle,
	}
}

// Run 执行一轮同步并同步返回运行记录。
// trigger 由调用方给出（manual / startup / cron）；enabled 开关不影响手动同步。
func (s *YunzhijiaSyncService) Run(
	ctx context.Context, trigger types.YunzhijiaSyncTrigger,
) (*types.YunzhijiaSyncRun, error) {
	if !s.running.CompareAndSwap(false, true) {
		return nil, ErrSyncRunning
	}
	defer s.running.Store(false)

	run, err := s.startRun(ctx, trigger)
	if err != nil {
		return nil, err
	}
	fetched, err := s.fetchAll(ctx)
	if err != nil {
		return s.failRun(ctx, run, err)
	}
	plan := s.buildPlan(fetched)
	now := time.Now()
	run.ActiveCount = len(fetched.active)
	run.InactiveCount = len(fetched.inactive)

	counters := &yzjRunCounters{}
	anomalies := append([]types.YunzhijiaSyncAnomaly(nil), plan.anomalies...)
	for _, employee := range plan.active {
		anomalies = append(anomalies, s.applyActive(ctx, employee, now, counters)...)
	}
	for _, employee := range plan.inactive {
		anomalies = append(anomalies, s.applyInactive(ctx, employee, now, counters)...)
	}
	// 先把已发生的账号计数落进运行记录，后续步骤失败时失败记录也能反映
	// 实际开号/更新/停用/迁移人数。
	run.CreatedCount = counters.created
	run.UpdatedCount = counters.updated
	run.DisabledCount = counters.disabled
	run.MigratedCount = counters.migrated
	missingAnomalies, err := s.applyMissingActive(ctx, fetched, now, counters)
	if err != nil {
		return s.failRun(ctx, run, err)
	}
	anomalies = append(anomalies, missingAnomalies...)

	encoded, err := marshalAnomalies(anomalies)
	if err != nil {
		return s.failRun(ctx, run, err)
	}
	run.Status = types.YunzhijiaSyncStatusSuccess
	run.CreatedCount = counters.created
	run.UpdatedCount = counters.updated
	run.DisabledCount = counters.disabled
	run.MigratedCount = counters.migrated
	run.Anomalies = encoded
	run.AnomalyCount = len(anomalies)
	if err := s.finishRun(ctx, run); err != nil {
		return run, err
	}
	return run, nil
}

// Preview 只抓取、校验、计数并写一条 trigger=preview 运行记录，
// 不写员工记录、不调用账号生命周期。enabled=false 时仍可用。
func (s *YunzhijiaSyncService) Preview(
	ctx context.Context,
) (*YunzhijiaPreviewResult, error) {
	if !s.running.CompareAndSwap(false, true) {
		return nil, ErrSyncRunning
	}
	defer s.running.Store(false)

	run, err := s.startRun(ctx, types.YunzhijiaSyncTriggerPreview)
	if err != nil {
		return nil, err
	}
	fetched, err := s.fetchAll(ctx)
	if err != nil {
		_, retErr := s.failRun(ctx, run, err)
		return nil, retErr
	}
	plan := s.buildPlan(fetched)
	result := &YunzhijiaPreviewResult{
		ActiveCount:   len(fetched.active),
		InactiveCount: len(fetched.inactive),
		Anomalies:     plan.anomalies,
	}
	for _, employee := range plan.active {
		tenantID, _ := s.departmentTenant(employee.Department)
		if tenantID == s.cfg.TenantHR {
			result.TenantHRCount++
		} else {
			result.TenantDefaultCount++
		}
	}

	encoded, err := marshalAnomalies(plan.anomalies)
	if err != nil {
		_, retErr := s.failRun(ctx, run, err)
		return nil, retErr
	}
	run.Status = types.YunzhijiaSyncStatusSuccess
	run.ActiveCount = result.ActiveCount
	run.InactiveCount = result.InactiveCount
	run.Anomalies = encoded
	run.AnomalyCount = len(plan.anomalies)
	if err := s.finishRun(ctx, run); err != nil {
		return nil, err
	}
	return result, nil
}

// applyActive upsert 在职员工，重新读出库内行后判断 blocked 并交给生命周期。
// 单条失败只记异常，不影响其他人。
func (s *YunzhijiaSyncService) applyActive(
	ctx context.Context, employee yunzhijia.Employee, now time.Time,
	counters *yzjRunCounters,
) []types.YunzhijiaSyncAnomaly {
	row := s.employeeRow(employee, types.YunzhijiaEmployeeStatusActive, now)
	persisted, anomaly := s.persistAndReload(ctx, row)
	if anomaly != nil {
		return []types.YunzhijiaSyncAnomaly{*anomaly}
	}
	if persisted.Blocked {
		return []types.YunzhijiaSyncAnomaly{blockedSkippedAnomaly(persisted)}
	}
	result, err := s.lifecycle.ApplyEmployee(ctx, persisted)
	if err != nil {
		return []types.YunzhijiaSyncAnomaly{lifecycleAnomaly(persisted, err)}
	}
	counters.add(result)
	return nil
}

// applyInactive 同样先 upsert 再读库内行，然后交给生命周期的离职处理。
func (s *YunzhijiaSyncService) applyInactive(
	ctx context.Context, employee yunzhijia.Employee, now time.Time,
	counters *yzjRunCounters,
) []types.YunzhijiaSyncAnomaly {
	row := s.employeeRow(employee, types.YunzhijiaEmployeeStatusInactive, now)
	persisted, anomaly := s.persistAndReload(ctx, row)
	if anomaly != nil {
		return []types.YunzhijiaSyncAnomaly{*anomaly}
	}
	if persisted.Blocked {
		return []types.YunzhijiaSyncAnomaly{blockedSkippedAnomaly(persisted)}
	}
	result, err := s.lifecycle.ApplyInactive(ctx, persisted)
	if err != nil {
		return []types.YunzhijiaSyncAnomaly{lifecycleAnomaly(persisted, err)}
	}
	counters.add(result)
	return nil
}

// applyMissingActive 处理「本地 active、但本轮上游 active/inactive 两批
// 名单都没有」的员工：两批都完整抓取时，这些人按离职收敛（AC-11）。
//
// 在职列表出现重复 open_id 时不执行：去重后条数仍等于 total，但实际
// 漏了人，不能据此判定缺席者离职（INV-9）。active total=0 已由抓取阶段
// 整轮拒绝，不会误判全员。
func (s *YunzhijiaSyncService) applyMissingActive(
	ctx context.Context, fetched *yzjFetched, now time.Time,
	counters *yzjRunCounters,
) ([]types.YunzhijiaSyncAnomaly, error) {
	activeSeen := make(map[string]struct{}, len(fetched.active))
	for _, employee := range fetched.active {
		if _, dup := activeSeen[employee.OpenID]; dup {
			return nil, nil
		}
		activeSeen[employee.OpenID] = struct{}{}
	}

	fetchedSeen := make(map[string]struct{}, len(fetched.active)+len(fetched.inactive))
	for _, employee := range fetched.active {
		fetchedSeen[employee.OpenID] = struct{}{}
	}
	for _, employee := range fetched.inactive {
		fetchedSeen[employee.OpenID] = struct{}{}
	}

	rows, err := s.employeeRepo.ListByStatus(ctx, types.YunzhijiaEmployeeStatusActive)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: 查询本地在职员工失败: %w", err)
	}
	var anomalies []types.YunzhijiaSyncAnomaly
	for _, row := range rows {
		if _, seen := fetchedSeen[row.OpenID]; seen {
			continue
		}
		anomalies = append(anomalies, types.YunzhijiaSyncAnomaly{
			Kind:   types.YunzhijiaAnomalyLocalActiveMissing,
			OpenID: row.OpenID,
			Name:   row.Name,
			Detail: "本地在职员工不在本轮上游名单，按离职处理",
		})
		anomalies = append(
			anomalies, s.applyInactive(ctx, yzjEmployeeFromRow(row), now, counters)...,
		)
	}
	return anomalies, nil
}

// yzjEmployeeFromRow 用本地行构造离职处理入参：本轮上游没有该员工，
// 沿用库里已有的姓名、部门与手机号，避免 upsert 清空同步字段。
func yzjEmployeeFromRow(row *types.YunzhijiaEmployee) yunzhijia.Employee {
	employee := yunzhijia.Employee{
		OpenID:     row.OpenID,
		Name:       row.Name,
		Department: row.Department,
	}
	if row.Mobile != nil {
		employee.Mobile = *row.Mobile
	}
	return employee
}

// persistAndReload 先 upsert 同步字段，再从仓储读出完整行。
// 必须用读出的行判断 blocked/user_id/disabled_by_sync，不能用上游入参。
func (s *YunzhijiaSyncService) persistAndReload(
	ctx context.Context, row *types.YunzhijiaEmployee,
) (*types.YunzhijiaEmployee, *types.YunzhijiaSyncAnomaly) {
	if err := s.employeeRepo.UpsertByOpenID(ctx, row); err != nil {
		anomaly := provisionFailedAnomaly(row, err.Error())
		return nil, &anomaly
	}
	persisted, err := s.employeeRepo.GetByOpenID(ctx, row.OpenID)
	if err != nil {
		anomaly := provisionFailedAnomaly(row, err.Error())
		return nil, &anomaly
	}
	if persisted == nil {
		anomaly := provisionFailedAnomaly(row, "upsert 后未读到员工记录")
		return nil, &anomaly
	}
	return persisted, nil
}

func blockedSkippedAnomaly(row *types.YunzhijiaEmployee) types.YunzhijiaSyncAnomaly {
	return types.YunzhijiaSyncAnomaly{
		Kind:   types.YunzhijiaAnomalyBlockedSkipped,
		OpenID: row.OpenID,
		Name:   row.Name,
		UserID: derefString(row.UserID),
		Detail: "员工已删除并禁止重建，同步跳过",
	}
}

func provisionFailedAnomaly(
	row *types.YunzhijiaEmployee, detail string,
) types.YunzhijiaSyncAnomaly {
	return types.YunzhijiaSyncAnomaly{
		Kind:   types.YunzhijiaAnomalyProvisionFailed,
		OpenID: row.OpenID,
		Name:   row.Name,
		UserID: derefString(row.UserID),
		Detail: detail,
	}
}

// lifecycleAnomaly 把生命周期返回的哨兵映射为对应异常类型。
func lifecycleAnomaly(
	row *types.YunzhijiaEmployee, err error,
) types.YunzhijiaSyncAnomaly {
	kind := types.YunzhijiaAnomalyProvisionFailed
	switch {
	case errors.Is(err, ErrYunzhijiaAccountBanned):
		kind = types.YunzhijiaAnomalyBannedSkipped
	case errors.Is(err, ErrYunzhijiaLocalUserMissing):
		kind = types.YunzhijiaAnomalyLocalUserMissing
	}
	return types.YunzhijiaSyncAnomaly{
		Kind:   kind,
		OpenID: row.OpenID,
		Name:   row.Name,
		UserID: derefString(row.UserID),
		Detail: err.Error(),
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// startRun 先写入 running 记录，保证整轮失败时也留下痕迹。
func (s *YunzhijiaSyncService) startRun(
	ctx context.Context, trigger types.YunzhijiaSyncTrigger,
) (*types.YunzhijiaSyncRun, error) {
	run := &types.YunzhijiaSyncRun{Trigger: trigger, StartedAt: time.Now()}
	if err := s.runRepo.CreateRunning(ctx, run); err != nil {
		return nil, fmt.Errorf("yunzhijia: 写入同步运行记录失败: %w", err)
	}
	return run, nil
}

// finishRun 写入结束状态、计数与异常。
func (s *YunzhijiaSyncService) finishRun(
	ctx context.Context, run *types.YunzhijiaSyncRun,
) error {
	now := time.Now()
	run.FinishedAt = &now
	if err := s.runRepo.Finish(ctx, run); err != nil {
		return fmt.Errorf("yunzhijia: 更新同步运行记录失败: %w", err)
	}
	return nil
}

// failRun 把整轮失败写进运行记录；Finish 自身失败时与原始错误合并返回。
func (s *YunzhijiaSyncService) failRun(
	ctx context.Context, run *types.YunzhijiaSyncRun, cause error,
) (*types.YunzhijiaSyncRun, error) {
	now := time.Now()
	run.Status = types.YunzhijiaSyncStatusFailed
	run.FinishedAt = &now
	run.ErrorSummary = cause.Error()
	if err := s.runRepo.Finish(ctx, run); err != nil {
		return run, errors.Join(cause, fmt.Errorf("yunzhijia: 更新失败运行记录失败: %w", err))
	}
	return run, cause
}

// marshalAnomalies 把异常清单编码为运行记录的 JSON 列；
// 没有异常时保持为空，由 omitempty 省略。
func marshalAnomalies(anomalies []types.YunzhijiaSyncAnomaly) (types.JSON, error) {
	if len(anomalies) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(anomalies)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: 序列化同步异常失败: %w", err)
	}
	return types.JSON(data), nil
}
