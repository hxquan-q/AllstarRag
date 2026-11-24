package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterYunzhijiaAdminRoutes 注册云之家同步管理端接口。
//
// 全部挂在 /api/v1/system/admin 组级 SystemAdmin 守卫下，handler 内
// 还会再校验一次系统管理员身份。不挂 PathTenantMatch：这些是平台级
// 操作，不依赖当前空间。
func RegisterYunzhijiaAdminRoutes(
	r *gin.RouterGroup,
	h *handler.YunzhijiaAdminHandler,
	g *rbacGuards,
) {
	adminRoutes := r.Group("/system/admin/yunzhijia", g.SystemAdmin())
	{
		adminRoutes.GET("/sync", h.GetSyncStatus)
		adminRoutes.POST("/sync/preview", h.PreviewSync)
		adminRoutes.POST("/sync", h.TriggerSync)
		adminRoutes.POST("/employees/:open_id/unblock", h.UnblockEmployee)
	}
}
