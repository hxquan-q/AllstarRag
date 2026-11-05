package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// UserManagementRow 是用户管理列表的 JOIN 投影：成员关系 + 用户身份字段。
// 单独定义而不是复用 types.User，避免把密码哈希等敏感列带出仓储层。
type UserManagementRow struct {
	UserID    string
	Email     string
	Username  string
	Role      types.TenantRole
	IsActive  bool
	CreatedAt time.Time
}

// userManagementBaseQuery 构造用户管理列表/计数的公共查询：
//   - 只取目标空间的 active 成员关系（GORM 软删条件自动加在 tenant_members 上）；
//   - JOIN users 并排除墓碑（users.deleted_at IS NULL）；
//   - 不排除被封禁用户（users.is_active=false），封禁只影响 status 展示；
//   - role 非空时按角色过滤；
//   - search 按邮箱/用户名模糊匹配，大小写不敏感。
func (r *tenantMemberRepository) userManagementBaseQuery(
	ctx context.Context,
	tenantID uint64,
	role types.TenantRole,
	search string,
) *gorm.DB {
	q := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Joins("INNER JOIN users ON users.id = tenant_members.user_id AND users.deleted_at IS NULL").
		Where("tenant_members.tenant_id = ? AND tenant_members.status = ?",
			tenantID, types.TenantMemberStatusActive)

	if role != "" {
		q = q.Where("tenant_members.role = ?", role)
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

// ListUserManagementPaged 按租户 + 角色 + 搜索条件分页返回成员列表，
// 排序与现有 /members 一致（joined_at ASC, id ASC），保证翻页稳定。
func (r *tenantMemberRepository) ListUserManagementPaged(
	ctx context.Context,
	tenantID uint64,
	role types.TenantRole,
	search string,
	offset, limit int,
) ([]UserManagementRow, error) {
	var rows []UserManagementRow
	err := r.userManagementBaseQuery(ctx, tenantID, role, search).
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

// CountUserManagement 统计与列表同条件命中的成员总数（Total）。
func (r *tenantMemberRepository) CountUserManagement(
	ctx context.Context,
	tenantID uint64,
	role types.TenantRole,
	search string,
) (int64, error) {
	var total int64
	err := r.userManagementBaseQuery(ctx, tenantID, role, search).Count(&total).Error
	return total, err
}

// CountUserManagementRoleCounts 返回整空间（不受 role/q 过滤影响）的
// 角色目录计数。四个角色键始终存在，无成员时为 0。
func (r *tenantMemberRepository) CountUserManagementRoleCounts(
	ctx context.Context,
	tenantID uint64,
) (map[types.TenantRole]int64, error) {
	counts := map[types.TenantRole]int64{
		types.TenantRoleViewer:      0,
		types.TenantRoleContributor: 0,
		types.TenantRoleAdmin:       0,
		types.TenantRoleOwner:       0,
	}
	var rows []struct {
		Role  types.TenantRole
		Total int64
	}
	err := r.userManagementBaseQuery(ctx, tenantID, "", "").
		Select("tenant_members.role AS role, COUNT(*) AS total").
		Group("tenant_members.role").
		Scan(&rows).Error
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
