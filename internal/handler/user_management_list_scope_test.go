package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T16 handler 层红阶段用例：scope 参数解析 / 非法值 400 / memberships 响应。
//
// 契约（05-tests.md §25.1）：
//   - scope 缺省或 current → 旧 ListUsers（现有调用方零破坏，现有测试不动）；
//   - scope=other/all → ListUsersScoped，role/q/page/page_size 原样下传；
//   - scope 非法（current/other/all 之外，精确匹配）→ 400 且不触达 service；
//   - ListUsersScoped 返回的 items[].memberships（tenant_id/tenant_name/role）
//     原样进入 JSON 响应；
//   - 服务层 *AppError 原样映射（403），其他错误 500。

// umScopeFakeService 同时实现新旧两个列表方法，记录各自调用与参数。
// 引用 interfaces.UserManagementListScope / UserManagementMembership 是
// 本轮红阶段的编译失败点。
type umScopeFakeService struct {
	interfaces.UserManagementService

	legacyResult *interfaces.UserManagementListResult
	scopedResult *interfaces.UserManagementListResult
	err          error

	legacyCalls int
	scopedCalls int
	gotTenantID uint64
	gotScope    interfaces.UserManagementListScope
	gotRole     types.TenantRole
	gotQuery    string
	gotPage     int
	gotPageSize int
}

func (s *umScopeFakeService) ListUsers(
	_ context.Context,
	_ uint64,
	_ types.TenantRole,
	_ string,
	_, _ int,
) (*interfaces.UserManagementListResult, error) {
	s.legacyCalls++
	return s.legacyResult, s.err
}

func (s *umScopeFakeService) ListUsersScoped(
	_ context.Context,
	tenantID uint64,
	scope interfaces.UserManagementListScope,
	role types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	s.scopedCalls++
	s.gotTenantID = tenantID
	s.gotScope = scope
	s.gotRole = role
	s.gotQuery = query
	s.gotPage = page
	s.gotPageSize = pageSize
	return s.scopedResult, s.err
}

// umScopeScopedResult 构造一个带 memberships 的多空间行。
func umScopeScopedResult() *interfaces.UserManagementListResult {
	created := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	return &interfaces.UserManagementListResult{
		Items: []interfaces.UserManagementListItem{
			{
				UserID: "u-dual", Email: "dual@example.com", Username: "dual",
				Role: types.TenantRoleViewer, Status: "active", IsActive: true,
				CreatedAt: created,
				Memberships: []interfaces.UserManagementMembership{
					{TenantID: 10, TenantName: "空间A", Role: types.TenantRoleViewer},
					{TenantID: 20, TenantName: "空间B", Role: types.TenantRoleAdmin},
				},
			},
		},
		Total: 1,
		RoleCounts: map[types.TenantRole]int64{
			types.TenantRoleOwner:  0,
			types.TenantRoleAdmin:  1,
			types.TenantRoleViewer: 1,
		},
	}
}

// umScopePayload 只解析 scope 模式需要断言的字段。
type umScopePayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []struct {
			UserID      string `json:"user_id"`
			Memberships []struct {
				TenantID   uint64 `json:"tenant_id"`
				TenantName string `json:"tenant_name"`
				Role       string `json:"role"`
			} `json:"memberships"`
		} `json:"items"`
		Total int64 `json:"total"`
	} `json:"data"`
}

// TestUserManagementListUsersScopeParsing 锁定 T16-1/2/9：scope 缺省与
// current 走旧 ListUsers；other/all 走 ListUsersScoped 且参数原样下传。
func TestUserManagementListUsersScopeParsing(t *testing.T) {
	t.Run("default_no_scope_uses_legacy", func(t *testing.T) {
		svc := &umScopeFakeService{legacyResult: umListSuccessResult()}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management")

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, svc.legacyCalls,
			"缺省 scope 必须走旧 ListUsers，现有调用方零破坏")
		require.Zero(t, svc.scopedCalls)
	})

	t.Run("explicit_current_uses_legacy", func(t *testing.T) {
		svc := &umScopeFakeService{legacyResult: umListSuccessResult()}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=current")

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, svc.legacyCalls,
			"显式 scope=current 与缺省等价，走旧 ListUsers")
		require.Zero(t, svc.scopedCalls)
	})

	t.Run("other_uses_scoped", func(t *testing.T) {
		svc := &umScopeFakeService{scopedResult: umScopeScopedResult()}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=other&role=admin&q=%20dual%20&page=2&page_size=50")

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, svc.scopedCalls)
		require.Zero(t, svc.legacyCalls)
		require.Equal(t, uint64(10), svc.gotTenantID)
		require.Equal(t, interfaces.UserManagementScopeOther, svc.gotScope)
		require.Equal(t, types.TenantRoleAdmin, svc.gotRole)
		require.Equal(t, "dual", svc.gotQuery, "q 必须在进入 service 前 trim")
		require.Equal(t, 2, svc.gotPage)
		require.Equal(t, 50, svc.gotPageSize)
	})

	t.Run("all_uses_scoped", func(t *testing.T) {
		svc := &umScopeFakeService{scopedResult: umScopeScopedResult()}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all")

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, svc.scopedCalls)
		require.Zero(t, svc.legacyCalls)
		require.Equal(t, interfaces.UserManagementScopeAll, svc.gotScope)
	})
}

// TestUserManagementListUsersInvalidScopeRejected 锁定非法 scope → 400，
// 且新旧两条 service 路径都不被触达。
func TestUserManagementListUsersInvalidScopeRejected(t *testing.T) {
	for _, scope := range []string{"galaxy", "everyone", "platform"} {
		scope := scope
		t.Run(scope, func(t *testing.T) {
			svc := &umScopeFakeService{
				legacyResult: umListSuccessResult(),
				scopedResult: umScopeScopedResult(),
			}
			h := NewUserManagementHandler(svc, nil)
			w := getUMListRoute(t, newUMListHandlerRouter(h),
				"/api/v1/tenants/10/user-management?scope="+scope)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, svc.legacyCalls, "非法 scope 必须在 handler 层拒绝，不触达旧 service 路径")
			require.Zero(t, svc.scopedCalls, "非法 scope 必须在 handler 层拒绝，不触达新 service 路径")
		})
	}
}

// TestUserManagementListUsersScopeMembershipsInResponse 锁定多空间模式下
// items[].memberships 的 JSON 形状（tenant_id/tenant_name/role）。
func TestUserManagementListUsersScopeMembershipsInResponse(t *testing.T) {
	svc := &umScopeFakeService{scopedResult: umScopeScopedResult()}
	h := NewUserManagementHandler(svc, nil)
	w := getUMListRoute(t, newUMListHandlerRouter(h),
		"/api/v1/tenants/10/user-management?scope=all")

	require.Equal(t, http.StatusOK, w.Code)

	var payload umScopePayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, int64(1), payload.Data.Total)
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, "u-dual", payload.Data.Items[0].UserID)

	require.Len(t, payload.Data.Items[0].Memberships, 2)
	first := payload.Data.Items[0].Memberships[0]
	require.Equal(t, uint64(10), first.TenantID)
	require.Equal(t, "空间A", first.TenantName)
	require.Equal(t, "viewer", first.Role)
	second := payload.Data.Items[0].Memberships[1]
	require.Equal(t, uint64(20), second.TenantID)
	require.Equal(t, "空间B", second.TenantName)
	require.Equal(t, "admin", second.Role)
}

// TestUserManagementListUsersScopedMapsServiceErrors 锁定 scope 模式下
// 服务层错误映射与旧路径一致（403/500）。
func TestUserManagementListUsersScopedMapsServiceErrors(t *testing.T) {
	t.Run("forbidden", func(t *testing.T) {
		svc := &umScopeFakeService{err: apperrors.NewForbiddenError("system admin only")}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=other")
		require.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("internal", func(t *testing.T) {
		svc := &umScopeFakeService{err: context.DeadlineExceeded}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all")
		require.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
