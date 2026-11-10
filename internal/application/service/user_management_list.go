package service

import (
	"context"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 用户管理列表的分页默认值与上限，与现有 /members 列表保持一致。
const (
	userManagementDefaultPageSize = 20
	userManagementMaxPageSize     = 100
)

// userManagementListRepo 是列表接口需要的窄仓储接口。单独定义而不是扩展
// interfaces.TenantMemberRepository，避免给 /members 等既有调用方强加
// 新的实现要求；真实仓储 *tenantMemberRepository 已在构造时自动探测。
type userManagementListRepo interface {
	ListUserManagementPaged(
		ctx context.Context, tenantID uint64, role types.TenantRole,
		search string, offset, limit int,
	) ([]apprepo.UserManagementRow, error)
	CountUserManagement(
		ctx context.Context, tenantID uint64, role types.TenantRole, search string,
	) (int64, error)
	CountUserManagementRoleCounts(
		ctx context.Context, tenantID uint64,
	) (map[types.TenantRole]int64, error)
	// 多角色变体（T22）：role IN (...) 过滤，供「编辑」目录一次查询。
	ListUserManagementPagedWithRoles(
		ctx context.Context, tenantID uint64, roles []types.TenantRole,
		search string, offset, limit int,
	) ([]apprepo.UserManagementRow, error)
	CountUserManagementWithRoles(
		ctx context.Context, tenantID uint64, roles []types.TenantRole, search string,
	) (int64, error)
}

// ListUsers 返回用户管理页的成员列表。
//
// 权限在服务层自证（AC-38/C4）：非 owner/普通管理员且非系统管理员一律
// 403，不能只依赖路由守卫。系统管理员不需要在本空间持有空间角色。
//
// 列表口径：
//   - 只含 active 成员关系，墓碑用户（users.deleted_at 非空）不出现；
//   - 被封禁用户（users.is_active=false）仍出现，status="banned"；
//   - role 为空表示全部，非法值 400；
//   - role_counts 始终是整空间四类角色的计数，不随 role/q 过滤缩小。
func (s *userManagementService) ListUsers(
	ctx context.Context,
	tenantID uint64,
	role types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	if err := s.requireManagerOrSystemAdmin(ctx, tenantID); err != nil {
		return nil, err
	}
	if role != "" && !role.IsValid() {
		return nil, apperrors.NewValidationError(
			"role must be one of viewer, contributor, admin, owner")
	}
	if s.listRepo == nil {
		return nil, apperrors.NewInternalServerError(
			"user management list repository is not configured")
	}

	page, pageSize = clampUserManagementPagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, err := s.listRepo.ListUserManagementPaged(
		ctx, tenantID, role, query, offset, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := s.listRepo.CountUserManagement(ctx, tenantID, role, query)
	if err != nil {
		return nil, err
	}
	counts, err := s.listRepo.CountUserManagementRoleCounts(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	items := make([]interfaces.UserManagementListItem, 0, len(rows))
	for _, row := range rows {
		status := "active"
		if !row.IsActive {
			status = "banned"
		}
		items = append(items, interfaces.UserManagementListItem{
			UserID:    row.UserID,
			Email:     row.Email,
			Username:  row.Username,
			Role:      row.Role,
			Status:    status,
			IsActive:  row.IsActive,
			CreatedAt: row.CreatedAt,
		})
	}
	return &interfaces.UserManagementListResult{
		Items:      items,
		Total:      total,
		RoleCounts: counts,
	}, nil
}

// clampUserManagementPagination 做服务层防御性兜底：handler 已对
// page<1 / page_size>100 直接返回 400，这里只保证被其他入口复用时
// 不会产生负 offset 或超大 limit。
func clampUserManagementPagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = userManagementDefaultPageSize
	}
	if pageSize > userManagementMaxPageSize {
		pageSize = userManagementMaxPageSize
	}
	return page, pageSize
}
