package service

import (
	"context"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ProvisionOwnerSpace 由系统管理员把已有账号设为 owner。
//
// 幂等规则（AC-11）：目标已经在任意空间持有 active owner 角色时复用该
// 空间，不重复建租户/成员；否则走 tenantService.CreateTenant +
// memberService.EnsureOwner，保证目标成为新空间唯一 owner，而调用者
// （系统管理员）不会顺带成为成员。
func (s *userManagementService) ProvisionOwnerSpace(
	ctx context.Context,
	targetUserID, spaceName string,
) (*types.Tenant, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.ownerDepsReady(); err != nil {
		return nil, err
	}
	targetUserID = strings.TrimSpace(targetUserID)
	if targetUserID == "" {
		return nil, apperrors.NewValidationError("target user id is required")
	}
	target, err := s.ensureTargetUser(ctx, targetUserID)
	if err != nil {
		return nil, err
	}

	existing, err := s.activeOwnerMembership(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		tenant, err := s.tenantService.GetTenantByID(ctx, existing.TenantID)
		if err != nil {
			return nil, err
		}
		if err := s.adoptHomeTenant(ctx, target, tenant.ID); err != nil {
			return nil, err
		}
		s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
			targetUserID, map[string]string{"space_name": tenant.Name, "reused": "true"})
		return tenant, nil
	}

	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{
		Name: defaultOwnerSpaceName(spaceName, target.Username),
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.memberService.EnsureOwner(ctx, targetUserID, tenant.ID); err != nil {
		// 空间刚建、还没有任何成员，删除不会留下悬挂引用。
		_ = s.tenantService.DeleteTenant(ctx, tenant.ID)
		return nil, err
	}
	if err := s.adoptHomeTenant(ctx, target, tenant.ID); err != nil {
		return nil, err
	}
	s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
		targetUserID, map[string]string{"space_name": tenant.Name})
	return tenant, nil
}

// compile-time 断言：T04 依赖注入契约需要具体实现暴露该 setter。
var _ interface {
	SetOwnerManagementDeps(
		interfaces.TenantService,
		interfaces.TenantMemberService,
		interfaces.TenantInvitationService,
		interfaces.UserService,
	)
} = (*userManagementService)(nil)
