package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// 本文件实现 T35 的平台级账号操作（ban / unban / purge）。
//
// 与租户级方法的根本区别是「不依赖成员关系」：平台方法按 users.id 直接
// 定位账号，调用者当前工作区与目标可以毫无关系（多空间 / 全平台视图）。
// 权限由系统管理员身份决定，审计写 tenant_id=0 的平台作用域，因此不会
// 出现在任何空间的操作记录里。

// requirePlatformAdmin 是平台方法的统一权限闸门。路由组已挂
// g.SystemAdmin()，这里在服务层再证一次（C4：隐藏入口不等于权限隔离）。
func requirePlatformAdmin(ctx context.Context) error {
	if !types.IsSystemAdminFromContext(ctx) {
		return apperrors.NewForbiddenError(
			"only system administrators can manage platform accounts")
	}
	return nil
}

// loadPlatformTarget 按 users.id 直接定位账号，不要求调用者与目标存在
// 成员关系。用户行缺失（含已墓碑）一律 404。
func (s *userManagementService) loadPlatformTarget(
	ctx context.Context, targetUserID string,
) (*types.User, error) {
	if targetUserID == "" {
		return nil, apperrors.NewValidationError("target user id is required")
	}
	user, err := s.userRepo.GetUserByID(ctx, targetUserID)
	if err != nil {
		if errors.Is(err, apprepo.ErrUserNotFound) {
			return nil, apperrors.NewNotFoundError("user not found")
		}
		return nil, err
	}
	return user, nil
}

// PlatformBanUser 账号级封禁：is_active=false + 吊销全部 token，成员关系
// 保持不动。自己 / 最后活跃系统管理员保护复用仓储 SetUserActive。
func (s *userManagementService) PlatformBanUser(
	ctx context.Context,
	targetUserID string,
	reason string,
) (*types.User, error) {
	if err := requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	target, err := s.loadPlatformTarget(ctx, targetUserID)
	if err != nil {
		return nil, err
	}

	wasActive := target.IsActive
	user, err := s.userRepo.SetUserActive(ctx, target.ID, auditActor(ctx), false)
	if err != nil {
		return nil, mapUserStatusError(err)
	}
	// 只有真正发生 active→inactive 才写审计，重复 ban 保持幂等，也不会
	// 反复改写「全平台最近一次 ban」的归属。
	if wasActive {
		s.emitPlatformAudit(ctx, target.ID, types.AuditActionMemberBanned, reason)
	}
	return user, nil
}

// PlatformUnbanUser 账号级解封：只恢复 is_active，不恢复历史 token。
// 平台接口不受「最近一次 ban 必须在本租户」限制，因此无需查 latestBan。
func (s *userManagementService) PlatformUnbanUser(
	ctx context.Context,
	targetUserID string,
	reason string,
) (*types.User, error) {
	if err := requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	target, err := s.loadPlatformTarget(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.SetUserActive(ctx, target.ID, auditActor(ctx), true)
	if err != nil {
		return nil, mapUserStatusError(err)
	}
	s.emitPlatformAudit(ctx, target.ID, types.AuditActionMemberUnbanned, reason)
	return user, nil
}

// PlatformPurgeUser 账号级注销（墓碑化）。跨空间 / 无成员关系账号允许，
// 但目标保护不放松：
//   - 确认邮箱必须匹配（400）；
//   - 不能删除自己（403）；
//   - 系统管理员 / 跨空间超管需先撤权（403）；
//   - 目标在任意空间持有 active owner 时需先交接（403）。
//
// 墓碑化事务传 tenantID=0 + callerIsSystemAdmin=true：平台视角不存在
// 「本租户」，跨空间归属检查由系统管理员身份豁免。
func (s *userManagementService) PlatformPurgeUser(
	ctx context.Context,
	targetUserID string,
	confirmEmail string,
	reason string,
) error {
	if err := requirePlatformAdmin(ctx); err != nil {
		return err
	}
	target, err := s.loadPlatformTarget(ctx, targetUserID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(confirmEmail), strings.TrimSpace(target.Email)) {
		return apperrors.NewValidationError(
			"confirm email does not match the target account")
	}
	actorID, _ := types.UserIDFromContext(ctx)
	if actorID != "" && actorID == target.ID {
		return apperrors.NewForbiddenError("you cannot purge your own account")
	}
	if target.IsSystemAdmin || target.CanAccessAllTenants {
		return apperrors.NewForbiddenError(
			"revoke system administrator / cross-workspace privileges before purging this account")
	}
	holdsOwner, err := s.holdsOwnerRole(ctx, target.ID)
	if err != nil {
		return err
	}
	if holdsOwner {
		return apperrors.NewForbiddenError(
			"hand over the workspace before purging an owner account")
	}
	if s.purgeRepo == nil {
		return apperrors.NewInternalServerError("user purge is not available")
	}
	audit := platformAuditEntry(ctx, target.ID, types.AuditActionMemberPurged, reason)
	if err := s.purgeRepo.PurgeUserTx(ctx, 0, target.ID, true, audit); err != nil {
		return mapPurgeError(err)
	}
	return nil
}

// emitPlatformAudit 写平台作用域审计。与既有账号状态审计一样是尽力而为：
// 审计写失败不影响已经完成的业务操作。
func (s *userManagementService) emitPlatformAudit(
	ctx context.Context,
	targetUserID string,
	action types.AuditAction,
	reason string,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, platformAuditEntry(ctx, targetUserID, action, reason))
}

// platformAuditEntry 组装 tenant_id=0 的审计行，供 /system/admin/audit-log
// 读取。TargetType 用 "user"（平台账号），details 只放原因，严禁邮箱、
// 密码哈希、token 等敏感数据；ScopeID 指向目标账号便于按目标筛选。
func platformAuditEntry(
	ctx context.Context,
	targetUserID string,
	action types.AuditAction,
	reason string,
) *types.AuditLog {
	details, _ := json.Marshal(map[string]string{"reason": reason})
	return &types.AuditLog{
		TenantID:     0, // 平台 / 系统作用域
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       action,
		ScopeID:      targetUserID,
		TargetType:   "user",
		TargetUserID: targetUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      types.JSON(details),
	}
}
