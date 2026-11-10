package service

import (
	"context"
	"errors"
	"strings"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// TransferOwner 交接空间 owner 身份。
//
// 语义（02-plan §4.5 / T04-4）：
//   - 普通 owner 只能传自己；fromUserID 为空时按调用者处理；
//   - 系统管理员可传任意 owner，且不受工作区角色限制；
//   - 目标必须是本空间 active 成员，否则 404；
//   - 提升目标、降级源在仓储的单个事务内完成；失败整体回滚，不做
//     跨事务补偿（T40），因此不会撤销其他已成功的交接。
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

	source, err := s.loadActiveMembership(ctx, from, tenantID)
	if err != nil {
		return err
	}
	if source.Role != types.TenantRoleOwner {
		return apperrors.NewNotFoundError("source owner membership not found")
	}
	targetMember, err := s.loadActiveMembership(ctx, to, tenantID)
	if err != nil {
		return err
	}
	// 唯一 owner 模型（AC-7）：交接目标必须是本空间 active 普通管理员
	// （admin）；viewer/contributor 需先提升为 admin，避免把空间直接交给
	// 没有成员管理能力的账号。
	if targetMember.Role != types.TenantRoleAdmin {
		return apperrors.NewBadRequestError(
			"transfer target must be an active admin; promote the target to admin first")
	}
	// 不能把空间交给被封禁的账号，否则交接后空间会立刻没有可登录的 owner（F30）。
	targetUser, err := s.userRepo.GetUserByID(ctx, to)
	if err != nil {
		if errors.Is(err, apprepo.ErrUserNotFound) {
			return apperrors.NewNotFoundError("target user not found")
		}
		return apperrors.NewInternalServerError("failed to load target user")
	}
	if targetUser == nil || !targetUser.IsActive {
		return apperrors.NewBadRequestError("cannot transfer ownership to a banned account")
	}
	// T40 单事务化时丢掉了旧路径的目标保护，这里在进入事务前恢复：
	// 普通 owner 不能把空间交给特权账号或在别处持有 owner 的账号。
	if err := s.guardTransferTarget(ctx, tenantID, targetUser); err != nil {
		return err
	}

	// 角色变更在仓储事务内完成；事务会再次复核角色与封禁状态，
	// 并发交接失败时不会留下半迁移状态。
	if err := s.memberRepo.TransferOwnershipTx(ctx, tenantID, from, to); err != nil {
		return transferOwnershipError(err)
	}

	s.emitOwnerAudit(ctx, tenantID, types.AuditActionOwnerTransferred, to, map[string]string{
		"from_user_id": from,
		"to_user_id":   to,
	})
	return nil
}

// guardTransferTarget 恢复交接的目标保护（T45），语义与改角色路径的
// ValidateRoleChangeTarget 一致：系统管理员调用者不受限；其余调用者
// 不能把空间交给系统管理员/跨空间超管，也不能交给在其他空间持有
// active owner 的账号。查询失败一律失败关闭。
func (s *userManagementService) guardTransferTarget(
	ctx context.Context,
	tenantID uint64,
	target *types.User,
) error {
	if types.IsSystemAdminFromContext(ctx) {
		return nil
	}
	if target.IsSystemAdmin || target.CanAccessAllTenants {
		return ErrTargetPrivileged
	}
	members, err := s.memberRepo.ListByUser(ctx, target.ID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.Role == types.TenantRoleOwner &&
			member.Status == types.TenantMemberStatusActive &&
			member.TenantID != tenantID {
			return ErrTargetOwnsWorkspace
		}
	}
	return nil
}

// transferOwnershipError 把仓储事务的哨兵映射成对外状态码：
// 目标被封禁 → 400；事务内复核发现角色已变化 → 409；目标成员/用户
// 在事务期间消失 → 404；其余错误交给上层按 500 处理。
func transferOwnershipError(err error) error {
	switch {
	case errors.Is(err, apprepo.ErrTargetBanned):
		return apperrors.NewBadRequestError("cannot transfer ownership to a banned account")
	case errors.Is(err, apprepo.ErrRoleChanged):
		return ErrMembershipChanged
	case errors.Is(err, gorm.ErrRecordNotFound):
		return apperrors.NewNotFoundError("target membership not found")
	default:
		return err
	}
}
