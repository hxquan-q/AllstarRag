package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// BanTxPauseBeforeAudit 是 BanUserTx 的测试暂停点名称：此时状态已写入、
// ban 审计尚未写入，二者仍在同一事务内。
const BanTxPauseBeforeAudit = "before_ban_audit"

// BanTxPauseHook 是测试专用的事务内暂停钩子。生产请求不会设置它。
type BanTxPauseHook func(stage string)

type banTxPauseHookKey struct{}

// WithBanTxPauseHook 把暂停钩子挂到 context 上，只供测试使用。
func WithBanTxPauseHook(ctx context.Context, hook BanTxPauseHook) context.Context {
	return context.WithValue(ctx, banTxPauseHookKey{}, hook)
}

func banTxPauseHookFromContext(ctx context.Context) BanTxPauseHook {
	hook, _ := ctx.Value(banTxPauseHookKey{}).(BanTxPauseHook)
	return hook
}

// BanUserTx 在单事务内完成「锁 users 行 + 复核保护 + 写 is_active=false +
// 吊销全部 token + 写 ban 审计」。
//
// 锁序与 SetUserActive 一致：先锁系统管理员行（保证“最后一个系统管理员”
// 判定与撤权串行化），再锁目标用户行。SQLite 没有行锁，先做一次空更新
// 拿库级写锁。
//
// 把 ban 审计放进同一事务，是让 UnbanUserTx 在用户行锁内读到的「最近一次
// ban」一定是已提交的：解封要么在 ban 提交前阻塞，要么看到完整的最新 ban。
// token 吊销也在同一事务内，失败则整次封禁回滚。
//
// 目标已经是封禁态时幂等返回，不写新审计，避免重复 ban 改写“全平台最近
// 一次 ban”的归属。audit 为 nil 时只改状态（调用方没有审计服务）。
//
// expectedTargetRole 非空时，在同一事务内锁住目标在本租户的 active 成员行
// 并复核角色仍等于预读值；不一致返回 ErrRoleChanged。这样并发把 viewer
// 提升为 admin 后，普通管理员不能再封禁已受同级保护的目标。平台级调用
// 传空角色跳过该复核（不依赖成员关系）。
func (r *userRepository) BanUserTx(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	actorID string,
	expectedTargetRole types.TenantRole,
	audit *types.AuditLog,
) (*types.User, error) {
	var user types.User
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		admins, err := lockSystemAdmins(tx)
		if err != nil {
			return err
		}
		if tx.Name() == "sqlite" {
			// SQLite 没有行锁；空更新拿库级写锁，与并发解封/注销串行化。
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
		// 已是封禁态：幂等返回，不写新审计。
		if !user.IsActive {
			return nil
		}
		if targetUserID == actorID {
			return ErrCannotDisableSelf
		}
		if user.IsSystemAdmin && activeSystemAdminCount(admins) <= 1 {
			return ErrCannotDisableLastAdmin
		}
		// 目标保护快照复核（N9 / N20）：服务层按预读的成员角色决定是否
		// 允许，这里锁住同一成员行并复核角色未变。并发提权（viewer→admin）
		// 后，普通管理员不能继续封禁该目标；expectedTargetRole 为空表示
		// 平台级调用，不依赖成员关系。
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
		if err := tx.Model(&types.User{}).Where("id = ?", targetUserID).
			Updates(map[string]any{
				"is_active":  false,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		// 与状态变更同事务：失败则整次封禁回滚。
		if err := tx.Model(&types.AuthToken{}).Where("user_id = ?", targetUserID).
			Update("is_revoked", true).Error; err != nil {
			return err
		}
		user.IsActive = false
		user.UpdatedAt = now

		if audit == nil {
			return nil
		}
		if hook := banTxPauseHookFromContext(ctx); hook != nil {
			hook(BanTxPauseBeforeAudit)
		}
		if audit.CreatedAt.IsZero() {
			audit.CreatedAt = now
		}
		return tx.WithContext(ctx).Create(audit).Error
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}
