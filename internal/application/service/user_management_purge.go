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

// userPurgeTx 是仓储层的墓碑化事务契约。具体实现放在
// repository/user_purge.go，通过构造时自动探测注入，避免扩大
// interfaces.UserRepository 的公共契约。
type userPurgeTx interface {
	// audit 与墓碑化在同一条事务里落库：任一步失败都整体回滚，
	// 不会出现"账号已删但没有审计"的状态。
	// tenantID / callerIsSystemAdmin 用于事务内复核跨空间归属（AC-19b）：
	// 非系统管理员不能墓碑化仍属于其他空间的账号。
	PurgeUserTx(
		ctx context.Context,
		tenantID uint64,
		targetUserID string,
		callerIsSystemAdmin bool,
		audit *types.AuditLog,
	) error
}

// PurgeUser 彻底删除（墓碑化）目标账号。
//
// 顺序：目标必须在本租户且仍 active → 邮箱二次确认 → 目标保护 →
// 单事务墓碑化与个人数据清理 → 写审计。确认邮箱不匹配时直接 400，
// 不产生任何副作用；重复 purge 因成员关系已软删而返回 404。
func (s *userManagementService) PurgeUser(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	confirmEmail string,
	reason string,
) error {
	target, err := s.loadTarget(ctx, tenantID, targetUserID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(confirmEmail), strings.TrimSpace(target.user.Email)) {
		return apperrors.NewValidationError(
			"confirm email does not match the target account")
	}
	if err := s.authorizePurge(ctx, target); err != nil {
		return err
	}
	if s.purgeRepo == nil {
		return apperrors.NewInternalServerError("user purge is not available")
	}
	audit := purgeAuditEntry(ctx, tenantID, target, reason)
	if err := s.purgeRepo.PurgeUserTx(
		ctx, tenantID, target.user.ID, types.IsSystemAdminFromContext(ctx), audit,
	); err != nil {
		return mapPurgeError(err)
	}
	return nil
}

// purgeAuditEntry 组装 rbac.member_purged 审计行。details 只放角色与
// 原因；邮箱、密码哈希、token 等一律不进审计。
func purgeAuditEntry(
	ctx context.Context,
	tenantID uint64,
	target *managedTarget,
	reason string,
) *types.AuditLog {
	details, _ := json.Marshal(map[string]string{
		"target_role": string(target.member.Role),
		"reason":      reason,
	})
	return &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: auditActor(ctx),
		ActorRole:   auditActorRole(ctx),
		Action:      types.AuditActionMemberPurged,
		// 与空间操作记录读侧的 scope_type='' 口径一致（F18）。
		ScopeID:      target.user.ID,
		TargetType:   "tenant_member",
		TargetUserID: target.user.ID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      types.JSON(details),
	}
}

// authorizePurge 是比封禁更严的目标保护：
//   - 任何人都不能删除自己；
//   - 系统管理员 / 跨空间超管必须先撤销特权；
//   - 调用者必须是系统管理员、本空间 owner 或普通管理员；
//   - 普通管理员不能删 owner 或同级管理员；
//   - 目标不得在任意空间持有 active owner（owner 必须先交接空间，
//     系统管理员也不例外）。
//
// 角色/目标保护先于墓碑化事务；被拒绝的请求不会写成功审计。
func (s *userManagementService) authorizePurge(
	ctx context.Context,
	target *managedTarget,
) error {
	actorID, _ := types.UserIDFromContext(ctx)
	if actorID != "" && actorID == target.user.ID {
		return apperrors.NewForbiddenError("you cannot purge your own account")
	}
	if target.user.IsSystemAdmin || target.user.CanAccessAllTenants {
		return apperrors.NewForbiddenError(
			"revoke system administrator / cross-workspace privileges before purging this account")
	}
	if !types.IsSystemAdminFromContext(ctx) {
		callerRole := types.TenantRoleFromContext(ctx)
		if callerRole != types.TenantRoleOwner && callerRole != types.TenantRoleAdmin {
			return apperrors.NewForbiddenError(
				"only workspace owners or administrators can purge members")
		}
		if err := memberTargetProtection(ctx, target.user.ID, target.member.Role, false); err != nil {
			return err
		}
		// AC-19b：彻底删除是账号级的，会连带清掉目标在其他空间的数据。
		// 非系统管理员不能跨空间删除，必须由超管处理。
		crossSpace, err := s.hasMembershipOutsideTenant(
			ctx, target.user.ID, target.member.TenantID)
		if err != nil {
			return err
		}
		if crossSpace {
			return apperrors.NewForbiddenError(
				"this account also belongs to other workspaces; ask a system administrator to purge it")
		}
	}
	holdsOwner, err := s.holdsOwnerRole(ctx, target.user.ID)
	if err != nil {
		return err
	}
	if holdsOwner {
		return apperrors.NewForbiddenError(
			"hand over the workspace before purging an owner account")
	}
	return nil
}

// hasMembershipOutsideTenant 检查目标在本租户之外是否还有 active 成员关系。
// 查询报错时返回错误（失败关闭），调用方不会继续墓碑化。
func (s *userManagementService) hasMembershipOutsideTenant(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (bool, error) {
	members, err := s.memberRepo.ListByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member.Status == types.TenantMemberStatusActive && member.TenantID != tenantID {
			return true, nil
		}
	}
	return false, nil
}

// mapPurgeError 把仓储事务的错误翻译成稳定 HTTP 语义。目标在执行期间
// 已被另一次 purge 墓碑化时按 404 处理（与幂等口径一致）。
func mapPurgeError(err error) error {
	switch {
	case errors.Is(err, apprepo.ErrUserNotFound):
		return apperrors.NewNotFoundError("membership not found")
	case errors.Is(err, apprepo.ErrPurgeTargetStillOwner):
		return apperrors.NewForbiddenError(
			"hand over the workspace before purging an owner account")
	case errors.Is(err, apprepo.ErrPurgeTargetPrivileged):
		return apperrors.NewForbiddenError(
			"revoke system administrator / cross-workspace privileges before purging this account")
	case errors.Is(err, apprepo.ErrPurgeTargetCrossTenant):
		return apperrors.NewForbiddenError(
			"this account also belongs to other workspaces; ask a system administrator to purge it")
	default:
		return err
	}
}
