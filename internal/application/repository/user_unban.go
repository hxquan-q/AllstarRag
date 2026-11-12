package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// ErrUnbanNotLatestTenant 表示目标的最近一次成功封禁不在调用者所在空间。
// 普通 owner 只能解封最近一次 ban 发生在本租户的账号。
var ErrUnbanNotLatestTenant = errors.New("latest ban is not in this workspace")

// UnbanUserTx 在单事务内完成"复核最近一次 ban + 复核目标角色 + 解封"。
//
// 锁顺序：先锁 users 行（SQLite 用一次空更新拿库级写锁），再在事务内
// 查询全平台最近一次成功 ban，最后锁目标在本租户的成员行复核角色。
// 检查与写入处于同一事务，避免"检查后、写入前"被并发的新封禁或角色
// 变更覆盖。
//
// expectedTargetRole 非空时复核目标成员行角色仍等于预读值；不一致返回
// ErrRoleChanged。这样并发把 owner/同级管理员降为 viewer 后，普通管理员
// 不能继续解封一个已经不再受其保护的目标（与 BanUserTx 的 B2 模式一致）。
func (r *userRepository) UnbanUserTx(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	actorID string,
	isSystemAdmin bool,
	expectedTargetRole types.TenantRole,
) (*types.User, error) {
	var user types.User
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "sqlite" {
			// SQLite 没有行锁；先对目标行做一次空更新，拿到库级写锁，
			// 与并发 SetUserActive / PurgeUserTx 串行化。
			if err := tx.Unscoped().Model(&types.User{}).
				Where("id = ?", targetUserID).
				UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}

		if err := lockUserRows(tx).Unscoped().
			Where("id = ?", targetUserID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		if user.DeletedAt.Valid {
			return ErrUserNotFound
		}

		if !isSystemAdmin {
			var latest types.AuditLog
			err := tx.Model(&types.AuditLog{}).
				Where("action = ? AND outcome = ? AND target_user_id = ?",
					types.AuditActionMemberBanned,
					types.AuditOutcomeSuccess,
					targetUserID).
				Order("id DESC").
				First(&latest).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUnbanNotLatestTenant
			}
			if err != nil {
				return err
			}
			if latest.TenantID != tenantID {
				return ErrUnbanNotLatestTenant
			}
		}

		// 目标保护快照复核（与 BanUserTx 对称）：服务层按预读的成员角色
		// 决定是否允许解封，这里锁住同一成员行并复核角色未变。并发提权/
		// 降级后返回 ErrRoleChanged，由服务层映射 409 让调用方刷新重试。
		if expectedTargetRole != "" {
			var member types.TenantMember
			err := tx.Clauses(forUpdateClause()).
				Where("tenant_id = ? AND user_id = ? AND status = ?",
					tenantID, targetUserID, types.TenantMemberStatusActive).
				First(&member).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoleChanged
			}
			if err != nil {
				return err
			}
			if member.Role != expectedTargetRole {
				return ErrRoleChanged
			}
		}

		now := time.Now()
		if err := tx.Unscoped().Model(&types.User{}).
			Where("id = ?", targetUserID).
			Updates(map[string]any{
				"is_active":  true,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		user.IsActive = true
		user.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}
