package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T22 handler 层红阶段用例（AC-1，多角色参数解析与分派）。
//
// 假定契约：
//   - role 支持逗号分隔多值，逐段 trim；任一段为空或非法 → 400 且不触达
//     service（空段包含末尾逗号、连续逗号、纯空格）；
//   - 单值继续走旧 ListUsers / ListUsersScoped（零破坏）；
//   - 多值走新增的 ListUsersWithRoles / ListUsersScopedWithRoles；
//   - scope 解析与既有行为一致。

type umMultiRoleFake struct {
	interfaces.UserManagementService

	listCalls        int
	scopedCalls      int
	multiCalls       int
	scopedMultiCalls int

	gotRoles  []types.TenantRole
	gotScope  interfaces.UserManagementListScope
	lastError error
}

func (f *umMultiRoleFake) ListUsers(
	_ context.Context, _ uint64, role types.TenantRole,
	_ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	f.listCalls++
	f.gotRoles = []types.TenantRole{role}
	return umListSuccessResult(), nil
}

func (f *umMultiRoleFake) ListUsersScoped(
	_ context.Context, _ uint64, scope interfaces.UserManagementListScope,
	role types.TenantRole, _ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	f.scopedCalls++
	f.gotScope = scope
	f.gotRoles = []types.TenantRole{role}
	return umListSuccessResult(), nil
}

func (f *umMultiRoleFake) ListUsersWithRoles(
	_ context.Context, _ uint64, roles []types.TenantRole,
	_ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	f.multiCalls++
	f.gotRoles = roles
	return umListSuccessResult(), nil
}

func (f *umMultiRoleFake) ListUsersScopedWithRoles(
	_ context.Context, _ uint64, scope interfaces.UserManagementListScope,
	roles []types.TenantRole, _ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	f.scopedMultiCalls++
	f.gotScope = scope
	f.gotRoles = roles
	return umListSuccessResult(), nil
}

func TestUserManagementListUsersMultiRoleParsesAndDispatches(t *testing.T) {
	fake := &umMultiRoleFake{}
	h := NewUserManagementHandler(fake, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management?role=contributor,admin")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.multiCalls)
	require.Equal(t, 0, fake.listCalls)
	require.Equal(t, []types.TenantRole{
		types.TenantRoleContributor, types.TenantRoleAdmin,
	}, fake.gotRoles)
}

func TestUserManagementListUsersSingleRoleKeepsLegacyPath(t *testing.T) {
	fake := &umMultiRoleFake{}
	h := NewUserManagementHandler(fake, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management?role=admin")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.listCalls,
		"单值必须继续走旧 ListUsers，保证零破坏")
	require.Equal(t, 0, fake.multiCalls)
	require.Equal(t, []types.TenantRole{types.TenantRoleAdmin}, fake.gotRoles)
}

func TestUserManagementListUsersMultiRoleScopedDispatches(t *testing.T) {
	fake := &umMultiRoleFake{}
	h := NewUserManagementHandler(fake, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r,
		"/api/v1/tenants/10/user-management?scope=all&role=contributor,admin")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, fake.scopedMultiCalls)
	require.Equal(t, 0, fake.scopedCalls)
	require.Equal(t, interfaces.UserManagementScopeAll, fake.gotScope)
	require.Equal(t, []types.TenantRole{
		types.TenantRoleContributor, types.TenantRoleAdmin,
	}, fake.gotRoles)
}

func TestUserManagementListUsersMultiRoleRejectsInvalidBeforeService(t *testing.T) {
	cases := []string{
		"role=contributor,",
		"role=,admin",
		"role=contributor,,admin",
		"role=Admin",
		"role=admin,superuser",
		"role=admin,%20",
	}
	for _, query := range cases {
		query := query
		t.Run(query, func(t *testing.T) {
			fake := &umMultiRoleFake{}
			h := NewUserManagementHandler(fake, nil)
			r := newUMListHandlerRouter(h)

			w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management?"+query)
			require.Equal(t, http.StatusBadRequest, w.Code,
				"非法多角色参数必须 400")
			require.Zero(t, fake.listCalls+fake.scopedCalls+
				fake.multiCalls+fake.scopedMultiCalls,
				"参数非法时不得触达 service")
		})
	}
}
