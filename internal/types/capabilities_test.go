package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 能力矩阵测试（T01 / AC-12b、AC-38）。
//
// 这里的断言就是本 feature 的「唯一角色→能力矩阵」契约：
// handler 只投影 CapabilitiesFor 的结果，不再重复业务判断；
// 前端只消费 /auth/capabilities 的投影。详见 02-plan 第 2 节。
//
// 当前实现文件 internal/types/capabilities.go 尚未创建，本文件属于
// TDD 红阶段用例（预期编译失败）。

// TestCapabilitiesForOwnerMatrix 锁定 owner（普通管理员）的能力集合：
// 可以管理本空间 viewer/contributor/admin，可加 co-owner、可交接空间；
// 但不能降级/注销任何 owner（user.owner.remove 仅超管），也不能设立新
// owner 的独立空间（owner.provision 仅超管）。
func TestCapabilitiesForOwnerMatrix(t *testing.T) {
	want := []Capability{
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

	got := CapabilitiesFor(TenantRoleOwner, false)

	require.ElementsMatch(t, want, got,
		"owner matrix must be exactly the documented set; a missing or extra entry means a second, drifting source of truth")
	require.NotContains(t, got, CapUserOwnerRemove,
		"owner must not be able to demote/purge another owner (AC-4 / AC-17)")
	require.NotContains(t, got, CapOwnerProvision,
		"only a system administrator may provision an owner space (02-plan 3.2)")
}

// TestCapabilitiesForSystemAdminIsFullMatrix 锁定系统管理员全量能力，
// 且与工作区角色无关：即使当前空间角色是 admin/viewer，系统管理员
// 仍拿到全部能力（superuser 旁路）。
func TestCapabilitiesForSystemAdminIsFullMatrix(t *testing.T) {
	all := []Capability{
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

	for _, role := range []TenantRole{
		TenantRoleOwner,
		TenantRoleAdmin,
		TenantRoleContributor,
		TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			require.ElementsMatch(t, all, CapabilitiesFor(role, true),
				"system administrator must receive the full matrix regardless of workspace role")
		})
	}
}

// TestCapabilitiesForNonOwnerRolesHaveNoUserManagement 锁定 admin /
// contributor / viewer 不进用户管理：只有 owner + 系统管理员能进入
// 用户管理页（AC-1），因此非超管的这三个角色能力为空。
func TestCapabilitiesForNonOwnerRolesHaveNoUserManagement(t *testing.T) {
	for _, role := range []TenantRole{
		TenantRoleAdmin,
		TenantRoleContributor,
		TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			require.Empty(t, CapabilitiesFor(role, false),
				"non-owner workspace roles must not gain user-management capabilities")
		})
	}
}

// TestCapabilitiesForReturnsNoDuplicates 防止矩阵表里同一能力被写两遍
// 导致前端 can() 计数/序列化出现重复项。
func TestCapabilitiesForReturnsNoDuplicates(t *testing.T) {
	for name, caps := range map[string][]Capability{
		"owner":                CapabilitiesFor(TenantRoleOwner, false),
		"system administrator": CapabilitiesFor(TenantRoleViewer, true),
	} {
		seen := map[Capability]bool{}
		for _, cap := range caps {
			require.False(t, seen[cap], "duplicate capability %q in %s matrix", cap, name)
			seen[cap] = true
		}
	}
}
