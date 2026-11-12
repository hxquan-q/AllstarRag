package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// CapabilitiesResponse 是 GET /auth/capabilities 的 data 载荷。
// 前端用它做菜单/按钮门控，真正的权限以后端逐接口校验为准（C4）。
type CapabilitiesResponse struct {
	Role          string   `json:"role"`
	IsSystemAdmin bool     `json:"is_system_admin"`
	Capabilities  []string `json:"capabilities"`
}

// GetCapabilitiesResponse 是 GET /auth/capabilities 的完整响应体，仅用于 Swagger 文档。
type GetCapabilitiesResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    CapabilitiesResponse `json:"data"`
}

// GetCapabilities 返回当前登录用户在「当前工作区」里的角色与能力集合。
//
// 能力值只来自 types.CapabilitiesFor（唯一事实源），本 handler 不做任何
// 额外的角色判断，避免前后端各写一套矩阵（AC-12b）。
//
// GetCapabilities godoc
// @Summary      获取当前用户能力
// @Description  返回当前工作区角色、是否系统管理员，以及由唯一能力矩阵投影出的能力列表。前端据此门控菜单与按钮；真正的权限由后端逐接口校验。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Success      200  {object}  handler.GetCapabilitiesResponse  "当前用户能力"
// @Failure      401  {object}  errors.AppError                 "未认证"
// @Security     Bearer
// @Router       /auth/capabilities [get]
func (h *AuthHandler) GetCapabilities(c *gin.Context) {
	ctx := c.Request.Context()

	// 路由层 middleware.Auth 是主防线；这里再查一次当前用户，保证即使
	// handler 被直接挂到无认证路由上也不会放行未登录请求。
	if _, err := h.userService.GetCurrentUser(ctx); err != nil {
		logger.Errorf(ctx, "Failed to get current user for capabilities: %v", err)
		appErr := errors.NewUnauthorizedError("Failed to get user information").WithDetails(err.Error())
		c.Error(appErr)
		return
	}

	role := types.TenantRoleFromContext(ctx)
	isSystemAdmin := types.IsSystemAdminFromContext(ctx)
	caps := types.CapabilitiesFor(role, isSystemAdmin)
	names := make([]string, 0, len(caps))
	for _, cap := range caps {
		names = append(names, string(cap))
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": CapabilitiesResponse{
			Role:          string(role),
			IsSystemAdmin: isSystemAdmin,
			Capabilities:  names,
		},
	})
}
