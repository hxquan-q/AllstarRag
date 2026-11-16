package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrResetTargetOwnsAnotherWorkspace 表示目标在调用方空间之外仍持有
// active owner 成员关系。密码是全局的，空间级重置会连带接管那个空间，
// 因此必须拒绝。
var ErrResetTargetOwnsAnotherWorkspace = errors.New("reset target owns another workspace")

// ErrResetTargetPrivileged 表示目标是平台级特权账号（系统管理员或可跨空间），
// 而调用方不在系统管理边界上。在事务内锁定并复核 users 行后返回，此时尚未
// 写密码，调用方也不应再吊销 token 或写审计。
var ErrResetTargetPrivileged = errors.New("reset target holds platform privileges")

// ErrResetTargetNotActiveMember 表示目标不是调用方空间的 active 成员
// （suspended / invited）。密码是全局的，空间级重置只能作用于本空间仍处于
// active 成员关系的目标（F1 安全热修）；在事务内复核，尚未写密码。
var ErrResetTargetNotActiveMember = errors.New("reset target is not an active member of the caller workspace")

// ResetPasswordTx 在单事务内完成「锁目标 users 行 → 复核特权目标 /
// 跨空间 owner / active 成员 → 写密码哈希」。
//
// 目的是消除 T30 的 TOCTOU：原实现先在事务外读成员关系，再写密码；若目标
// 在校验与写入之间被并发提升为其他空间的 owner，重置仍会成功。这里先锁
// users 行（挡住针对 users 行的并发写，含平台授权 UPDATE），再在事务内按
// id 顺序锁住目标的 active 成员行并复核跨空间 owner（真正挡住角色提升/
// 交接的是成员行 FOR UPDATE），最后写密码。特权目标检查同样放在事务内，
// 避免「事务外预检通过、加锁前被并发授予平台特权」的窗口。
//
// enforceOtherWorkspaceOwnerCheck 由服务层按「调用方站在空间边界而非系统
// 管理边界」计算，为真时同时执行两项空间级检查：目标必须是调用方空间的
// active 成员（F1：suspended 目标不可重置），且不得在其他空间持有 active
// owner。系统管理边界的重置不绑定任何空间，两项检查都跳过。
//
// SQLite 没有行锁，先做一次空更新拿库级写锁；真实生产库（PostgreSQL /
// MySQL）用 SELECT ... FOR UPDATE。
func (r *userRepository) ResetPasswordTx(
	ctx context.Context,
	targetUserID string,
	callerTenantID uint64,
	enforceOtherWorkspaceOwnerCheck bool,
	enforcePrivilegedTargetCheck bool,
	passwordHash string,
) (*types.User, error) {
	var user types.User
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "sqlite" {
			// SQLite 没有行锁；先取库级写锁，让复核看到已提交的成员关系。
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
		// 特权目标复核：必须在已锁定 users 行之后、写密码之前。命中时直接
		// 回滚事务，不写密码、不吊销 token、不写审计。
		if enforcePrivilegedTargetCheck && (user.IsSystemAdmin || user.CanAccessAllTenants) {
			return ErrResetTargetPrivileged
		}

		if enforceOtherWorkspaceOwnerCheck {
			// F1 安全热修：目标必须是调用方空间的 active 成员。用户迁移到
			// 新空间后，旧空间的关系变为 suspended；旧空间的管理员不得再
			// 通过本接口改其全局密码。该检查在已锁定 users 行之后、写密码
			// 之前，与下方的跨空间 owner 复核共用同一把锁时序。
			var activeInCaller int64
			if err := tx.Model(&types.TenantMember{}).
				Where("user_id = ? AND tenant_id = ? AND status = ?",
					targetUserID, callerTenantID, types.TenantMemberStatusActive).
				Count(&activeInCaller).Error; err != nil {
				return err
			}
			if activeInCaller == 0 {
				return ErrResetTargetNotActiveMember
			}
			var members []types.TenantMember
			q := tx.Model(&types.TenantMember{}).
				Where("user_id = ? AND status = ?",
					targetUserID, types.TenantMemberStatusActive).
				Order("id ASC")
			if tx.Name() == "postgres" || tx.Name() == "mysql" {
				q = q.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := q.Find(&members).Error; err != nil {
				return err
			}
			for _, member := range members {
				if member.Role == types.TenantRoleOwner && member.TenantID != callerTenantID {
					return ErrResetTargetOwnsAnotherWorkspace
				}
			}
		}

		now := time.Now()
		res := tx.Unscoped().Model(&types.User{}).
			Where("id = ?", targetUserID).
			Updates(map[string]any{
				"password_hash": passwordHash,
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrUserNotFound
		}
		user.PasswordHash = passwordHash
		user.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}
