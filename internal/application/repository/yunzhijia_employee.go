package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrYunzhijiaEmployeeExists 由 Create 在 open_id 唯一键冲突时返回，
	// 服务层据此走开号冲突分支。
	ErrYunzhijiaEmployeeExists = errors.New("repository: yunzhijia employee already exists")
	// ErrYunzhijiaEmployeeNotFound 由写操作（MarkBlocked/Unblock/
	// SetDisabledBySync/UpdateSyncedFields）在目标行不存在时返回。
	ErrYunzhijiaEmployeeNotFound = errors.New("repository: yunzhijia employee not found")
)

// yunzhijiaEmployeeRepository 实现 interfaces.YunzhijiaEmployeeRepository。
type yunzhijiaEmployeeRepository struct {
	db *gorm.DB
}

// NewYunzhijiaEmployeeRepository 构造员工目录仓储。
func NewYunzhijiaEmployeeRepository(db *gorm.DB) interfaces.YunzhijiaEmployeeRepository {
	return &yunzhijiaEmployeeRepository{db: db}
}

// isYunzhijiaDuplicate 判断底层错误是否为唯一键冲突。
// Postgres 报 "duplicate key value violates unique constraint"，
// SQLite 报 "UNIQUE constraint failed"；GORM 开启 TranslateError 时
// 还会给出 ErrDuplicatedKey。
func isYunzhijiaDuplicate(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique constraint")
}

// Create 插入一行员工目录记录。
func (r *yunzhijiaEmployeeRepository) Create(ctx context.Context, employee *types.YunzhijiaEmployee) error {
	if err := r.db.WithContext(ctx).Create(employee).Error; err != nil {
		if isYunzhijiaDuplicate(err) {
			return ErrYunzhijiaEmployeeExists
		}
		return err
	}
	return nil
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

// MarkBlocked 置 blocked=true 并清空 user_id（账号已删除）。
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
			"user_id":    nil,
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

// Unblock 解除禁止重建，清空 blocked 相关字段与 user_id。
// 下一轮同步会按上游数据重新建号。
func (r *yunzhijiaEmployeeRepository) Unblock(
	ctx context.Context, openID string,
) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ? AND blocked = ?", openID, true).
		Updates(map[string]interface{}{
			"blocked":    false,
			"blocked_at": nil,
			"blocked_by": nil,
			"user_id":    nil,
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

// UpdateSyncedFields 更新来自上游的同步字段。
func (r *yunzhijiaEmployeeRepository) UpdateSyncedFields(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&types.YunzhijiaEmployee{}).
		Where("open_id = ?", employee.OpenID).
		Updates(map[string]interface{}{
			"name":           employee.Name,
			"department":     employee.Department,
			"mobile":         employee.Mobile,
			"tenant_id":      employee.TenantID,
			"status":         employee.Status,
			"last_synced_at": employee.LastSyncedAt,
			"updated_at":     now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrYunzhijiaEmployeeNotFound
	}
	employee.UpdatedAt = now
	return nil
}
