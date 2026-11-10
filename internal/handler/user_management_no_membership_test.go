package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T44 handler 层红阶段用例：without_membership 参数解析与 no_membership_count
// 响应字段（Codex 复核 ①）。
//
// 契约（03-tickets/T44.md + 05-tests §79）：
//   - 新查询参数 without_membership（严格布尔：仅 true/false，其他值 400）；
//   - scope=all & without_membership=true → 分派新方法
//     ListUsersWithoutMembership(ctx, tenantID, q, page, pageSize)，
//     不携带 role（角色过滤与无空间过滤互斥，同时出现 → 400）；
//   - scope 缺省/current/other 带 without_membership=true → 400 且不触达
//     service（无空间目录只在全平台视图存在）；
//   - without_membership=false 等价于缺省（正常 scoped 路径）；
//   - 响应：scope=all 时 data 下新增 no_membership_count（与 role_counts
//     同级）；scope != all 时该键不存在（而非 0）。

// umT44FakeService 记录四条列表路径的调用；嵌入接口兜底其余方法。
type umT44FakeService struct {
	interfaces.UserManagementService

	scopedCalls   int
	noMemberCalls int
	legacyCalls   int
	gotTenantID   uint64
	gotQuery      string
	gotPage       int
	gotPageSize   int
}

func (s *umT44FakeService) ListUsersScoped(
	_ context.Context, tenantID uint64, _ interfaces.UserManagementListScope,
	_ types.TenantRole, query string, page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	s.scopedCalls++
	s.gotTenantID = tenantID
	s.gotQuery = query
	s.gotPage = page
	s.gotPageSize = pageSize
	return umT44Result(), nil
}

// ListUsers 补缺省 scope（不带 scope 参数）路径的桩：handler 对该路径
// 分派既有 ListUsers 而非 scoped/新方法，嵌入的 nil 接口会让它 panic
// 成 500。返回不含无空间计数的既有成功结果。
func (s *umT44FakeService) ListUsers(
	_ context.Context, tenantID uint64, _ types.TenantRole,
	query string, page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	s.legacyCalls++
	s.gotTenantID = tenantID
	s.gotQuery = query
	s.gotPage = page
	s.gotPageSize = pageSize
	return umListSuccessResult(), nil
}

func (s *umT44FakeService) ListUsersWithoutMembership(
	_ context.Context, tenantID uint64, query string, page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	s.noMemberCalls++
	s.gotTenantID = tenantID
	s.gotQuery = query
	s.gotPage = page
	s.gotPageSize = pageSize
	return umT44Result(), nil
}

// umT44Result 构造带 NoMembershipCount 的成功结果（字段本身是红点）。
func umT44Result() *interfaces.UserManagementListResult {
	result := umListSuccessResult()
	result.NoMembershipCount = 3
	return result
}

// umT44DataPayload 用 map 解析 data，可断言键存在性。
func umT44DataPayload(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var payload struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	require.True(t, payload.Success)
	return payload.Data
}

// TestT44WithoutMembershipParamParsing 锁定参数解析与分派。
func TestT44WithoutMembershipParamParsing(t *testing.T) {
	t.Run("all_true_dispatches_to_no_membership", func(t *testing.T) {
		svc := &umT44FakeService{}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true&q=%20probe%20&page=2&page_size=50")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, 1, svc.noMemberCalls)
		require.Zero(t, svc.scopedCalls, "无空间过滤必须走新方法，不再走 scoped 路径")
		require.Equal(t, uint64(10), svc.gotTenantID)
		require.Equal(t, "probe", svc.gotQuery, "q 必须在进入 service 前 trim")
		require.Equal(t, 2, svc.gotPage)
		require.Equal(t, 50, svc.gotPageSize)
	})

	t.Run("false_means_absent", func(t *testing.T) {
		svc := &umT44FakeService{}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all&without_membership=false")
		require.Equal(t, http.StatusOK, w.Code)
		require.Zero(t, svc.noMemberCalls)
		require.Equal(t, 1, svc.scopedCalls, "false 等价缺省，走正常 scoped 路径")
	})

	t.Run("invalid_boolean_rejected_before_service", func(t *testing.T) {
		for _, raw := range []string{"yes", "1", "TRUE", ""} {
			raw := raw
			t.Run("value_"+raw, func(t *testing.T) {
				svc := &umT44FakeService{}
				h := NewUserManagementHandler(svc, nil)
				w := getUMListRoute(t, newUMListHandlerRouter(h),
					"/api/v1/tenants/10/user-management?scope=all&without_membership="+raw)
				require.Equal(t, http.StatusBadRequest, w.Code,
					"without_membership 只接受 true/false（空串视为非法，不宽容）")
				require.Zero(t, svc.noMemberCalls+svc.scopedCalls)
			})
		}
	})

	t.Run("non_all_scope_rejected", func(t *testing.T) {
		paths := []string{
			"/api/v1/tenants/10/user-management?scope=current&without_membership=true",
			"/api/v1/tenants/10/user-management?scope=other&without_membership=true",
			// scope 缺省等价 current，同样拒绝
			"/api/v1/tenants/10/user-management?without_membership=true",
		}
		for _, path := range paths {
			path := path
			t.Run(path, func(t *testing.T) {
				svc := &umT44FakeService{}
				h := NewUserManagementHandler(svc, nil)
				w := getUMListRoute(t, newUMListHandlerRouter(h), path)
				require.Equal(t, http.StatusBadRequest, w.Code,
					"无空间过滤只在 scope=all 可用，其他 scope 一律 400")
				require.Zero(t, svc.noMemberCalls+svc.scopedCalls,
					"400 必须在 handler 层拒绝，不触达 service")
			})
		}
	})

	t.Run("role_conflicts_with_without_membership", func(t *testing.T) {
		svc := &umT44FakeService{}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true&role=viewer")
		require.Equal(t, http.StatusBadRequest, w.Code,
			"无空间账号没有角色，role 过滤与 without_membership 互斥")
		require.Zero(t, svc.noMemberCalls+svc.scopedCalls)
	})
}

// TestT44NoMembershipCountInResponse 锁定响应字段：scope=all 必含
// no_membership_count，其他 scope 该键不存在。
func TestT44NoMembershipCountInResponse(t *testing.T) {
	t.Run("all_has_key", func(t *testing.T) {
		svc := &umT44FakeService{}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all")
		require.Equal(t, http.StatusOK, w.Code)
		data := umT44DataPayload(t, w.Body.Bytes())
		require.Contains(t, data, "no_membership_count")
		require.Contains(t, data, "role_counts", "既有字段不回归")
		require.Equal(t, float64(3), data["no_membership_count"])
	})

	t.Run("all_with_filter_has_key", func(t *testing.T) {
		svc := &umT44FakeService{}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h),
			"/api/v1/tenants/10/user-management?scope=all&without_membership=true")
		require.Equal(t, http.StatusOK, w.Code)
		data := umT44DataPayload(t, w.Body.Bytes())
		require.Contains(t, data, "no_membership_count")
	})

	t.Run("other_and_current_omit_key", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/tenants/10/user-management?scope=other",
			"/api/v1/tenants/10/user-management",
		} {
			path := path
			t.Run(path, func(t *testing.T) {
				svc := &umT44FakeService{}
				h := NewUserManagementHandler(svc, nil)
				w := getUMListRoute(t, newUMListHandlerRouter(h), path)
				require.Equal(t, http.StatusOK, w.Code)
				data := umT44DataPayload(t, w.Body.Bytes())
				require.NotContains(t, data, "no_membership_count",
					"非 scope=all 不渲染无空间目录，键必须缺席（而不是 0）")
				// 键缺席之外还要锁定分派路径：这两条路径绝不能走无空间新方法。
				require.Zero(t, svc.noMemberCalls)
				if strings.Contains(path, "scope=other") {
					require.Equal(t, 1, svc.scopedCalls, "scope=other 走既有 scoped 路径")
					require.Zero(t, svc.legacyCalls)
				} else {
					require.Equal(t, 1, svc.legacyCalls, "缺省 scope 走既有 ListUsers 路径")
					require.Zero(t, svc.scopedCalls)
				}
			})
		}
	})
}
