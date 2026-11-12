package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// 本文件覆盖 01-spec AC-1 / AC-2 / AC-5 的角色矩阵纯函数。
// 口径来源：02-plan §2.3 与 T05（一号一空间 feature 后只分配 admin/viewer）。
//   - owner / admin（普通管理员）：创建 admin / viewer；重置 admin / viewer
//   - admin 不能重置同级 admin（同级保护）
//   - contributor / viewer：无权限
//   - 任何人不能创建或重置 owner；contributor 不再作为可分配目标
//
// 系统管理员按 owner 处理在 handler 入口完成，不在纯函数里，因此这里只测角色本身。

// allTenantRoles 是矩阵遍历顺序，与 types.TenantRole 定义一致。
var allTenantRoles = []types.TenantRole{
	types.TenantRoleOwner,
	types.TenantRoleAdmin,
	types.TenantRoleContributor,
	types.TenantRoleViewer,
}

func TestCanCreateRoleFullMatrix(t *testing.T) {
	// 行 = 操作者，列 = 目标角色。
	want := map[types.TenantRole]map[types.TenantRole]bool{
		types.TenantRoleOwner: {
			types.TenantRoleOwner:       false, // 任何人都不能创建 owner
			types.TenantRoleAdmin:       true,
			types.TenantRoleContributor: false, // contributor 不再分配（AC-2）
			types.TenantRoleViewer:      true,
		},
		types.TenantRoleAdmin: {
			types.TenantRoleOwner:       false,
			types.TenantRoleAdmin:       true,
			types.TenantRoleContributor: false, // contributor 不再分配（AC-2）
			types.TenantRoleViewer:      true,
		},
		types.TenantRoleContributor: {
			types.TenantRoleOwner:       false,
			types.TenantRoleAdmin:       false,
			types.TenantRoleContributor: false,
			types.TenantRoleViewer:      false,
		},
		types.TenantRoleViewer: {
			types.TenantRoleOwner:       false,
			types.TenantRoleAdmin:       false,
			types.TenantRoleContributor: false,
			types.TenantRoleViewer:      false,
		},
	}

	for _, actor := range allTenantRoles {
		for _, target := range allTenantRoles {
			got := CanCreateRole(actor, target)
			if got != want[actor][target] {
				t.Errorf("CanCreateRole(%q, %q) = %v, want %v", actor, target, got, want[actor][target])
			}
		}
	}
}

func TestCanCreateRoleUnknownInputsFailClosed(t *testing.T) {
	unknown := types.TenantRole("wizard")
	for _, target := range allTenantRoles {
		if CanCreateRole(unknown, target) {
			t.Errorf("unknown actor must not create %q", target)
		}
		if CanCreateRole(types.TenantRoleOwner, unknown) {
			t.Errorf("owner must not create unknown role %q", target)
		}
	}
	if CanCreateRole("", types.TenantRoleViewer) {
		t.Error("empty actor must not create viewer")
	}
}

func TestCanResetRoleFullMatrix(t *testing.T) {
	want := map[types.TenantRole]map[types.TenantRole]bool{
		types.TenantRoleOwner: {
			types.TenantRoleOwner:       false, // 不能重置 owner（含另一位）
			types.TenantRoleAdmin:       true,
			types.TenantRoleContributor: false, // contributor 不再分配（AC-2）
			types.TenantRoleViewer:      true,
		},
		types.TenantRoleAdmin: {
			types.TenantRoleOwner:       false, // 不能重置上级
			types.TenantRoleAdmin:       false, // 普通管理员不能重置同级
			types.TenantRoleContributor: false, // contributor 不再分配（AC-2）
			types.TenantRoleViewer:      true,
		},
		types.TenantRoleContributor: {
			types.TenantRoleOwner:       false,
			types.TenantRoleAdmin:       false,
			types.TenantRoleContributor: false,
			types.TenantRoleViewer:      false,
		},
		types.TenantRoleViewer: {
			types.TenantRoleOwner:       false,
			types.TenantRoleAdmin:       false,
			types.TenantRoleContributor: false,
			types.TenantRoleViewer:      false,
		},
	}

	for _, actor := range allTenantRoles {
		for _, target := range allTenantRoles {
			got := CanResetRole(actor, target)
			if got != want[actor][target] {
				t.Errorf("CanResetRole(%q, %q) = %v, want %v", actor, target, got, want[actor][target])
			}
		}
	}
}

func TestCanResetRoleUnknownInputsFailClosed(t *testing.T) {
	unknown := types.TenantRole("wizard")
	for _, target := range allTenantRoles {
		if CanResetRole(unknown, target) {
			t.Errorf("unknown actor must not reset %q", target)
		}
		if CanResetRole(types.TenantRoleOwner, unknown) {
			t.Errorf("owner must not reset unknown role %q", target)
		}
	}
	if CanResetRole("", types.TenantRoleViewer) {
		t.Error("empty actor must not reset viewer")
	}
}
