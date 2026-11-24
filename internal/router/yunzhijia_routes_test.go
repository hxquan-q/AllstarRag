package router

// T03 TDD 红阶段：云之家登录路由（AC-16）。
//
// 红阶段契约：router.RegisterYunzhijiaAuthRoutes(*gin.RouterGroup, *handler.YunzhijiaAuthHandler)
// 注册 POST /api/v1/auth/yunzhijia/login，公开（不挂 Auth 中间件），
// 并复用 middleware.PublicAuthRateLimit()（默认 30 次/分/IP）。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type yzjT03RouteLoginStub struct {
	resp *types.LoginResponse
}

func (s *yzjT03RouteLoginStub) Login(context.Context, string) (*types.LoginResponse, error) {
	return s.resp, nil
}

var _ interfaces.YunzhijiaLoginService = (*yzjT03RouteLoginStub)(nil)

func newYZJT03RoutesEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stub := &yzjT03RouteLoginStub{resp: &types.LoginResponse{
		Success:      true,
		User:         &types.User{ID: "yzj-user-placeholder"},
		ActiveTenant: &types.Tenant{ID: 10024, Name: "测试空间"},
		Memberships:  []types.Membership{{TenantID: 10024, Role: types.TenantRoleViewer}},
		Token:        "access-token-placeholder",
		RefreshToken: "refresh-token-placeholder",
		Capabilities: &types.LoginCapabilities{CanCreateTenant: false},
	}}
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	cfg := &config.YunzhijiaSSOConfig{Enabled: true}
	RegisterYunzhijiaAuthRoutes(r.Group("/api/v1"), handler.NewYunzhijiaAuthHandler(cfg, stub))
	return r
}

func yzjT03PostRoute(t *testing.T, r *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/yunzhijia/login",
		bytes.NewBufferString(`{"ticket":"ticket-placeholder-route"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestYunzhijiaT03LoginRouteIsPublic(t *testing.T) {
	r := newYZJT03RoutesEngine(t)

	w := yzjT03PostRoute(t, r, "203.0.113.20:42000")
	require.Equal(t, http.StatusOK, w.Code, "公开路由不应要求认证，resp=%s", w.Body.String())
	assert.Contains(t, w.Body.String(), `"success":true`)
}

func TestYunzhijiaT03LoginRouteRateLimited(t *testing.T) {
	r := newYZJT03RoutesEngine(t)
	const remoteAddr = "203.0.113.21:42000"

	// 默认 IP 预算 30 次/分钟：前 30 次放行，第 31 次 429。
	for i := 0; i < 30; i++ {
		w := yzjT03PostRoute(t, r, remoteAddr)
		require.Equal(t, http.StatusOK, w.Code, "第 %d 次请求应放行，resp=%s", i+1, w.Body.String())
	}
	w := yzjT03PostRoute(t, r, remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "第 31 次应触发限速，resp=%s", w.Body.String())
}
