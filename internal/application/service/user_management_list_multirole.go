package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// validateUserManagementRoles 校验多角色过滤参数。roles 为空表示全部；
// 任一段为空、带空格或不是合法角色都返回 400。trim 责任在 handler，
// 服务层保持严格口径（与 T22 红阶段用例一致）。
func validateUserManagementRoles(roles []types.TenantRole) error {
	for _, role := range roles {
		if role == "" || !role.IsValid() {
			return apperrors.NewValidationError(
				"role must be one of viewer, contributor, admin, owner")
		}
	}
	return nil
}

// ListUsersWithRoles 是 ListUsers 的多角色版本：roles 命中任意一个即返回，
// 为空表示全部；total/分页按现有单空间口径，RoleCounts 不随过滤缩小。
func (s *userManagementService) ListUsersWithRoles(
	ctx context.Context,
	tenantID uint64,
	roles []types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	if err := s.requireManagerOrSystemAdmin(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := validateUserManagementRoles(roles); err != nil {
		return nil, err
	}
	if s.listRepo == nil {
		return nil, apperrors.NewInternalServerError(
			"user management list repository is not configured")
	}

	page, pageSize = clampUserManagementPagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, err := s.listRepo.ListUserManagementPagedWithRoles(
		ctx, tenantID, roles, query, offset, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := s.listRepo.CountUserManagementWithRoles(ctx, tenantID, roles, query)
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

// ListUsersScopedWithRoles 是 ListUsersScoped 的多角色版本。scope=current
// 等价 ListUsersWithRoles；other/all 仍只允许系统管理员（403）。
func (s *userManagementService) ListUsersScopedWithRoles(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	roles []types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	switch scope {
	case interfaces.UserManagementScopeCurrent:
		return s.ListUsersWithRoles(ctx, tenantID, roles, query, page, pageSize)
	case interfaces.UserManagementScopeOther, interfaces.UserManagementScopeAll:
	default:
		return nil, apperrors.NewValidationError(
			"scope must be one of current, other, all")
	}
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if err := validateUserManagementRoles(roles); err != nil {
		return nil, err
	}
	if s.scopedListRepo == nil {
		return nil, apperrors.NewInternalServerError(
			"user management list repository is not configured")
	}

	page, pageSize = clampUserManagementPagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, err := s.scopedListRepo.ListUserManagementScopedPagedWithRoles(
		ctx, tenantID, scope, roles, query, offset, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := s.scopedListRepo.CountUserManagementScopedWithRoles(
		ctx, tenantID, scope, roles, query)
	if err != nil {
		return nil, err
	}
	counts, err := s.scopedListRepo.CountUserManagementScopedRoleCounts(
		ctx, tenantID, scope)
	if err != nil {
		return nil, err
	}
	noMembership, err := s.noMembershipBadgeCount(ctx, scope)
	if err != nil {
		return nil, err
	}

	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	memberships, err := s.scopedListRepo.ListUserManagementMemberships(
		ctx, tenantID, scope, userIDs)
	if err != nil {
		return nil, err
	}
	membershipsByUser := make(map[string][]interfaces.UserManagementMembership, len(rows))
	for _, m := range memberships {
		membershipsByUser[m.UserID] = append(membershipsByUser[m.UserID],
			interfaces.UserManagementMembership{
				TenantID:   m.TenantID,
				TenantName: m.TenantName,
				Role:       m.Role,
			})
	}

	items := make([]interfaces.UserManagementListItem, 0, len(rows))
	for _, row := range rows {
		status := "active"
		if !row.IsActive {
			status = "banned"
		}
		item := interfaces.UserManagementListItem{
			UserID:      row.UserID,
			Email:       row.Email,
			Username:    row.Username,
			Status:      status,
			IsActive:    row.IsActive,
			CreatedAt:   row.CreatedAt,
			Memberships: membershipsByUser[row.UserID],
		}
		if len(item.Memberships) > 0 {
			item.Role = item.Memberships[0].Role
		}
		items = append(items, item)
	}
	return &interfaces.UserManagementListResult{
		Items:             items,
		Total:             total,
		RoleCounts:        counts,
		NoMembershipCount: noMembership,
	}, nil
}
