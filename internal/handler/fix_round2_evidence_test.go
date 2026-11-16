package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// F19：封禁/解封响应不得泄露跨空间偏好（Preferences）。
func TestBanResponseDoesNotLeakPreferences(t *testing.T) {
	tenant := uint64(99)
	target := &types.User{
		ID:       "target",
		Email:    "target@example.com",
		IsActive: false,
		Preferences: types.UserPreferences{
			LastActiveTenantID:    &tenant,
			BoundAgentIDsByTenant: map[string][]string{"99": {"agent-secret"}},
		},
	}
	svc := umSuccessService(target)
	members := &umFakeMembershipService{member: &types.TenantMember{
		UserID: "target", TenantID: 10, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive,
	}}
	r := newUMHandlerRouter(NewUserManagementHandler(svc, members))

	w := postUMRoute(t, r, "/api/v1/tenants/10/members/target/ban", `{"reason":"x"}`)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.NotContains(t, body, "preferences")
	require.NotContains(t, body, "last_active_tenant_id")
	require.NotContains(t, body, "bound_agent_ids_by_tenant")
	require.NotContains(t, body, "agent-secret")
}

// F4：scoped API Key 的 manage_members 能力必须能进入改角色路径；
// 没有该能力的 Key 仍被拒。
func TestCanManageMemberRolesWithAPIKeyScope(t *testing.T) {
	withMembers := types.WithTenantAPIKeyScope(context.Background(), types.TenantAPIKeyScope{
		KeyID:        1,
		ScopeType:    types.APIKeyScopeTenant,
		Capabilities: types.StringArray{string(types.APIKeyCapabilityManageMembers)},
	})
	require.True(t, canManageMemberRoles(withMembers),
		"manage_members scoped key must manage roles")

	withoutMembers := types.WithTenantAPIKeyScope(context.Background(), types.TenantAPIKeyScope{
		KeyID:        2,
		ScopeType:    types.APIKeyScopeTenant,
		Capabilities: types.StringArray{string(types.APIKeyCapabilityReadAgents)},
	})
	require.False(t, canManageMemberRoles(withoutMembers),
		"a key without manage_members must stay rejected")

	ownerCtx := context.WithValue(context.Background(), types.TenantRoleContextKey, types.TenantRoleOwner)
	require.True(t, canManageMemberRoles(ownerCtx))
}
