package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrPurgeTargetStillOwner 表示目标仍在某个空间持有 active owner。
	// 墓碑化前必须先完成空间交接，否则会留下无主空间。
	ErrPurgeTargetStillOwner = errors.New("user still owns a workspace")
	// ErrPurgeTargetPrivileged 表示目标仍是系统管理员或跨空间超管。
	// 必须先撤销特权，才能彻底删除。
	ErrPurgeTargetPrivileged = errors.New("user still holds elevated privileges")
	// ErrPurgeTargetCrossTenant 表示目标还属于其他空间，而调用方不是
	// 系统管理员。跨空间删除必须由超管处理（AC-19b）。
	ErrPurgeTargetCrossTenant = errors.New("user belongs to other workspaces")
)

// PurgeUserTx 在单事务内完成账号墓碑化与个人数据清理。
//
// 步骤：锁 users 行 → 复核目标未删除/未持有特权 → 锁住目标的 active
// 成员行并复核 owner / 跨空间归属 → 退役并删除会话 → 清理成员关系、
// 邀请、token、收藏、pins、MCP token、环境变量、个人通知 → 匿名化并
// 软删 users 行 → 写审计。资产表（knowledge_bases / knowledge /
// custom_agents 等）完全不动，creator_id 继续指向墓碑。
//
// 成员行加锁把"服务层校验后、事务提交前"的并发提权挡住：并发的
// UpdateRole / TransferOwner 要么先提交（这里能看到 owner 行并拒绝），
// 要么被行锁挡住，待墓碑提交后因成员行已软删而失败回滚。
func (r *userRepository) PurgeUserTx(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	callerIsSystemAdmin bool,
	audit *types.AuditLog,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, err := lockPurgeTarget(tx, targetUserID)
		if err != nil {
			return err
		}
		if user.DeletedAt.Valid {
			return ErrUserNotFound
		}
		// 复核目标保护，避免"服务层校验后、事务提交前"被提权。
		if user.IsSystemAdmin || user.CanAccessAllTenants {
			return ErrPurgeTargetPrivileged
		}
		var members []types.TenantMember
		if err := lockPurgeMemberRows(tx).
			Where("user_id = ? AND status = ?",
				targetUserID, types.TenantMemberStatusActive).
			Find(&members).Error; err != nil {
			return err
		}
		for _, member := range members {
			if member.Role == types.TenantRoleOwner {
				return ErrPurgeTargetStillOwner
			}
			// AC-19b：非系统管理员不能删除仍属于其他空间的账号。
			if !callerIsSystemAdmin && member.TenantID != tenantID {
				return ErrPurgeTargetCrossTenant
			}
		}

		now := time.Now().UTC()
		if err := purgeUserSessionsTx(tx, targetUserID, now); err != nil {
			return err
		}
		if err := purgeUserPersonalDataTx(tx, targetUserID); err != nil {
			return err
		}
		if err := tombstoneUserTx(tx, targetUserID, now); err != nil {
			return err
		}
		// 审计与数据清理同事务：要么一起成功，要么一起回滚。
		if audit.CreatedAt.IsZero() {
			audit.CreatedAt = now
		}
		return tx.WithContext(ctx).Create(audit).Error
	})
}

// lockPurgeTarget 锁定目标 users 行并读取墓碑标记。PostgreSQL/MySQL 用
// SELECT ... FOR UPDATE；SQLite 没有行锁，先用一次空更新拿写锁，再在
// 同一事务里读取，语义上等价于"锁住目标行"。
func lockPurgeTarget(tx *gorm.DB, targetUserID string) (*types.User, error) {
	if tx.Name() == "sqlite" {
		if err := tx.Unscoped().Model(&types.User{}).
			Where("id = ?", targetUserID).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return nil, err
		}
	}
	var user types.User
	err := lockUserRows(tx).Unscoped().Where("id = ?", targetUserID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// lockPurgeMemberRows 对目标的 active 成员行加 UPDATE 锁。
// PostgreSQL/MySQL 用 SELECT ... FOR UPDATE；SQLite 没有行锁，事务内对
// users 行的空更新已取得库级写锁，这里直接读即可。
func lockPurgeMemberRows(tx *gorm.DB) *gorm.DB {
	switch tx.Name() {
	case "postgres", "mysql":
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	default:
		return tx
	}
}

// purgeUserSessionsTx 退役并删除账号会话。只有当沙箱分配表存在时才走
// RetireSessionsTx（Lite/测试库可能没有沙箱表），否则直接删会话行。
func purgeUserSessionsTx(tx *gorm.DB, userID string, at time.Time) error {
	migrator := tx.Migrator()
	if migrator.HasTable("session_sandbox_assignments") &&
		migrator.HasTable("sandbox_user_machines") {
		ids, err := ownerSessionIDs(tx, userID)
		if err != nil {
			return err
		}
		if _, err := RetireSessionsTx(tx, ids, at); err != nil {
			return err
		}
	}
	// Unscoped：已经软删的历史会话也一并清掉，避免以账号为键残留。
	return tx.Unscoped().Where("user_id = ?", userID).Delete(&types.Session{}).Error
}

// purgeUserPersonalDataTx 清理账号级的个人数据。顺序无关，任一失败都
// 会让整个事务回滚。
func purgeUserPersonalDataTx(tx *gorm.DB, userID string) error {
	steps := []func(*gorm.DB) error{
		func(tx *gorm.DB) error {
			// tenant_members 保留行供审计/资产引用，只做软删。
			return tx.Where("user_id = ?", userID).Delete(&types.TenantMember{}).Error
		},
		func(tx *gorm.DB) error {
			// 目标收到的邀请（invitee_user_id）与目标发出的邀请 /
			// 分享链接（invited_by）都要清理，避免墓碑后仍留下可用的
			// pending 邀请（AC-19 / AC-25）。
			return tx.Where("invitee_user_id = ? OR invited_by = ?", userID, userID).
				Delete(&types.TenantInvitation{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Unscoped().Where("user_id = ?", userID).
				Delete(&types.AuthToken{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Where("user_id = ?", userID).
				Delete(&types.UserResourceFavorite{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Where("user_id = ?", userID).Delete(&userKBPinRow{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Where("user_id = ?", userID).Delete(&types.MCPOAuthToken{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Where("principal_id = ?", userID).
				Delete(&types.TenantUserEnvVar{}).Error
		},
		func(tx *gorm.DB) error {
			return tx.Where("user_id = ?", userID).Delete(&types.Notification{}).Error
		},
	}
	for _, step := range steps {
		if err := step(tx); err != nil {
			return err
		}
	}
	return nil
}

// tombstoneUserTx 把 users 行匿名化并软删。tenant_id 写 NULL（读回即 0），
// 与现有 UpdateUser 处理可空外键的方式一致；邮箱/用户名用 id 派生的唯一
// 匿名值，释放原值供重新注册。
func tombstoneUserTx(tx *gorm.DB, userID string, at time.Time) error {
	updates := map[string]interface{}{
		"username":               "deleted-" + userID,
		"email":                  "deleted-" + userID + "@deleted.local",
		"password_hash":          "",
		"avatar":                 "",
		"preferences":            types.UserPreferences{},
		"tenant_id":              nil,
		"is_active":              false,
		"is_system_admin":        false,
		"can_access_all_tenants": false,
		"deleted_at":             at,
		"updated_at":             at,
	}
	res := tx.Unscoped().Model(&types.User{}).Where("id = ?", userID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}
