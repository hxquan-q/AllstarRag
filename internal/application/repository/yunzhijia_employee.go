package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrYunzhijiaEmployeeNotFound 由写操作（MarkBlocked/Unblock/
// SetDisabledBySync）在目标行不存在时返回。
var ErrYunzhijiaEmployeeNotFound = errors.New("repository: yunzhijia employee not found")

// yunzhijiaEmployeeRepository 实现 interfaces.YunzhijiaEmployeeRepository。
type yunzhijiaEmployeeRepository struct {
	db *gorm.DB
}

// NewYunzhijiaEmployeeRepository 构造员工目录仓储。
func NewYunzhijiaEmployeeRepository(db *gorm.DB) interfaces.YunzhijiaEmployeeRepository {
	return &yunzhijiaEmployeeRepository{db: db}
}

// UpsertByOpenID 按 open_id 插入或更新上游同步字段。
// 本地维护的 user_id / disabled_by_sync / blocked 字段不在更新列内，
// 避免一轮同步把管理员封禁或人工停用状态覆盖掉。
func (r *yunzhijiaEmployeeRepository) UpsertByOpenID(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) error {
	now := time.Now()
	employee.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "open_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"name":           employee.Name,
			"department":     employee.Department,
			"mobile":         employee.Mobile,
			"tenant_id":      employee.TenantID,
			"status":         employee.Status,
			"last_synced_at": employee.LastSyncedAt,
			"updated_at":     now,
		}),
	}).Create(employee).Error
	if err != nil {
		return err
	}
	return nil
}

// GetByOpenID 按 open_id 查询；不存在返回 (nil, nil)。
func (r *yunzhijiaEmployeeRepository) GetByOpenID(
	ctx context.Context, openID string,
) (*types.YunzhijiaEmployee, error) {
	var employee types.YunzhijiaEmployee
	err := r.db.WithContext(ctx).Where("open_id = ?", openID).First(&employee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &employee, nil
}

// GetByUserID 按本地用户 id 查询；不存在返回 (nil, nil)。
func (r *yunzhijiaEmployeeRepository) GetByUserID(
	ctx context.Context, userID string,
) (*types.YunzhijiaEmployee, error) {
	var employee types.YunzhijiaEmployee
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&employee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &employee, nil
}

// SetUserID 在本地账号创建成功后回填 employee.user_id。
// 目标行不存在时返回 ErrYunzhijiaEmployeeNotFound。
func (r *yunzhijiaEmployeeRepository) SetUserID(
	ctx context.Context, openID, userID string,
) error {
	result := r.db.WithContext(ctx).
		Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ?", openID).
		Update("user_id", userID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrYunzhijiaEmployeeNotFound
	}
	return nil
}

// FindActiveByName 返回所有 status=active 的同名记录（含 blocked）。
// 服务层用返回条数与 blocked 字段区分不同登录错误。
func (r *yunzhijiaEmployeeRepository) FindActiveByName(
	ctx context.Context, name string,
) ([]*types.YunzhijiaEmployee, error) {
	var employees []*types.YunzhijiaEmployee
	err := r.db.WithContext(ctx).
		Where("name = ? AND status = ?", name, types.YunzhijiaEmployeeStatusActive).
		Order("id ASC").
		Find(&employees).Error
	if err != nil {
		return nil, err
	}
	return employees, nil
}

// ListByStatus 返回指定状态的员工记录，按 id 升序。
func (r *yunzhijiaEmployeeRepository) ListByStatus(
	ctx context.Context, status types.YunzhijiaEmployeeStatus,
) ([]*types.YunzhijiaEmployee, error) {
	var employees []*types.YunzhijiaEmployee
	err := r.db.WithContext(ctx).
		Where("status = ?", status).
		Order("id ASC").
		Find(&employees).Error
	if err != nil {
		return nil, err
	}
	return employees, nil
}

// ListBlocked 返回 blocked=true 的记录，按 blocked_at 倒序。
func (r *yunzhijiaEmployeeRepository) ListBlocked(
	ctx context.Context,
) ([]*types.YunzhijiaEmployee, error) {
	var employees []*types.YunzhijiaEmployee
	err := r.db.WithContext(ctx).
		Where("blocked = ?", true).
		Order("blocked_at DESC, id DESC").
		Find(&employees).Error
	if err != nil {
		return nil, err
	}
	return employees, nil
}

// MarkBlocked 置 blocked=true。blocked 优先级最高，保留 user_id 供审计；
// 解除禁止重建（Unblock）时再清空 user_id。
func (r *yunzhijiaEmployeeRepository) MarkBlocked(
	ctx context.Context, openID, blockedBy string,
) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ?", openID).
		Updates(map[string]interface{}{
			"blocked":    true,
			"blocked_at": now,
			"blocked_by": blockedBy,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrYunzhijiaEmployeeNotFound
	}
	return nil
}

// Unblock 解除禁止重建，清空 blocked 相关字段与 user_id，同时清掉
// disabled_by_sync，否则离职停用标记会让下一轮同步与登录继续拒绝重建。
// 下一轮同步会按上游数据重新建号。重复调用（已解除）返回
// ErrYunzhijiaEmployeeNotFound，管理端按 404 处理。
func (r *yunzhijiaEmployeeRepository) Unblock(
	ctx context.Context, openID string,
) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ? AND blocked = ?", openID, true).
		Updates(map[string]interface{}{
			"blocked":          false,
			"blocked_at":       nil,
			"blocked_by":       nil,
			"user_id":          nil,
			"disabled_by_sync": false,
			"updated_at":       now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrYunzhijiaEmployeeNotFound
	}
	return nil
}

// SetDisabledBySync 更新“由同步因离职停用”标记。
func (r *yunzhijiaEmployeeRepository) SetDisabledBySync(
	ctx context.Context, openID string, disabled bool,
) error {
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ?", openID).
		Updates(map[string]interface{}{
			"disabled_by_sync": disabled,
			"updated_at":       time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrYunzhijiaEmployeeNotFound
	}
	return nil
}
