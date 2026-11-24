package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrYunzhijiaSyncRunNotFound 由 Finish 在目标运行记录不存在时返回。
var ErrYunzhijiaSyncRunNotFound = errors.New("repository: yunzhijia sync run not found")

// defaultYunzhijiaSyncRunListLimit 是 ListRecent(limit<=0) 的默认条数。
const defaultYunzhijiaSyncRunListLimit = 20

// yunzhijiaSyncRunRepository 实现 interfaces.YunzhijiaSyncRunRepository。
type yunzhijiaSyncRunRepository struct {
	db *gorm.DB
}

// NewYunzhijiaSyncRunRepository 构造同步运行记录仓储。
func NewYunzhijiaSyncRunRepository(db *gorm.DB) interfaces.YunzhijiaSyncRunRepository {
	return &yunzhijiaSyncRunRepository{db: db}
}

// CreateRunning 写入一条 running 记录；自增 id 会回填到 run。
func (r *yunzhijiaSyncRunRepository) CreateRunning(
	ctx context.Context, run *types.YunzhijiaSyncRun,
) error {
	run.Status = types.YunzhijiaSyncStatusRunning
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(run).Error
}

// Finish 按 run.ID 写入结束状态、计数、异常与错误摘要。
func (r *yunzhijiaSyncRunRepository) Finish(
	ctx context.Context, run *types.YunzhijiaSyncRun,
) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaSyncRun{}).
		Where("id = ?", run.ID).
		Updates(map[string]interface{}{
			"status":         run.Status,
			"finished_at":    run.FinishedAt,
			"active_count":   run.ActiveCount,
			"inactive_count": run.InactiveCount,
			"created_count":  run.CreatedCount,
			"updated_count":  run.UpdatedCount,
			"disabled_count": run.DisabledCount,
			"migrated_count": run.MigratedCount,
			"anomaly_count":  run.AnomalyCount,
			"anomalies":      run.Anomalies,
			"error_summary":  run.ErrorSummary,
			"updated_at":     now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrYunzhijiaSyncRunNotFound
	}
	run.UpdatedAt = now
	return nil
}

// Latest 返回最近一条运行记录；没有记录时返回 (nil, nil)。
// 预检（trigger=preview）只读，不覆盖管理端看到的真实同步状态。
func (r *yunzhijiaSyncRunRepository) Latest(
	ctx context.Context,
) (*types.YunzhijiaSyncRun, error) {
	var run types.YunzhijiaSyncRun
	err := r.db.WithContext(ctx).
		Where("trigger <> ?", types.YunzhijiaSyncTriggerPreview).
		Order("started_at DESC, id DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// ListRecent 返回最近 limit 条记录（limit<=0 时按 20 条），按 id 倒序。
func (r *yunzhijiaSyncRunRepository) ListRecent(
	ctx context.Context, limit int,
) ([]*types.YunzhijiaSyncRun, error) {
	if limit <= 0 {
		limit = defaultYunzhijiaSyncRunListLimit
	}
	var runs []*types.YunzhijiaSyncRun
	err := r.db.WithContext(ctx).Order("started_at DESC, id DESC").Limit(limit).Find(&runs).Error
	if err != nil {
		return nil, err
	}
	return runs, nil
}
