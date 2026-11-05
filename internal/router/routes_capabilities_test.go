package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T01 / AC-38（C4）路由守卫用例：
// GET /api/v1/auth/capabilities 必须真实注册在 v1 组下，并跟随
// middleware.Auth 的认证链。未携带任何凭据的请求应得到 401——
// 后端拒绝，而不是只靠前端隐藏。
//
// 红阶段：路由尚未注册，第一条断言先失败。
func TestAuthCapabilitiesRouteIsRegisteredBehindAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(middleware.Auth(nil, nil, nil, nil, cfg))
	RegisterAuthRoutes(engine.Group("/api/v1"), &handler.AuthHandler{}, &rbacGuards{cfg: cfg})

	registered := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/api/v1/auth/capabilities" {
			registered = true
			break
		}
	}
	require.True(t, registered,
		"GET /api/v1/auth/capabilities must be registered in RegisterAuthRoutes")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/capabilities", nil)
	engine.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code,
		"unauthenticated /auth/capabilities must be rejected by backend auth, not only hidden in the UI")
}
