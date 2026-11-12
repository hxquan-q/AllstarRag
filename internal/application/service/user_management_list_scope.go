package service

import (
	"context"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// userManagementScopedListRepo 是多空间视图（scope=other/all）列表需要的
// 窄仓储接口。与单空间 listRepo 同理：不扩展 interfaces 宽接口，真实
// *tenantMemberRepository 在构造时自动探测。
type userManagementScopedListRepo interface {
	ListUserManagementScopedPaged(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
		role types.TenantRole, search string, offset, limit int,
	) ([]apprepo.UserManagementScopedRow, error)
	CountUserManagementScoped(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
		role types.TenantRole, search string,
	) (int64, error)
	CountUserManagementScopedRoleCounts(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
	) (map[types.TenantRole]int64, error)
	ListUserManagementMemberships(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
		userIDs []string,
	) ([]apprepo.UserManagementMembershipRow, error)
	// 多角色变体（T22）：多空间视图下按角色集合过滤。
	ListUserManagementScopedPagedWithRoles(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
		roles []types.TenantRole, search string, offset, limit int,
	) ([]apprepo.UserManagementScopedRow, error)
	CountUserManagementScopedWithRoles(
		ctx context.Context, tenantID uint64, scope interfaces.UserManagementListScope,
		roles []types.TenantRole, search string,
	) (int64, error)
	// 「无空间」目录（T44）：无任何 active 成员关系的普通账号。
	ListUserManagementUsersWithoutMembership(
		ctx context.Context, search string, offset, limit int,
	) ([]apprepo.UserManagementScopedRow, error)
	CountUserManagementUsersWithoutMembership(
		ctx context.Context, search string,
	) (int64, error)
}

// ListUsersScoped 返回带空间筛选的用户管理列表。
//
// scope 校验先于判权（非法 scope 即使无权也回 400，口径与 handler 一致）：
//   - scope=current：与旧 ListUsers 完全一致（等价分派，零破坏）；
//   - scope=other/all：系统管理员专属（C4 权限在服务层自证，非系统管理
//     管员一律 403，不依赖路由守卫）。
//
// 多空间口径：
//   - 用户按 user_id 去重（one user, one row），搜索与分页在去重后进行；
//   - memberships 按 tenant_id 升序；other 只含其它空间，all 含本空间；
//   - 顶层 Role 取视图内第一条成员关系的角色（展示口径，无关系为空）；
//   - role_counts 按视图内"有该角色的去重用户数"计，不随过滤缩小。
func (s *userManagementService) ListUsersScoped(
	ctx context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	role types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	switch scope {
	case interfaces.UserManagementScopeCurrent:
		return s.ListUsers(ctx, tenantID, role, query, page, pageSize)
	case interfaces.UserManagementScopeOther, interfaces.UserManagementScopeAll:
	default:
		return nil, apperrors.NewValidationError(
			"scope must be one of current, other, all")
	}
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if role != "" && !role.IsValid() {
		return nil, apperrors.NewValidationError(
			"role must be one of viewer, contributor, admin, owner")
	}
	if s.scopedListRepo == nil {
		return nil, apperrors.NewInternalServerError(
			"user management list repository is not configured")
	}

	page, pageSize = clampUserManagementPagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, err := s.scopedListRepo.ListUserManagementScopedPaged(
		ctx, tenantID, scope, role, query, offset, pageSize)
	if err != nil {
		return nil, err
	}
	total, err := s.scopedListRepo.CountUserManagementScoped(
		ctx, tenantID, scope, role, query)
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
		// 顶层角色取视图内第一条成员关系（memberships 已按 tenant_id
		// 升序）；无成员关系的账号（all 视图）保持空角色。
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
