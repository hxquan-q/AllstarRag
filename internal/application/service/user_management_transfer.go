package service

import (
	"context"
	"errors"
	"strings"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// TransferOwner 交接空间 owner 身份。
//
// 语义（02-plan §4.5 / T04-4）：
//   - 普通 owner 只能传自己；fromUserID 为空时按调用者处理；
//   - 系统管理员可传任意 owner，且不受工作区角色限制；
//   - 目标必须是本空间 active 成员，否则 404；
//   - 顺序是先提目标为 owner、再降源为 admin，任何时刻都至少保留
//     1 名 owner；降级用 UpdateRole 的最后 owner 保护兜底。
func (s *userManagementService) TransferOwner(
	ctx context.Context,
	tenantID uint64,
	fromUserID, toUserID string,
) error {
	callerID, _ := types.UserIDFromContext(ctx)
	from := strings.TrimSpace(fromUserID)
	if from == "" {
		from = callerID
	}
	to := strings.TrimSpace(toUserID)
	if to == "" {
		return apperrors.NewValidationError("to_user_id is required")
	}
	if from == "" {
		return apperrors.NewForbiddenError("only workspace owners can transfer ownership")
	}
	if from == to {
		return apperrors.NewValidationError("cannot transfer ownership to the same user")
	}
	if err := s.requireOwnerOrSystemAdmin(ctx, tenantID); err != nil {
		return err
	}
	if !types.IsSystemAdminFromContext(ctx) && from != callerID {
		return apperrors.NewForbiddenError("owners can only transfer their own ownership")
	}
	if s.memberService == nil {
		return apperrors.NewInternalServerError("owner management dependencies are not configured")
	}

	source, err := s.loadActiveMembership(ctx, from, tenantID)
	if err != nil {
		return err
	}
	if source.Role != types.TenantRoleOwner {
		return apperrors.NewNotFoundError("source owner membership not found")
	}
	target, err := s.loadActiveMembership(ctx, to, tenantID)
	if err != nil {
		return err
	}
	// 不能把空间交给被封禁的账号，否则交接后空间会立刻没有可登录的 owner（F30）。
	if targetUser, err := s.userRepo.GetUserByID(ctx, to); err != nil {
		if errors.Is(err, apprepo.ErrUserNotFound) {
			return apperrors.NewNotFoundError("target user not found")
		}
		return apperrors.NewInternalServerError("failed to load target user")
	} else if targetUser == nil || !targetUser.IsActive {
		return apperrors.NewBadRequestError("cannot transfer ownership to a banned account")
	}

	previousTargetRole := target.Role
	if previousTargetRole != types.TenantRoleOwner {
		if err := s.updateRoleGuarded(ctx, to, tenantID, previousTargetRole, types.TenantRoleOwner, false); err != nil {
			return err
		}
	}
	if err := s.updateRoleGuarded(ctx, from, tenantID, types.TenantRoleOwner, types.TenantRoleAdmin, true); err != nil {
		// 降级失败时把目标恢复原角色，避免留下"交接了一半"的状态。
		if previousTargetRole != types.TenantRoleOwner {
			_ = s.updateRoleGuarded(ctx, to, tenantID, types.TenantRoleOwner, previousTargetRole, true)
		}
		return err
	}

	s.emitOwnerAudit(ctx, tenantID, types.AuditActionOwnerTransferred, to, map[string]string{
		"from_user_id": from,
		"to_user_id":   to,
	})
	return nil
}

// updateRoleGuarded 在成员服务支持时走带 CAS 的角色更新；不支持该能力的
// 测试替身回退到旧的 UpdateRole 路径。
func (s *userManagementService) updateRoleGuarded(
	ctx context.Context,
	userID string,
	tenantID uint64,
	expectedRole, newRole types.TenantRole,
	allowOwnerDemotion bool,
) error {
	if guarded, ok := s.memberService.(interface {
		UpdateRoleGuarded(
			context.Context, string, uint64, types.TenantRole, types.TenantRole, bool,
		) error
	}); ok {
		return guarded.UpdateRoleGuarded(ctx, userID, tenantID, expectedRole, newRole, allowOwnerDemotion)
	}
	return s.memberService.UpdateRole(ctx, userID, tenantID, newRole)
}
