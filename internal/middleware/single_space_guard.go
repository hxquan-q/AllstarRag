package middleware

import (
	"context"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T04 一号一空间：认证中间件的孤儿空间自愈必须服从「非超管同一时刻只
// 属于一个空间」。自愈逻辑放在独立文件，避免继续膨胀既有 auth.go。

// userBelongsToOtherSpace 判断用户是否已在 targetTenantID 之外的空间拥有
// active/invited 成员关系。suspended/deleted 不计（AC-18）。
func userBelongsToOtherSpace(
	ctx context.Context,
	memberService interfaces.TenantMemberService,
	userID string,
	tenantID uint64,
) (bool, error) {
	members, err := memberService.ListByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member == nil || member.DeletedAt.Valid || member.TenantID == tenantID {
			continue
		}
		if member.Status == types.TenantMemberStatusActive ||
			member.Status == types.TenantMemberStatusInvited {
			return true, nil
		}
	}
	return false, nil
}

// tenantHasAnyActiveMember 判断空间是否还有任何 active 成员（不限角色）。
// 自愈只服务「严格孤儿」空间：只要还有成员，缺 owner 属于治理异常，
// 由超管处理，不能把任意一个带着旧 home 指针的人补成 owner（N22）。
// 查询失败返回错误，由调用方 fail-closed。
func tenantHasAnyActiveMember(
	ctx context.Context,
	memberService interfaces.TenantMemberService,
	tenantID uint64,
) (bool, error) {
	members, err := memberService.ListByTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member != nil && member.Status == types.TenantMemberStatusActive {
			return true, nil
		}
	}
	return false, nil
}

// tryHealOrphanTenant 执行孤儿空间自愈。只有「严格孤儿」空间才补 owner：
// 空间没有任何 active 成员、也没有 active owner。已有 active owner、
// 仍有其他 active 成员、用户已属于其他空间、或查询失败时都不自愈，
// 由调用方按 EnableRBAC fail-closed。
func tryHealOrphanTenant(
	ctx context.Context,
	memberService interfaces.TenantMemberService,
	user *types.User,
	tenantID uint64,
) (types.TenantRole, bool) {
	hasActiveOwner, err := tenantHasActiveOwner(ctx, memberService, tenantID)
	if err != nil {
		logger.Warnf(ctx, "tenant owner check failed tenant=%d: %v", tenantID, err)
		return "", false
	}
	if hasActiveOwner {
		return "", false
	}
	hasMember, err := tenantHasAnyActiveMember(ctx, memberService, tenantID)
	if err != nil {
		logger.Warnf(ctx, "tenant member check failed tenant=%d: %v", tenantID, err)
		return "", false
	}
	if hasMember {
		logger.Warnf(ctx,
			"skip orphan-tenant self-heal: tenant=%d still has active members but no active owner",
			tenantID)
		return "", false
	}
	// 一号一空间（T04）：非超管已属于其他空间时不得再被补成第二个空间
	// 的 owner；超管豁免。
	if !user.IsSystemAdmin && !user.CanAccessAllTenants {
		belongs, belongErr := userBelongsToOtherSpace(ctx, memberService, user.ID, tenantID)
		if belongErr != nil {
			logger.Warnf(ctx, "membership check failed user=%s tenant=%d: %v",
				user.ID, tenantID, belongErr)
			return "", false
		}
		if belongs {
			logger.Warnf(ctx,
				"skip orphan-tenant self-heal: user=%s already belongs to another space (target=%d)",
				user.ID, tenantID)
			return "", false
		}
	}
	if _, err := memberService.AddMember(
		ctx, user.ID, tenantID, types.TenantRoleOwner, nil,
	); err != nil {
		logger.Warnf(ctx, "Failed to auto-promote user %s in tenant %d: %v",
			user.ID, tenantID, err)
		return "", false
	}
	logger.Infof(ctx,
		"[audit] Auto-promoted user %s to Owner of orphan tenant %d (home_tenant=true)",
		user.ID, tenantID,
	)
	return types.TenantRoleOwner, true
}
