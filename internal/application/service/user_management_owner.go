package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SetOwnerManagementDeps 注入 owner 空间 / co-owner / 交接所需的跨域服务。
// 保留 NewUserManagementService 的三参构造，避免改动 T02/T03 的容器接线
// 与测试；容器与 T04 测试通过该 setter 补齐依赖。
func (s *userManagementService) SetOwnerManagementDeps(
	tenantService interfaces.TenantService,
	memberService interfaces.TenantMemberService,
	invitationService interfaces.TenantInvitationService,
	userService interfaces.UserService,
) {
	s.tenantService = tenantService
	s.memberService = memberService
	s.invitationService = invitationService
	s.userService = userService
}

// requireSystemAdmin 是超管专属操作（设 owner / 生成 owner 邀请）的服务层
// 守卫。路由层已有 SystemAdmin()，这里再证一次，避免服务被其他入口复用。
func requireSystemAdmin(ctx context.Context) error {
	if types.IsSystemAdminFromContext(ctx) {
		return nil
	}
	return apperrors.NewForbiddenError("system administrator privileges are required")
}

// requireOwnerOrSystemAdmin 要求调用者是系统管理员，或本空间 active owner。
// 不能只信 context 里的角色：测试与生产都可能出现"自称 owner 但不是成员"
// 的调用者，必须回查成员关系。
func (s *userManagementService) requireOwnerOrSystemAdmin(
	ctx context.Context,
	tenantID uint64,
) error {
	if types.IsSystemAdminFromContext(ctx) {
		return nil
	}
	if types.TenantRoleFromContext(ctx) != types.TenantRoleOwner {
		return apperrors.NewForbiddenError("only workspace owners can manage owners")
	}
	actorID, _ := types.UserIDFromContext(ctx)
	if actorID == "" {
		return apperrors.NewForbiddenError("only workspace owners can manage owners")
	}
	member, err := s.memberRepo.Get(ctx, actorID, tenantID)
	if err != nil {
		return err
	}
	if member == nil || member.Status != types.TenantMemberStatusActive ||
		member.Role != types.TenantRoleOwner {
		return apperrors.NewForbiddenError("only workspace owners can manage owners")
	}
	return nil
}

// activeOwnerMembership 返回目标在任意空间持有的 active owner 成员关系；
// 没有则返回 (nil, nil)。用于 ProvisionOwnerSpace 的幂等复用。
func (s *userManagementService) activeOwnerMembership(
	ctx context.Context,
	userID string,
) (*types.TenantMember, error) {
	members, err := s.memberRepo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		if member == nil || member.Status != types.TenantMemberStatusActive {
			continue
		}
		if member.Role == types.TenantRoleOwner {
			return member, nil
		}
	}
	return nil, nil
}

// loadActiveMembership 要求 (userID, tenantID) 是 active 成员，否则 404。
func (s *userManagementService) loadActiveMembership(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (*types.TenantMember, error) {
	member, err := s.memberRepo.Get(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.Status != types.TenantMemberStatusActive {
		return nil, apperrors.NewNotFoundError("membership not found")
	}
	return member, nil
}

// defaultOwnerSpaceName 生成非空的空间名：优先用调用方提供的名字，
// 否则用 "<username>的空间"，保证 tenants.name 非空约束。
func defaultOwnerSpaceName(spaceName, username string) string {
	name := strings.TrimSpace(spaceName)
	if name != "" {
		return name
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return "独立空间"
	}
	return fmt.Sprintf("%s的空间", username)
}

// adoptHomeTenant 把无家空间账号（users.tenant_id=0）指向刚确认的空间。
// 已有家空间的账号保持原值，避免悄悄改写别的空间归属。
func (s *userManagementService) adoptHomeTenant(
	ctx context.Context,
	user *types.User,
	tenantID uint64,
) error {
	if user == nil || user.TenantID != 0 || tenantID == 0 {
		return nil
	}
	user.TenantID = tenantID
	user.UpdatedAt = time.Now()
	// 家空间是登录/空间切换的归属依据，写失败必须让调用方看到，
	// 否则账号会继续被当成无空间用户。
	return s.userRepo.UpdateUser(ctx, user)
}

// emitOwnerAudit 写 owner 管理相关审计。审计是尽力而为：写失败不影响
// 已经完成的空间/成员变更（与既有 emitAudit 语义一致）。
func (s *userManagementService) emitOwnerAudit(
	ctx context.Context,
	tenantID uint64,
	action types.AuditAction,
	targetUserID string,
	details map[string]string,
) {
	if s.audit == nil {
		return
	}
	payload := types.JSON(nil)
	if len(details) > 0 {
		if encoded, err := json.Marshal(details); err == nil {
			payload = types.JSON(encoded)
		}
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:     tenantID,
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       action,
		TargetType:   "tenant_member",
		TargetUserID: targetUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      payload,
	})
}

// ownerDepsReady 检查 T04 依赖是否已注入；缺失属于接线错误，直接报内部
// 错误，避免静默跳过建租户/建成员等关键步骤。
func (s *userManagementService) ownerDepsReady() error {
	if s.tenantService == nil || s.memberService == nil ||
		s.invitationService == nil || s.userService == nil {
		return apperrors.NewInternalServerError("owner management dependencies are not configured")
	}
	return nil
}

// mapUserIdentityError 把注册时的重复身份错误映射成 409。
func mapUserIdentityError(err error) error {
	switch {
	case errors.Is(err, ErrUserEmailExists):
		return apperrors.NewConflictError(
			"this email is already registered; invite or add the existing user instead")
	case errors.Is(err, ErrUserUsernameExists):
		return apperrors.NewConflictError("this username is already taken")
	case errors.Is(err, ErrUserIdentityConflict):
		return apperrors.NewConflictError("this email or username is already taken")
	default:
		return err
	}
}

// mapMembershipError 把入组冲突映射成 409；目标账号已在本空间时同样
// 属于身份冲突，交由上层决定是否提示邀请。
func mapMembershipError(err error) error {
	switch {
	case errors.Is(err, ErrMembershipAlreadyExists):
		return apperrors.NewConflictError("this user is already a member of the workspace")
	default:
		return err
	}
}

// ensureTargetUser 读取目标用户，不存在时返回 404。
func (s *userManagementService) ensureTargetUser(
	ctx context.Context,
	targetUserID string,
) (*types.User, error) {
	user, err := s.userRepo.GetUserByID(ctx, targetUserID)
	if err != nil {
		if errors.Is(err, apprepo.ErrUserNotFound) {
			return nil, apperrors.NewNotFoundError("user not found")
		}
		return nil, err
	}
	return user, nil
}
