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

// TestCapabilitiesForOwnerMatrix 锁定 owner（空间拥有者）的能力集合：
// 可以管理本空间成员并交接空间；但不能再加第二个 owner、不能降级/注销
// owner、也不能设立新 owner 的独立空间（后者仅超管）。
func TestCapabilitiesForOwnerMatrix(t *testing.T) {
	want := []Capability{
		CapUserView,
		CapUserInvite,
		CapUserCreate,
		CapUserRoleChange,
		CapUserBan,
		CapUserUnban,
		CapUserPurge,
		CapSpaceTransfer,
	}

	got := CapabilitiesFor(TenantRoleOwner, false)

	require.NotContains(t, got, CapUserOwnerAdd,
		"owner must not be able to add a second owner; space ownership is unique (AC-2 / AC-4)")
	require.NotContains(t, got, CapUserOwnerRemove,
		"owner must not be able to demote/purge another owner (AC-4 / AC-17)")
	require.NotContains(t, got, CapOwnerProvision,
		"only a system administrator may provision an owner space (02-plan 3.2)")
	require.ElementsMatch(t, want, got,
		"owner matrix must be exactly the documented set; a missing or extra entry means a second, drifting source of truth")
}

// TestCapabilitiesForAdminMatrix 锁定 admin（普通管理员）的能力集合：
// 能力集 = co-owner 集，仅少「交接空间」；也不能加/降级 owner 或设立
// 独立 owner 空间。
func TestCapabilitiesForAdminMatrix(t *testing.T) {
	want := []Capability{
		CapUserView,
		CapUserInvite,
		CapUserCreate,
		CapUserRoleChange,
		CapUserBan,
		CapUserUnban,
		CapUserPurge,
	}

	got := CapabilitiesFor(TenantRoleAdmin, false)

	require.NotContains(t, got, CapSpaceTransfer,
		"only the space owner (or a system administrator) may transfer the space (AC-6)")
	require.NotContains(t, got, CapUserOwnerAdd,
		"admin must not be able to add a second owner (AC-2 / AC-4)")
	require.NotContains(t, got, CapUserOwnerRemove,
		"admin must never demote/purge the space owner (AC-8)")
	require.NotContains(t, got, CapOwnerProvision,
		"only a system administrator may provision an owner space (02-plan 3.2)")
	require.ElementsMatch(t, want, got,
		"admin matrix must be exactly the co-owner set minus space.transfer (AC-5)")
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

// TestCapabilitiesForContributorAndViewerHaveNoUserManagement 锁定
// contributor / viewer 不进用户管理（AC-1 / AC-2）：非超管时能力为空。
// admin 的能力由 TestCapabilitiesForAdminMatrix 单独覆盖。
func TestCapabilitiesForContributorAndViewerHaveNoUserManagement(t *testing.T) {
	for _, role := range []TenantRole{
		TenantRoleContributor,
		TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			require.Empty(t, CapabilitiesFor(role, false),
				"contributor/viewer must not gain user-management capabilities")
		})
	}
}

// TestCapabilitiesForReturnsNoDuplicates 防止矩阵表里同一能力被写两遍
// 导致前端 can() 计数/序列化出现重复项。
func TestCapabilitiesForReturnsNoDuplicates(t *testing.T) {
	for name, caps := range map[string][]Capability{
		"owner":                CapabilitiesFor(TenantRoleOwner, false),
		"admin":                CapabilitiesFor(TenantRoleAdmin, false),
		"system administrator": CapabilitiesFor(TenantRoleViewer, true),
	} {
		seen := map[Capability]bool{}
		for _, cap := range caps {
			require.False(t, seen[cap], "duplicate capability %q in %s matrix", cap, name)
			seen[cap] = true
		}
	}
}
