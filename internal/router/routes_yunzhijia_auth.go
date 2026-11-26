package router

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
)

// yunzhijiaLoginRateLimit 是登录入口的 IP 预算：办公网常用同一出口 IP，
// 且每次打开应用都会登录一次，按 300 次/分钟避免早高峰误伤。
const (
	yunzhijiaLoginRateWindow = 60 * time.Second
	yunzhijiaLoginRateLimit  = 300
)

// RegisterYunzhijiaAuthRoutes 注册云之家 ticket 免登录的公开入口。
//
// 与 /auth/login 一样按客户端 IP 限速，不挂认证中间件；Auth 中间件
// 通过 middleware 的公开路径白名单放行。
func RegisterYunzhijiaAuthRoutes(r *gin.RouterGroup, h *handler.YunzhijiaAuthHandler) {
	r.POST("/auth/yunzhijia/login",
		middleware.PublicAuthRateLimitWith(yunzhijiaLoginRateWindow, yunzhijiaLoginRateLimit, "yunzhijia-login"),
		h.Login)
}
