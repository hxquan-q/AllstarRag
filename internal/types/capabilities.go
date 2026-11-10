package types

// Capability 是一项用户管理能力的稳定标识。
//
// 本文件是「角色 → 能力」的唯一事实源（AC-12b）：后端逐接口按能力校验，
// 前端只消费 GET /api/v1/auth/capabilities 的投影，不再另写一套 role 判断。
// 能力字符串一旦发布就作为前后端契约，不要随意改名。
type Capability string

const (
	// CapUserView 查看用户管理页与成员列表。
	CapUserView Capability = "user.view"
	// CapUserInvite 邀请新成员（普通管理员 / 游客）。
	CapUserInvite Capability = "user.invite"
	// CapUserCreate 直接创建账号（普通管理员 / 游客）。
	CapUserCreate Capability = "user.create"
	// CapUserRoleChange 在 admin/viewer 之间改角色（contributor 不再分配）。
	CapUserRoleChange Capability = "user.role.change"
	// CapUserBan 封禁账号。
	CapUserBan Capability = "user.ban"
	// CapUserUnban 解封账号。
	CapUserUnban Capability = "user.unban"
	// CapUserPurge 彻底删除（墓碑化）账号。
	CapUserPurge Capability = "user.purge"
	// CapUserOwnerAdd 把成员加为 co-owner。
	// 常量保留，但只授予系统管理员；owner/admin 都不能再加第二个 owner。
	CapUserOwnerAdd Capability = "user.owner.add"
	// CapUserOwnerRemove 降级/注销 owner（仅系统管理员，且须先完成空间交接）。
	CapUserOwnerRemove Capability = "user.owner.remove"
	// CapSpaceTransfer 空间交接。
	CapSpaceTransfer Capability = "space.transfer"
	// CapOwnerProvision 把账号设为 owner 并创建独立空间（仅系统管理员）。
	CapOwnerProvision Capability = "owner.provision"
)

// adminCapabilities 是普通管理员（admin）的能力集合，等于 co-owner 能力集，
// 仅少「交接空间」。admin 可以管理本空间普通成员，但不能新增/降级 owner，
// 也不能设立 owner 独立空间。
var adminCapabilities = []Capability{
	CapUserView,
	CapUserInvite,
	CapUserCreate,
	CapUserRoleChange,
	CapUserBan,
	CapUserUnban,
	CapUserPurge,
}

// ownerCapabilities 是空间拥有者（owner）的能力集合：普通管理员能力集 +
// 交接空间。唯一 owner 之后 owner 不能再加 co-owner，因此不含
// CapUserOwnerAdd；降级/注销 owner 与设立 owner 独立空间只属系统管理员。
var ownerCapabilities = append(append([]Capability{}, adminCapabilities...), CapSpaceTransfer)

// allCapabilities 是系统管理员的完整能力集合。
var allCapabilities = []Capability{
	CapUserView,
	CapUserInvite,
	CapUserCreate,
	CapUserRoleChange,
	CapUserBan,
	CapUserUnban,
	CapUserPurge,
	CapUserOwnerAdd,
	CapUserOwnerRemove,
	CapSpaceTransfer,
	CapOwnerProvision,
}

// CapabilitiesFor 返回某工作区角色对应的能力集合。
// 系统管理员旁路：无论当前工作区角色是什么都拿到全量能力。
// owner（空间拥有者）= 普通管理员能力集 + 交接空间；
// admin（普通管理员）= co-owner 能力集，仅少 space.transfer；
// contributor/viewer 不进入用户管理，返回空集合。
//
// 返回值是副本，调用方修改不会污染矩阵本身。
func CapabilitiesFor(role TenantRole, isSystemAdmin bool) []Capability {
	switch {
	case isSystemAdmin:
		return cloneCapabilities(allCapabilities)
	case role == TenantRoleOwner:
		return cloneCapabilities(ownerCapabilities)
	case role == TenantRoleAdmin:
		return cloneCapabilities(adminCapabilities)
	default:
		return []Capability{}
	}
}

func cloneCapabilities(caps []Capability) []Capability {
	out := make([]Capability, len(caps))
	copy(out, caps)
	return out
}
