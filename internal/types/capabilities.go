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
	// CapUserInvite 邀请新成员（含 co-owner）。
	CapUserInvite Capability = "user.invite"
	// CapUserCreate 直接创建账号（含 co-owner）。
	CapUserCreate Capability = "user.create"
	// CapUserRoleChange 在 viewer/contributor/admin 之间改角色。
	CapUserRoleChange Capability = "user.role.change"
	// CapUserBan 封禁账号。
	CapUserBan Capability = "user.ban"
	// CapUserUnban 解封账号。
	CapUserUnban Capability = "user.unban"
	// CapUserPurge 彻底删除（墓碑化）账号。
	CapUserPurge Capability = "user.purge"
	// CapUserOwnerAdd 把成员加为 co-owner。
	CapUserOwnerAdd Capability = "user.owner.add"
	// CapUserOwnerRemove 降级/注销 owner（仅系统管理员，且须先完成空间交接）。
	CapUserOwnerRemove Capability = "user.owner.remove"
	// CapSpaceTransfer 空间交接。
	CapSpaceTransfer Capability = "space.transfer"
	// CapOwnerProvision 把账号设为 owner 并创建独立空间（仅系统管理员）。
	CapOwnerProvision Capability = "owner.provision"
)

// ownerCapabilities 是普通管理员（owner）的能力集合：可管理本空间的
// viewer/contributor/admin，可加 co-owner、可交接空间；但降级/注销 owner
// 与设立 owner 独立空间只属系统管理员，因此不含 CapUserOwnerRemove 和
// CapOwnerProvision。
var ownerCapabilities = []Capability{
	CapUserView,
	CapUserInvite,
	CapUserCreate,
	CapUserRoleChange,
	CapUserBan,
	CapUserUnban,
	CapUserPurge,
	CapUserOwnerAdd,
	CapSpaceTransfer,
}

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
// admin/contributor/viewer 不进入用户管理，非系统管理员时返回空集合。
//
// 返回值是副本，调用方修改不会污染矩阵本身。
func CapabilitiesFor(role TenantRole, isSystemAdmin bool) []Capability {
	switch {
	case isSystemAdmin:
		return cloneCapabilities(allCapabilities)
	case role == TenantRoleOwner:
		return cloneCapabilities(ownerCapabilities)
	default:
		return []Capability{}
	}
}

func cloneCapabilities(caps []Capability) []Capability {
	out := make([]Capability, len(caps))
	copy(out, caps)
	return out
}
