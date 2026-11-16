package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// /auth/capabilities 红阶段用例（T01 / AC-1、AC-12b、AC-38）。
//
// 假定契约（02-plan 3.3 + T01 涉及文件）：
//   - internal/handler/capabilities.go 提供 (*AuthHandler).GetCapabilities；
//   - GET /api/v1/auth/capabilities 返回 {success:true, data:{role,
//     is_system_admin, capabilities:[]}}；
//   - 未认证返回 401。
//
// 业务规则只在 types.CapabilitiesFor 里断言；本文件只验证 handler
// 是否把同一份矩阵原样投影出来，不在 handler 里重复业务规则。

type capabilitiesUserService struct {
	interfaces.UserService
	user *types.User
	err  error
}

func (s *capabilitiesUserService) GetCurrentUser(context.Context) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

type capabilitiesPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Role          string   `json:"role"`
		IsSystemAdmin bool     `json:"is_system_admin"`
		Capabilities  []string `json:"capabilities"`
	} `json:"data"`
}

// newCapabilitiesRouter 复刻 middleware/auth.go 的 applyAuthSession：
// 角色/系统管理员标志/用户 id 都落在请求上下文里，handler 从同一处读取。
func newCapabilitiesRouter(h *AuthHandler, role types.TenantRole, systemAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "caller")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/api/v1/auth/capabilities", h.GetCapabilities)
	return r
}

func getCapabilities(t *testing.T, r *gin.Engine) (*httptest.ResponseRecorder, capabilitiesPayload) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/capabilities", nil)
	r.ServeHTTP(w, req)

	var payload capabilitiesPayload
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	}
	return w, payload
}

// capabilityNames 把唯一事实源的 []Capability 转成 JSON 视角的 []string，
// 让断言比较的是「同一份矩阵」，而不是测试里另抄一遍。
func capabilityNames(caps []types.Capability) []string {
	names := make([]string, 0, len(caps))
	for _, cap := range caps {
		names = append(names, string(cap))
	}
	return names
}

func TestGetCapabilitiesOwner(t *testing.T) {
	h := &AuthHandler{userService: &capabilitiesUserService{
		user: &types.User{ID: "caller", IsSystemAdmin: false},
	}}
	r := newCapabilitiesRouter(h, types.TenantRoleOwner, false)

	w, payload := getCapabilities(t, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, payload.Success)
	require.Equal(t, string(types.TenantRoleOwner), payload.Data.Role)
	require.False(t, payload.Data.IsSystemAdmin)
	require.ElementsMatch(t,
		capabilityNames(types.CapabilitiesFor(types.TenantRoleOwner, false)),
		payload.Data.Capabilities,
		"handler must project types.CapabilitiesFor verbatim (AC-12b)")
}

func TestGetCapabilitiesSystemAdmin(t *testing.T) {
	h := &AuthHandler{userService: &capabilitiesUserService{
		user: &types.User{ID: "caller", IsSystemAdmin: true},
	}}
	// 系统管理员当前空间角色即使是 admin，也必须拿到全量能力。
	r := newCapabilitiesRouter(h, types.TenantRoleAdmin, true)

	w, payload := getCapabilities(t, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, payload.Success)
	require.Equal(t, string(types.TenantRoleAdmin), payload.Data.Role)
	require.True(t, payload.Data.IsSystemAdmin)
	require.ElementsMatch(t,
		capabilityNames(types.CapabilitiesFor(types.TenantRoleAdmin, true)),
		payload.Data.Capabilities)
}

func TestGetCapabilitiesNonOwnerRolesHaveEmptyMatrix(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			h := &AuthHandler{userService: &capabilitiesUserService{
				user: &types.User{ID: "caller"},
			}}
			r := newCapabilitiesRouter(h, role, false)

			w, payload := getCapabilities(t, r)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, string(role), payload.Data.Role)
			require.False(t, payload.Data.IsSystemAdmin)
			require.ElementsMatch(t, capabilityNames(types.CapabilitiesFor(role, false)),
				payload.Data.Capabilities)
		})
	}
}

// TestGetCapabilitiesUnauthenticated 验证「未认证返回 401」。
// 生产路径上未认证请求会被 middleware.Auth 拦下；handler 以
// userService.GetCurrentUser 的失败兜底，保证直接调用也不放行。
func TestGetCapabilitiesUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{userService: &capabilitiesUserService{
		err: errors.New("no authenticated user in context"),
	}}
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/api/v1/auth/capabilities", h.GetCapabilities)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/capabilities", nil))

	require.Equal(t, http.StatusUnauthorized, w.Code)
}
