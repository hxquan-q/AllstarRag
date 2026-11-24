package service

// 云之家同步管理端服务（T08）：汇总最近运行、blocked 清单，提供只读预检、
// 手动触发与解除禁止重建。权限由路由与 handler 控制；本层只做业务动作。
//
// 预检与手动同步不受 enabled 门控：首次导入可在登录未启用时进行。
// 手动触发与预检共用同步服务的单运行互斥，运行中返回 ErrSyncRunning。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrYunzhijiaEmployeeNotFound 表示 blocked 记录不存在或已解除。
var ErrYunzhijiaEmployeeNotFound = errors.New("yunzhijia: 员工不存在或已解除禁止重建")

// YunzhijiaBlockedEmployee 是管理端 blocked 清单的展示项。
// 只给 open_id/姓名/部门/时间/操作人，不带手机号与本地 user_id。
type YunzhijiaBlockedEmployee struct {
	OpenID     string     `json:"open_id"`
	Name       string     `json:"name"`
	Department string     `json:"department"`
	BlockedAt  *time.Time `json:"blocked_at,omitempty"`
	BlockedBy  string     `json:"blocked_by,omitempty"`
}

// YunzhijiaRunSummary 是最近一次真实同步运行的摘要（不含预览记录）。
type YunzhijiaRunSummary struct {
	ID            uint64                       `json:"id"`
	Trigger       types.YunzhijiaSyncTrigger   `json:"trigger"`
	Status        types.YunzhijiaSyncStatus    `json:"status"`
	StartedAt     time.Time                    `json:"started_at"`
	FinishedAt    *time.Time                   `json:"finished_at,omitempty"`
	DurationMS    int64                        `json:"duration_ms"`
	ActiveCount   int                          `json:"active_count"`
	InactiveCount int                          `json:"inactive_count"`
	CreatedCount  int                          `json:"created_count"`
	UpdatedCount  int                          `json:"updated_count"`
	DisabledCount int                          `json:"disabled_count"`
	MigratedCount int                          `json:"migrated_count"`
	AnomalyCount  int                          `json:"anomaly_count"`
	Anomalies     []types.YunzhijiaSyncAnomaly `json:"anomalies"`
	ErrorSummary  string                       `json:"error_summary,omitempty"`
}

// YunzhijiaAdminStatus 是管理端状态接口的返回。
type YunzhijiaAdminStatus struct {
	Enabled bool                       `json:"enabled"`
	Latest  *YunzhijiaRunSummary       `json:"latest,omitempty"`
	Blocked []YunzhijiaBlockedEmployee `json:"blocked"`
}

// YunzhijiaAdminService 提供管理端同步状态与操作。
type YunzhijiaAdminService struct {
	cfg       *config.YunzhijiaSSOConfig
	sync      *YunzhijiaSyncService
	runs      interfaces.YunzhijiaSyncRunRepository
	employees interfaces.YunzhijiaEmployeeRepository
	auditLog  interfaces.AuditLogService
}

// NewYunzhijiaAdminService 构造管理端服务。
func NewYunzhijiaAdminService(
	cfg *config.YunzhijiaSSOConfig,
	syncSvc *YunzhijiaSyncService,
	runs interfaces.YunzhijiaSyncRunRepository,
	employees interfaces.YunzhijiaEmployeeRepository,
	auditLog interfaces.AuditLogService,
) *YunzhijiaAdminService {
	return &YunzhijiaAdminService{
		cfg:       cfg,
		sync:      syncSvc,
		runs:      runs,
		employees: employees,
		auditLog:  auditLog,
	}
}

// Status 返回 enabled、最近一次真实运行摘要与 blocked 清单。
// 不返回全量员工名单，也不带手机号。
func (s *YunzhijiaAdminService) Status(
	ctx context.Context,
) (*YunzhijiaAdminStatus, error) {
	latest, err := s.runs.Latest(ctx)
	if err != nil {
		return nil, err
	}
	blocked, err := s.employees.ListBlocked(ctx)
	if err != nil {
		return nil, err
	}

	status := &YunzhijiaAdminStatus{
		Enabled: s.cfg != nil && s.cfg.Enabled,
		Blocked: make([]YunzhijiaBlockedEmployee, 0, len(blocked)),
	}
	for _, employee := range blocked {
		status.Blocked = append(status.Blocked, YunzhijiaBlockedEmployee{
			OpenID:     employee.OpenID,
			Name:       employee.Name,
			Department: employee.Department,
			BlockedAt:  employee.BlockedAt,
			BlockedBy:  derefString(employee.BlockedBy),
		})
	}
	if latest == nil {
		return status, nil
	}
	summary, err := summarizeRun(latest)
	if err != nil {
		return nil, err
	}
	status.Latest = summary
	return status, nil
}

// Preview 只读预检；不写员工记录与本地账号。
// 预检跑完整轮才返回，脱离请求取消，避免浏览器断开导致记录卡在 running。
func (s *YunzhijiaAdminService) Preview(
	ctx context.Context,
) (*YunzhijiaPreviewResult, error) {
	ctx = context.WithoutCancel(ctx)
	return s.sync.Preview(ctx)
}

// ManualSync 手动触发一轮同步。enabled=false 时仍可用；运行中返回
// ErrSyncRunning，由 handler 映射为 409。
// 同步跑完整轮才返回，脱离请求取消，避免浏览器断开导致运行卡 running、
// 审计丢失或员工停在半截。
func (s *YunzhijiaAdminService) ManualSync(
	ctx context.Context,
) (*types.YunzhijiaSyncRun, error) {
	ctx = context.WithoutCancel(ctx)
	run, err := s.sync.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if run != nil {
		s.emitSyncAudit(ctx, run)
	}
	return run, err
}

// Unblock 解除 blocked：清 blocked 标记与 user_id，下一轮同步重建账号。
// 重复解除按 ErrYunzhijiaEmployeeNotFound 返回，handler 映射为 404。
func (s *YunzhijiaAdminService) Unblock(
	ctx context.Context, openID, actorID string,
) error {
	if err := s.employees.Unblock(ctx, openID); err != nil {
		if errors.Is(err, repository.ErrYunzhijiaEmployeeNotFound) {
			return ErrYunzhijiaEmployeeNotFound
		}
		return err
	}
	s.emitUnblockAudit(ctx, openID, actorID)
	return nil
}

// summarizeRun 把运行记录转换成响应摘要，并解码异常 JSON。
func summarizeRun(run *types.YunzhijiaSyncRun) (*YunzhijiaRunSummary, error) {
	summary := &YunzhijiaRunSummary{
		ID:            run.ID,
		Trigger:       run.Trigger,
		Status:        run.Status,
		StartedAt:     run.StartedAt,
		FinishedAt:    run.FinishedAt,
		ActiveCount:   run.ActiveCount,
		InactiveCount: run.InactiveCount,
		CreatedCount:  run.CreatedCount,
		UpdatedCount:  run.UpdatedCount,
		DisabledCount: run.DisabledCount,
		MigratedCount: run.MigratedCount,
		AnomalyCount:  run.AnomalyCount,
		ErrorSummary:  run.ErrorSummary,
		Anomalies:     []types.YunzhijiaSyncAnomaly{},
	}
	if run.FinishedAt != nil {
		summary.DurationMS = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	}
	if len(run.Anomalies) > 0 {
		if err := json.Unmarshal(run.Anomalies, &summary.Anomalies); err != nil {
			return nil, err
		}
	}
	return summary, nil
}

// emitSyncAudit 记录一次手动同步。审计失败不影响同步结果。
func (s *YunzhijiaAdminService) emitSyncAudit(
	ctx context.Context, run *types.YunzhijiaSyncRun,
) {
	details, _ := json.Marshal(map[string]any{
		"run_id":         run.ID,
		"status":         run.Status,
		"active_count":   run.ActiveCount,
		"inactive_count": run.InactiveCount,
		"created_count":  run.CreatedCount,
		"updated_count":  run.UpdatedCount,
		"disabled_count": run.DisabledCount,
		"migrated_count": run.MigratedCount,
		"anomaly_count":  run.AnomalyCount,
	})
	actorID, _ := types.UserIDFromContext(ctx)
	_ = s.auditLog.Log(ctx, &types.AuditLog{
		TenantID:    0,
		ActorUserID: actorID,
		ActorRole:   "system_admin",
		Action:      types.AuditActionSystemYunzhijiaSyncTriggered,
		TargetType:  "yunzhijia_sync_run",
		TargetID:    strconv.FormatUint(run.ID, 10),
		Outcome:     types.AuditOutcomeSuccess,
		Details:     types.JSON(details),
	})
}

// emitUnblockAudit 记录解除 blocked。审计失败不影响解除结果。
func (s *YunzhijiaAdminService) emitUnblockAudit(
	ctx context.Context, openID, actorID string,
) {
	details, _ := json.Marshal(map[string]any{"open_id": openID})
	_ = s.auditLog.Log(ctx, &types.AuditLog{
		TenantID:    0,
		ActorUserID: actorID,
		ActorRole:   "system_admin",
		Action:      types.AuditActionSystemYunzhijiaUnblocked,
		TargetType:  "yunzhijia_employee",
		TargetID:    openID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     types.JSON(details),
	})
}
