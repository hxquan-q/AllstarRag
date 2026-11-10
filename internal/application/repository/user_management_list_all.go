package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// UserManagementScopedRow 是多空间视图（scope=other/all）用户行的投影：
// 用户身份字段，不含成员关系（memberships 由单独查询回填）。
type UserManagementScopedRow struct {
	UserID    string
	Email     string
	Username  string
	IsActive  bool
	CreatedAt time.Time
}

// UserManagementMembershipRow 是多空间视图中一条成员关系投影，
// TenantName 来自 LEFT JOIN tenants。
type UserManagementMembershipRow struct {
	UserID     string
	TenantID   uint64
	TenantName string
	Role       types.TenantRole
}

// scopedUserQuery 构造多空间视图的去重用户集合查询（users 表无 JOIN，
// 天然按用户去重）：
//   - 排除墓碑（Model(&User{}) 的 GORM 软删条件自动加 users.deleted_at IS NULL）；
//   - other：本空间之外任一空间持有 active 成员关系的用户；
//   - all：全平台全部用户；
//   - role 非空时匹配"任意成员关系的角色"（other 视图只看其它空间的关系，
//     all 视图不限空间）；
//   - search 按邮箱/用户名模糊匹配，与单空间列表同口径。
func (r *tenantMemberRepository) scopedUserQuery(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	role types.TenantRole,
	search string,
) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&types.User{})
	if scope == interfaces.UserManagementScopeOther {
		q = q.Where("users.id IN (?)", r.scopedActiveMemberSubquery(ctx, tenantID, role))
	} else if role != "" {
		q = q.Where("users.id IN (?)", r.scopedActiveMemberSubquery(ctx, 0, role))
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

// scopedActiveMemberSubquery 返回 active 成员关系的 user_id 子查询。
// tenantID 非 0 时排除该空间（other 视图）；为 0 时不限空间（all 视图
// 的 role 过滤）。role 为空时不过滤角色。
func (r *tenantMemberRepository) scopedActiveMemberSubquery(
	ctx context.Context,
	tenantID uint64,
	role types.TenantRole,
) *gorm.DB {
	sub := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Select("tenant_members.user_id").
		Where("tenant_members.status = ?", types.TenantMemberStatusActive)
	if tenantID != 0 {
		sub = sub.Where("tenant_members.tenant_id <> ?", tenantID)
	}
	if role != "" {
		sub = sub.Where("tenant_members.role = ?", role)
	}
	return sub
}

// ListUserManagementScopedPaged 按多空间视图返回去重用户分页行，
// 排序 created_at ASC, id ASC 保证翻页稳定。顶层 Role 不在此取：
// 由 memberships 查询回填（视图内第一条关系的角色）。
func (r *tenantMemberRepository) ListUserManagementScopedPaged(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	role types.TenantRole,
	search string,
	offset, limit int,
) ([]UserManagementScopedRow, error) {
	var rows []UserManagementScopedRow
	err := r.scopedUserQuery(ctx, tenantID, scope, role, search).
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

// CountUserManagementScoped 统计多空间视图同条件命中的去重用户总数。
func (r *tenantMemberRepository) CountUserManagementScoped(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	role types.TenantRole,
	search string,
) (int64, error) {
	var total int64
	err := r.scopedUserQuery(ctx, tenantID, scope, role, search).Count(&total).Error
	return total, err
}

// CountUserManagementScopedRoleCounts 返回多空间视图的角色计数：按
// "视图内持有该角色的去重用户数"计，同一用户在多个空间持同一角色只算
// 一次。T06 起只暴露 owner/admin/viewer 三桶；contributor 历史行不计入。
// other 视图只统计其它空间的关系，all 视图不限空间。
func (r *tenantMemberRepository) CountUserManagementScopedRoleCounts(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
) (map[types.TenantRole]int64, error) {
	counts := map[types.TenantRole]int64{
		types.TenantRoleViewer: 0,
		types.TenantRoleAdmin:  0,
		types.TenantRoleOwner:  0,
	}
	q := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Joins("INNER JOIN users ON users.id = tenant_members.user_id AND users.deleted_at IS NULL").
		Where("tenant_members.status = ?", types.TenantMemberStatusActive)
	if scope == interfaces.UserManagementScopeOther {
		q = q.Where("tenant_members.tenant_id <> ?", tenantID)
	}
	var rows []struct {
		Role  types.TenantRole
		Total int64
	}
	err := q.Select(
		"tenant_members.role AS role, COUNT(DISTINCT tenant_members.user_id) AS total",
	).Group("tenant_members.role").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, ok := counts[row.Role]; ok {
			counts[row.Role] = row.Total
		}
	}
	return counts, nil
}

// ListUserManagementMemberships 返回给定用户在视图内的全部 active
// 成员关系（含租户名），按 tenant_id 升序。other 视图只含其它空间的
// 关系，all 视图含本空间。租户行缺失时 tenant_name 为空串（租户被删
// 的历史数据仍要展示成员关系本身）。
func (r *tenantMemberRepository) ListUserManagementMemberships(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	userIDs []string,
) ([]UserManagementMembershipRow, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	q := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Joins("LEFT JOIN tenants ON tenants.id = tenant_members.tenant_id").
		Where("tenant_members.user_id IN ? AND tenant_members.status = ?",
			userIDs, types.TenantMemberStatusActive)
	if scope == interfaces.UserManagementScopeOther {
		q = q.Where("tenant_members.tenant_id <> ?", tenantID)
	}
	var rows []UserManagementMembershipRow
	err := q.Select(`tenant_members.user_id AS user_id,
		tenant_members.tenant_id AS tenant_id,
		tenants.name AS tenant_name,
		tenant_members.role AS role`).
		Order("tenant_members.user_id ASC, tenant_members.tenant_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
