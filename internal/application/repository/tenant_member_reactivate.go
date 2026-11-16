package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// ReactivateSuspended 把被管理员挂起（status=suspended）的成员行恢复为
// active，用于「挂起后重新邀请/重新加入」：角色取本次请求传入的值，
// joined_at 记为本次重新加入时间，invited_by 取本次邀请人。
//
// 只匹配仍为 suspended 的行：并发请求已经把行改成 active 时 RowsAffected=0，
// 返回 gorm.ErrRecordNotFound，由服务层按「已是成员」处理。
// 软删（deleted_at 非空）的行不在匹配范围，重新加入走 Create 新建。
func (r *tenantMemberRepository) ReactivateSuspended(
	ctx context.Context,
	userID string,
	tenantID uint64,
	role types.TenantRole,
	invitedBy *string,
) (*types.TenantMember, error) {
	now := time.Now()
	res := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Where("user_id = ? AND tenant_id = ? AND status = ?",
			userID, tenantID, types.TenantMemberStatusSuspended).
		Updates(map[string]any{
			"status":     types.TenantMemberStatusActive,
			"role":       role,
			"joined_at":  now,
			"invited_by": invitedBy,
			"updated_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var member types.TenantMember
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		First(&member).Error; err != nil {
		return nil, err
	}
	return &member, nil
}
