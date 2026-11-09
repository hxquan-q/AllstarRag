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

// ResetPasswordTx 在单事务内完成「锁目标 users 行 → 复核跨空间 owner →
// 写密码哈希」。
//
// 目的是消除 T30 的 TOCTOU：原实现先在事务外读成员关系，再写密码；若目标
// 在校验与写入之间被并发提升为其他空间的 owner，重置仍会成功。这里先锁
// users 行（提升/交接路径更新成员行时会被这把锁挡住），再在事务内按
// id 顺序锁住目标的 active 成员行并复核，最后写密码。
//
// SQLite 没有行锁，先做一次空更新拿库级写锁；真实生产库（PostgreSQL /
// MySQL）用 SELECT ... FOR UPDATE。
func (r *userRepository) ResetPasswordTx(
	ctx context.Context,
	targetUserID string,
	callerTenantID uint64,
	enforceOtherWorkspaceOwnerCheck bool,
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

		if enforceOtherWorkspaceOwnerCheck {
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
