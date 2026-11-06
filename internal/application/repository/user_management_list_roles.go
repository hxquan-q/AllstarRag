package repository

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// 本文件是用户管理列表的多角色（role IN (...））变体，供 T22 的
// 「编辑」目录一次查询 contributor + admin 使用。单角色查询保持原样，
// 两个入口共用同一套过滤口径（active 成员关系、排除墓碑、封禁不排除、
// 搜索大小写不敏感）。

// userManagementRolesBaseQuery 构造单空间多角色过滤的公共查询。
func (r *tenantMemberRepository) userManagementRolesBaseQuery(
	ctx context.Context,
	tenantID uint64,
	roles []types.TenantRole,
	search string,
) *gorm.DB {
	q := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Joins("INNER JOIN users ON users.id = tenant_members.user_id AND users.deleted_at IS NULL").
		Where("tenant_members.tenant_id = ? AND tenant_members.status = ?",
			tenantID, types.TenantMemberStatusActive)
	// 按用户去重：同一用户在同空间有多条成员关系时只取最早一条命中
	// 角色的关系（T22 契约 total/分页按去重用户计）。用 MIN(id) 子查询
	// 而不是 GROUP BY 裸列，兼容 PostgreSQL。
	sub := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Select("MIN(id)").
		Where("tenant_members.tenant_id = ? AND tenant_members.status = ?",
			tenantID, types.TenantMemberStatusActive)
	if len(roles) > 0 {
		sub = sub.Where("tenant_members.role IN ?", roles)
	}
	sub = sub.Group("tenant_members.user_id")
	q = q.Where("tenant_members.id IN (?)", sub)
	search = strings.TrimSpace(search)
	if search != "" {
		like := "%" + escapeLikePattern(search) + "%"
		q = q.Where(
			`(LOWER(users.email) LIKE LOWER(?) ESCAPE ? OR LOWER(users.username) LIKE LOWER(?) ESCAPE ?)`,
			like, likeEscapeChar, like, likeEscapeChar)
	}
	return q
}

// ListUserManagementPagedWithRoles 按租户 + 角色集合 + 搜索条件分页返回
// 成员列表，排序与单角色版本一致。
func (r *tenantMemberRepository) ListUserManagementPagedWithRoles(
	ctx context.Context,
	tenantID uint64,
	roles []types.TenantRole,
	search string,
	offset, limit int,
) ([]UserManagementRow, error) {
	var rows []UserManagementRow
	err := r.userManagementRolesBaseQuery(ctx, tenantID, roles, search).
		Select(`tenant_members.user_id AS user_id,
			users.email AS email,
			users.username AS username,
			tenant_members.role AS role,
			users.is_active AS is_active,
			users.created_at AS created_at`).
		Order("tenant_members.joined_at ASC, tenant_members.id ASC").
		Offset(offset).
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CountUserManagementWithRoles 统计多角色过滤命中的成员总数。
func (r *tenantMemberRepository) CountUserManagementWithRoles(
	ctx context.Context,
	tenantID uint64,
	roles []types.TenantRole,
	search string,
) (int64, error) {
	var total int64
	err := r.userManagementRolesBaseQuery(ctx, tenantID, roles, search).
		Distinct("tenant_members.user_id").Count(&total).Error
	return total, err
}

// scopedUserRolesQuery 构造多空间视图的多角色过滤查询：
//   - 排除墓碑（Model(&User{}) 的软删条件）；
//   - other：本空间之外任一空间持有 active 成员关系的用户；
//   - all：全平台全部用户；
//   - roles 非空时匹配任意一个成员关系的角色。
func (r *tenantMemberRepository) scopedUserRolesQuery(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	roles []types.TenantRole,
	search string,
) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&types.User{})
	if scope == interfaces.UserManagementScopeOther {
		q = q.Where("users.id IN (?)", r.scopedActiveMemberRolesSubquery(ctx, tenantID, roles))
	} else if len(roles) > 0 {
		q = q.Where("users.id IN (?)", r.scopedActiveMemberRolesSubquery(ctx, 0, roles))
	}
	search = strings.TrimSpace(search)
	if search != "" {
		like := "%" + escapeLikePattern(search) + "%"
		q = q.Where(
			`(LOWER(users.email) LIKE LOWER(?) ESCAPE ? OR LOWER(users.username) LIKE LOWER(?) ESCAPE ?)`,
			like, likeEscapeChar, like, likeEscapeChar)
	}
	return q
}

// scopedActiveMemberRolesSubquery 返回 active 成员关系的 user_id 子查询，
// 按角色集合过滤；tenantID 非 0 时排除该空间。
func (r *tenantMemberRepository) scopedActiveMemberRolesSubquery(
	ctx context.Context,
	tenantID uint64,
	roles []types.TenantRole,
) *gorm.DB {
	sub := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Select("tenant_members.user_id").
		Where("tenant_members.status = ?", types.TenantMemberStatusActive)
	if tenantID != 0 {
		sub = sub.Where("tenant_members.tenant_id <> ?", tenantID)
	}
	if len(roles) > 0 {
		sub = sub.Where("tenant_members.role IN ?", roles)
	}
	return sub
}

// ListUserManagementScopedPagedWithRoles 按多空间视图 + 多角色过滤返回
// 去重用户分页行；memberships 由既有查询回填。
func (r *tenantMemberRepository) ListUserManagementScopedPagedWithRoles(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	roles []types.TenantRole,
	search string,
	offset, limit int,
) ([]UserManagementScopedRow, error) {
	var rows []UserManagementScopedRow
	err := r.scopedUserRolesQuery(ctx, tenantID, scope, roles, search).
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

// CountUserManagementScopedWithRoles 统计多空间视图 + 多角色过滤命中的
// 去重用户总数。
func (r *tenantMemberRepository) CountUserManagementScopedWithRoles(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	roles []types.TenantRole,
	search string,
) (int64, error) {
	var total int64
	err := r.scopedUserRolesQuery(ctx, tenantID, scope, roles, search).Count(&total).Error
	return total, err
}
