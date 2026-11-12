package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T05 handler 层红阶段用例（T05-1/2/4、AC-32）。
//
// 假定契约：
//   - (*UserManagementHandler).ListUsers 处理
//     GET /tenants/:id/user-management?role=&q=&page=&page_size=；
//   - 响应 200 {success:true,data:{items,total,role_counts}}；
//   - role 为空 = 全部，非法值 400（不触达服务层）；
//   - page/page_size 沿用 parseListPagination（默认 1/20，上限 100）；
//   - 服务层 *AppError 原样映射（403/404/400），其他错误 500；
//   - 响应不得包含密码哈希、token 等敏感字段。

// umListFakeService 只实现 ListUsers，其余方法由嵌入接口兜底。
// 引用 interfaces.UserManagementListResult 是本轮红阶段的编译失败点。
type umListFakeService struct {
	interfaces.UserManagementService

	result *interfaces.UserManagementListResult
	err    error

	calls       int
	gotTenantID uint64
	gotRole     types.TenantRole
	gotQuery    string
	gotPage     int
	gotPageSize int
}

func (s *umListFakeService) ListUsers(
	_ context.Context,
	tenantID uint64,
	role types.TenantRole,
	query string,
	page, pageSize int,
) (*interfaces.UserManagementListResult, error) {
	s.calls++
	s.gotTenantID = tenantID
	s.gotRole = role
	s.gotQuery = query
	s.gotPage = page
	s.gotPageSize = pageSize
	return s.result, s.err
}

type umListPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []struct {
			UserID    string    `json:"user_id"`
			Email     string    `json:"email"`
			Username  string    `json:"username"`
			Role      string    `json:"role"`
			Status    string    `json:"status"`
			IsActive  bool      `json:"is_active"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"items"`
		Total      int64            `json:"total"`
		RoleCounts map[string]int64 `json:"role_counts"`
	} `json:"data"`
}

func newUMListHandlerRouter(h *UserManagementHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.UserIDContextKey, "u-owner")
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(10))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleOwner)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, false)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/api/v1/tenants/:id/user-management", h.ListUsers)
	return r
}

func getUMListRoute(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func umListSuccessResult() *interfaces.UserManagementListResult {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	return &interfaces.UserManagementListResult{
		Items: []interfaces.UserManagementListItem{
			{
				UserID: "u-active", Email: "active@example.com", Username: "active",
				Role: types.TenantRoleAdmin, Status: "active", IsActive: true, CreatedAt: created,
			},
			{
				UserID: "u-banned", Email: "banned@example.com", Username: "banned",
				Role: types.TenantRoleViewer, Status: "banned", IsActive: false, CreatedAt: created,
			},
		},
		Total: 2,
		RoleCounts: map[types.TenantRole]int64{
			types.TenantRoleOwner:  0,
			types.TenantRoleAdmin:  1,
			types.TenantRoleViewer: 1,
		},
	}
}

// TestUserManagementListUsersResponse 锁定响应形状与 AC-32（不泄露敏感字段）。
func TestUserManagementListUsersResponse(t *testing.T) {
	svc := &umListFakeService{result: umListSuccessResult()}
	h := NewUserManagementHandler(svc, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management")

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, forbidden := range []string{"password", "token", "hash", "secret"} {
		require.NotContains(t, strings.ToLower(body), forbidden,
			"user-management list must not leak %q", forbidden)
	}

	var payload umListPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, int64(2), payload.Data.Total)
	require.Len(t, payload.Data.Items, 2)
	require.Equal(t, map[string]int64{
		"owner": 0, "admin": 1, "viewer": 1,
	}, payload.Data.RoleCounts,
		"the directory response must not expose a contributor bucket")

	bannedIndex := -1
	for i := range payload.Data.Items {
		if payload.Data.Items[i].UserID == "u-banned" {
			bannedIndex = i
		}
	}
	require.NotEqual(t, -1, bannedIndex)
	banned := payload.Data.Items[bannedIndex]
	require.Equal(t, "banned@example.com", banned.Email)
	require.Equal(t, "banned", banned.Status)
	require.False(t, banned.IsActive)
	require.Equal(t, "viewer", banned.Role)
}

// TestUserManagementListUsersPassesFilters 锁定查询参数原样下传。
func TestUserManagementListUsersPassesFilters(t *testing.T) {
	svc := &umListFakeService{result: umListSuccessResult()}
	h := NewUserManagementHandler(svc, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r,
		"/api/v1/tenants/10/user-management?role=admin&q=%20alice%20&page=2&page_size=50")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, uint64(10), svc.gotTenantID)
	require.Equal(t, types.TenantRoleAdmin, svc.gotRole)
	require.Equal(t, "alice", svc.gotQuery, "q must be trimmed before reaching the service")
	require.Equal(t, 2, svc.gotPage)
	require.Equal(t, 50, svc.gotPageSize)
}

// TestUserManagementListUsersEmptyRoleMeansAll 锁定空 role 表示全部，
// 以及默认分页 1/20。
func TestUserManagementListUsersEmptyRoleMeansAll(t *testing.T) {
	svc := &umListFakeService{result: umListSuccessResult()}
	h := NewUserManagementHandler(svc, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, types.TenantRole(""), svc.gotRole)
	require.Equal(t, 1, svc.gotPage)
	require.Equal(t, 20, svc.gotPageSize)
}

// TestUserManagementListUsersInvalidRoleRejected 锁定非法 role → 400，
// 且不触达服务层。
func TestUserManagementListUsersInvalidRoleRejected(t *testing.T) {
	for _, role := range []string{"superuser", "member", "system_admin"} {
		role := role
		t.Run(role, func(t *testing.T) {
			svc := &umListFakeService{result: umListSuccessResult()}
			h := NewUserManagementHandler(svc, nil)
			r := newUMListHandlerRouter(h)

			w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management?role="+role)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, svc.calls, "invalid role must be rejected before the service call")
		})
	}
}

// TestUserManagementListUsersInvalidPaginationRejected 锁定分页参数与现有
// 列表接口同口径：page < 1 / page_size > 100 → 400。
func TestUserManagementListUsersInvalidPaginationRejected(t *testing.T) {
	for _, query := range []string{"page=0", "page_size=101"} {
		query := query
		t.Run(query, func(t *testing.T) {
			svc := &umListFakeService{result: umListSuccessResult()}
			h := NewUserManagementHandler(svc, nil)
			r := newUMListHandlerRouter(h)

			w := getUMListRoute(t, r, "/api/v1/tenants/10/user-management?"+query)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Zero(t, svc.calls, "invalid pagination must be rejected before the service call")
		})
	}
}

// TestUserManagementListUsersInvalidTenantID 锁定非法租户 ID → 400。
func TestUserManagementListUsersInvalidTenantID(t *testing.T) {
	svc := &umListFakeService{result: umListSuccessResult()}
	h := NewUserManagementHandler(svc, nil)
	r := newUMListHandlerRouter(h)

	w := getUMListRoute(t, r, "/api/v1/tenants/not-a-number/user-management")

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, svc.calls)
}

// TestUserManagementListUsersMapsServiceErrors 锁定服务层错误映射。
func TestUserManagementListUsersMapsServiceErrors(t *testing.T) {
	t.Run("forbidden", func(t *testing.T) {
		svc := &umListFakeService{err: apperrors.NewForbiddenError("only workspace owners can list users")}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h), "/api/v1/tenants/10/user-management")
		require.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("internal", func(t *testing.T) {
		svc := &umListFakeService{err: context.DeadlineExceeded}
		h := NewUserManagementHandler(svc, nil)
		w := getUMListRoute(t, newUMListHandlerRouter(h), "/api/v1/tenants/10/user-management")
		require.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
