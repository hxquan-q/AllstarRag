package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// SetTenantIDIfEmpty 只在 users.tenant_id 仍为空（NULL / 0）时把它指向
// tenantID，返回是否实际写入。
//
// 为什么不用 UpdateUser：UpdateUser 是整行 Save（只剔除 is_active），
// 成员加入时的「回填 home」会拿一份用户快照覆盖掉并发发生的密码重置、
// 撤权等安全字段（N23 / N26）。这里只写 tenant_id / updated_at 两列，
// 并用 WHERE tenant_id = 0 做条件更新，让并发的 home 回填只有一方生效。
func (r *userRepository) SetTenantIDIfEmpty(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (bool, error) {
	if userID == "" || tenantID == 0 {
		return false, nil
	}
	res := r.db.WithContext(ctx).
		Model(&types.User{}).
		Where("id = ? AND (tenant_id IS NULL OR tenant_id = 0)", userID).
		Updates(map[string]any{
			"tenant_id":  tenantID,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
