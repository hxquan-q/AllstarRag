package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
)

// RegisterYunzhijiaAuthRoutes 注册云之家 ticket 免登录的公开入口。
//
// 与 /auth/login 一样按客户端 IP 限速（默认 30 次/分钟），不挂认证
// 中间件；Auth 中间件通过 middleware 的公开路径白名单放行。
func RegisterYunzhijiaAuthRoutes(r *gin.RouterGroup, h *handler.YunzhijiaAuthHandler) {
	if h == nil {
		return
	}
	r.POST("/auth/yunzhijia/login", middleware.PublicAuthRateLimit(), h.Login)
}
