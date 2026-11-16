package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// memberTargetProtection 是 T06 的目标保护矩阵（02-plan §2.2）：
//
//	调用者 \ 目标    owner                  普通管理员(admin)    游客(viewer)
//	owner            403（只能交接）        允许                 允许
//	普通管理员       403                    403（同级保护）      允许
//	系统管理员       保持现状（由入口额外约束决定）
//
// 上下文未显式注入调用者角色表示服务内部或旧单测路径（HTTP 入口由认证
// 中间件保证角色非空），此时不做调用者角色判定，保持既有内部调用兼容。
// 注意不能用 TenantRoleFromContext 判空：它在键缺失时会兜底为 viewer。
//
// self 始终拒绝（allowSelf=false）；LeaveTenant 等离职路径不由本函数判定。
func memberTargetProtection(
	ctx context.Context,
	targetUserID string,
	targetRole types.TenantRole,
	allowSelf bool,
) error {
	// API Key 是机器主体，权限由路由层 APIKeyGate 按 manage_members 能力
	// 判定（与 RequireRole 的短路口径一致），不参与 JWT 角色阶梯；handler
	// 的 ValidateRoleChangeTarget 仍会拦截特权目标。
	if _, ok := types.TenantAPIKeyScopeFromContext(ctx); ok {
		return nil
	}
	// AC-9：任何人（含系统管理员）都不能对自己降级/封禁/注销。self 检查
	// 必须先于超管豁免，否则超管以普通成员身份可以改自己的空间角色。
	actorID, _ := types.UserIDFromContext(ctx)
	if actorID != "" && actorID == targetUserID && !allowSelf {
		return apperrors.NewForbiddenError("you cannot change your own account status")
	}
	if types.IsSystemAdminFromContext(ctx) {
		return nil
	}
	callerRole, ok := explicitCallerRole(ctx)
	if !ok {
		return nil
	}
	switch callerRole {
	case types.TenantRoleOwner:
		if targetRole == types.TenantRoleOwner {
			return apperrors.NewForbiddenError(
				"owner accounts can only be managed by a system administrator")
		}
		return nil
	case types.TenantRoleAdmin:
		if targetRole == types.TenantRoleOwner {
			return apperrors.NewForbiddenError(
				"owner accounts can only be managed by a system administrator")
		}
		if targetRole == types.TenantRoleAdmin {
			return apperrors.NewForbiddenError(
				"administrators cannot manage another administrator")
		}
		return nil
	default:
		return apperrors.NewForbiddenError(
			"only workspace owners or administrators can manage members")
	}
}

// explicitCallerRole 只读取调用方显式注入的工作区角色。TenantRoleFromContext
// 在键缺失时会兜底 viewer，无法区分「未注入」与「真的是游客」，因此目标
// 保护用它判断是否需要做调用者角色判定。
func explicitCallerRole(ctx context.Context) (types.TenantRole, bool) {
	if role, ok := ctx.Value(types.TenantRoleContextKey).(types.TenantRole); ok && role.IsValid() {
		return role, true
	}
	if caller, ok := ctx.Value(types.CallerContextKey).(types.Caller); ok && caller.Role.IsValid() {
		return caller.Role, true
	}
	return "", false
}
