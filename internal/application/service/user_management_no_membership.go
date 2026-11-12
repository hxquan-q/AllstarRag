package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// noMembershipBadgeCount 返回「无空间」目录徽标计数：仅 scope=all 需要
// （全平台视图才渲染该目录），其余 scope 恒为 0。计数按空 search 口径，
// 不随 q/role 过滤缩小（与 role_counts 徽标语义一致）。
func (s *userManagementService) noMembershipBadgeCount(
	ctx context.Context,
	scope interfaces.UserManagementListScope,
) (int64, error) {
	if scope != interfaces.UserManagementScopeAll {
		return 0, nil
	}
	return s.scopedListRepo.CountUserManagementUsersWithoutMembership(ctx, "")
}

// ListUsersWithoutMembership 返回「无空间」目录：无任何 active 成员关系
// 的非墓碑普通账号（排除系统管理员/跨空间超管）。仅系统管理员可用，
// 服务层自证（C4），非系统管理员 403。items 的 Memberships 为空数组、
// Role 为空串；封禁账号保留并如实标 status="banned"。total 随 q 缩小，
// NoMembershipCount 不随 q 缩小；RoleCounts 沿用 scope=all 四键目录计数。
func (s *userManagementService) ListUsersWithoutMembership(
	ctx context.Context,
	tenantID uint64,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if s.scopedListRepo == nil {
		return nil, apperrors.NewInternalServerError(
			"user management list repository is not configured")
	}

	page, pageSize = clampUserManagementPagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, err := s.scopedListRepo.ListUserManagementUsersWithoutMembership(
		ctx, query, offset, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := s.scopedListRepo.CountUserManagementUsersWithoutMembership(ctx, query)
	if err != nil {
		return nil, err
	}
	counts, err := s.scopedListRepo.CountUserManagementScopedRoleCounts(
		ctx, tenantID, interfaces.UserManagementScopeAll)
	if err != nil {
		return nil, err
	}
	noMembership, err := s.noMembershipBadgeCount(ctx, interfaces.UserManagementScopeAll)
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
			Status:    status,
			IsActive:  row.IsActive,
			CreatedAt: row.CreatedAt,
			// 无空间账号没有任何成员关系：memberships 必须是空数组而不是
			// nil（前端据此省略「所属空间」列），顶层角色保持空串。
			Memberships: []interfaces.UserManagementMembership{},
		})
	}
	return &interfaces.UserManagementListResult{
		Items:             items,
		Total:             total,
		RoleCounts:        counts,
		NoMembershipCount: noMembership,
	}, nil
}
