package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SoftDeleteWithRole 是按 expectedRole 做比较交换（CAS）的软删：目标成员行
// 当前 role 与调用方预读的不一致时不动数据并返回 ErrRoleChanged。
//
// 目标保护（02-plan §2.2）先查快照再删除，两者之间存在并发提权窗口：
// admin 读到 viewer 后暂停，owner 把该行提升为 admin，随后删除会误删同级
// 管理员。把 expectedRole 带进 DELETE 的 WHERE 子句，让删除只对「仍是
// 那个角色」的成员行生效（T06 / N9 / N17 / N20）。
func (r *tenantMemberRepository) SoftDeleteWithRole(
	ctx context.Context,
	userID string,
	tenantID uint64,
	expectedRole types.TenantRole,
) error {
	res := r.db.WithContext(ctx).
		Where("user_id = ? AND tenant_id = ? AND role = ?",
			userID, tenantID, expectedRole).
		Delete(&types.TenantMember{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRoleChanged
	}
	return nil
}
