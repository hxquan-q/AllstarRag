package service

import "github.com/Tencent/WeKnora/internal/types"

// IsAssignableMemberRole 判断角色能否通过邀请 / 建号 / 改角色分配。
//
// 唯一 owner 模型（T05 / AC-11）：产品层只分配 admin / viewer；owner 只能
// 由 ProvisionOwnerSpace 或 transfer-owner 交接产生；contributor 枚举保留
// 但不再分配（C10，避免上游同步冲突）。
func IsAssignableMemberRole(role types.TenantRole) bool {
	return role == types.TenantRoleAdmin || role == types.TenantRoleViewer
}

// CanCreateRole 判断操作者能否创建指定租户角色的成员账号。
//
// 规则来自 01-spec AC-5 / AC-11 与 02-plan §2.3：
//   - owner / admin（普通管理员）：可创建 admin / viewer；
//   - contributor / viewer：不能创建任何账号；
//   - 任何人都不能创建 owner / contributor。
//
// 这里只做纯角色判定，不读 context：系统管理员按 owner 处理由调用方
// （handler）在传入前完成，便于单测直接覆盖矩阵本身。
func CanCreateRole(actor, target types.TenantRole) bool {
	if !IsAssignableMemberRole(target) {
		return false
	}
	switch actor {
	case types.TenantRoleOwner, types.TenantRoleAdmin:
		return true
	default:
		return false
	}
}

// CanResetRole 判断操作者能否重置指定角色成员的密码。
//
// 规则来自 01-spec AC-5 / AC-11 与 02-plan §2.3：
//   - owner：可重置 admin / viewer；
//   - admin：只能重置 viewer（同级保护：不能重置另一位普通管理员）；
//   - contributor / viewer：不能重置任何人；
//   - 任何人不能重置 owner / contributor。
//
// 注意：owner 之间也不能互相重置密码（同级不允许），重置本人由调用方
// 单独判定并返回 400，交给「修改密码」接口。
//
// 与 CanCreateRole 一样只做纯角色判定，不读 context：系统管理员按 owner
// 处理由 handler 在传入前完成。
func CanResetRole(actor, target types.TenantRole) bool {
	if !IsAssignableMemberRole(target) {
		return false
	}
	switch actor {
	case types.TenantRoleOwner:
		return true
	case types.TenantRoleAdmin:
		return target == types.TenantRoleViewer
	default:
		return false
	}
}
