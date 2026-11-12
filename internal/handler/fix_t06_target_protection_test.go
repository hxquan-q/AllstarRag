package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// T06 红阶段（handler 层）：普通管理员拿到 co-owner 能力集后，改角色入口
// 必须放行 admin→viewer / admin→admin 目标的合法变更，并保留 owner 自保护
// 与 owner 目标保护。
//
// 服务层同级保护（admin→admin 403）在 fix_t06_target_protection_test.go
// 断言；这里只锁定 handler 自身的调用者门槛（当前 canManageMemberRoles
// 只认 owner/超管，普通管理员会在这里被 403）。
func TestT06UpdateMemberRoleAdminCanManageViewer(t *testing.T) {
	called := false
	var gotRole types.TenantRole
	ms := &roleGuardMemberService{
		getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
			return activeMember("u-viewer", tenantID, types.TenantRoleViewer), nil
		},
		updateRole: func(_ context.Context, _ string, _ uint64, newRole types.TenantRole) error {
			called = true
			gotRole = newRole
			return nil
		},
	}

	w := putMemberRole(t, ms, memberRoleSession{
		callerID: "u-admin", role: types.TenantRoleAdmin,
	}, 1, "u-viewer", types.TenantRoleAdmin)

	if w.Code != http.StatusOK {
		t.Fatalf("admin must be able to change a viewer's role, got %d body=%s", w.Code, w.Body.String())
	}
	if !called || gotRole != types.TenantRoleAdmin {
		t.Fatalf("service.UpdateRole must run with role admin, called=%v role=%q", called, gotRole)
	}
}

func TestT06UpdateMemberRoleOwnerSelfChangeRejected(t *testing.T) {
	called := false
	ms := &roleGuardMemberService{
		getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
			return activeMember("u-owner", tenantID, types.TenantRoleOwner), nil
		},
		updateRole: func(context.Context, string, uint64, types.TenantRole) error {
			called = true
			return nil
		},
	}

	w := putMemberRole(t, ms, memberRoleSession{
		callerID: "u-owner", role: types.TenantRoleOwner,
	}, 1, "u-owner", types.TenantRoleAdmin)

	if w.Code != http.StatusForbidden {
		t.Fatalf("owner must not demote themselves directly, got %d body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Fatalf("service.UpdateRole must not run for owner self-demotion")
	}
}
