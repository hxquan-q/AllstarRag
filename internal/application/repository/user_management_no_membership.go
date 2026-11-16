package repository

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// noMembershipUserQuery 构造「无空间」目录（T44）的去重用户集合查询：
//   - 无任何 active 成员关系（invited/suspended 不算 active；持邀请未
//     接受的账号当前不属于任何空间，视为无空间，接受后自动移出目录）；
//   - 排除墓碑（Model(&User{}) 的 GORM 软删条件自动加 users.deleted_at IS NULL）；
//   - 排除系统管理员与跨空间超管账号（他们属于 system_admin 目录）；
//   - 封禁账号保留（超管要能看到并解封/注销）；
//   - search 与既有 scoped 列表同口径（邮箱/用户名模糊、大小写不敏感）。
func (r *tenantMemberRepository) noMembershipUserQuery(
	ctx context.Context,
	search string,
) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&types.User{}).
		Where("users.id NOT IN (?)", r.scopedActiveMemberSubquery(ctx, 0, "")).
		Where("users.is_system_admin = ? AND users.can_access_all_tenants = ?", false, false)
	search = strings.TrimSpace(search)
	if search != "" {
		like := "%" + escapeLikePattern(search) + "%"
		q = q.Where(
			`(LOWER(users.email) LIKE LOWER(?) ESCAPE ? OR LOWER(users.username) LIKE LOWER(?) ESCAPE ?)`,
			like, likeEscapeChar, like, likeEscapeChar)
	}
	return q
}

// ListUserManagementUsersWithoutMembership 返回「无空间」目录的去重用户
// 分页行，投影与排序（created_at ASC, id ASC）与 scoped 列表同构。
func (r *tenantMemberRepository) ListUserManagementUsersWithoutMembership(
	ctx context.Context,
	search string,
	offset, limit int,
) ([]UserManagementScopedRow, error) {
	var rows []UserManagementScopedRow
	err := r.noMembershipUserQuery(ctx, search).
		Select(`users.id AS user_id,
			users.email AS email,
			users.username AS username,
			users.is_active AS is_active,
			users.created_at AS created_at`).
		Order("users.created_at ASC, users.id ASC").
		Offset(offset).
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CountUserManagementUsersWithoutMembership 统计「无空间」目录同口径的
// 用户总数。search 非空时随搜索缩小；目录徽标计数请传空 search
// （与 role_counts 的"不随过滤缩小"语义对齐）。
func (r *tenantMemberRepository) CountUserManagementUsersWithoutMembership(
	ctx context.Context,
	search string,
) (int64, error) {
	var total int64
	err := r.noMembershipUserQuery(ctx, search).Count(&total).Error
	return total, err
}
