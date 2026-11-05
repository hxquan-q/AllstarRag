package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// OwnerManagementHandler 负责超管的 owner 管理接口（设 owner 空间 /
// 生成 owner 邀请）。权限与业务规则在 service 层完成；本层只做参数解析、
// 错误映射、邀请 URL 组装和响应包裹。
type OwnerManagementHandler struct {
	userManagementService interfaces.UserManagementService
	config                *config.Config
}

// NewOwnerManagementHandler 注入用户管理服务。config 由容器通过
// WithConfig 补充，测试可以只传服务（邀请 URL 退化为 host-relative）。
func NewOwnerManagementHandler(
	userManagementService interfaces.UserManagementService,
) *OwnerManagementHandler {
	return &OwnerManagementHandler{userManagementService: userManagementService}
}

// WithConfig 注入配置，用于拼出完整的邀请注册链接。
func (h *OwnerManagementHandler) WithConfig(cfg *config.Config) *OwnerManagementHandler {
	h.config = cfg
	return h
}

// provisionOwnerSpaceRequest 是设 owner 空间的请求体；space_name 可选。
type provisionOwnerSpaceRequest struct {
	SpaceName string `json:"space_name"`
}

// createOwnerInviteRequest 是生成 owner 邀请的请求体，三个字段都可选。
type createOwnerInviteRequest struct {
	Email         string `json:"email"`
	SpaceName     string `json:"space_name"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// ProvisionOwnerSpace 把已有账号设为 owner（无空间则新建，有则复用）。
//
//	@Summary      设置账号为 owner
//	@Description  系统管理员把已有账号设为 owner：已有独立空间则复用，否则新建空间并成为唯一 owner
//	@Tags         系统管理
//	@Produce      json
//	@Param        user_id  path  string                     true  "用户 ID"
//	@Param        request  body  provisionOwnerSpaceRequest  false  "空间名（可选）"
//	@Success      200      {object}  map[string]interface{}
//	@Router       /system/admin/users/{user_id}/owner-space [post]
func (h *OwnerManagementHandler) ProvisionOwnerSpace(c *gin.Context) {
	ctx := c.Request.Context()

	var req provisionOwnerSpaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	targetUserID := strings.TrimSpace(c.Param("user_id"))
	if targetUserID == "" {
		_ = c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}

	tenant, err := h.userManagementService.ProvisionOwnerSpace(
		ctx, targetUserID, strings.TrimSpace(req.SpaceName),
	)
	if err != nil {
		renderManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"tenant": tenant},
	})
}

// CreateOwnerInvite 生成 owner 邀请：pending 空间 + role=owner 的分享链接。
//
//	@Summary      生成 owner 邀请链接
//	@Description  创建一个 pending 空间与 owner 角色的分享链接；接受邀请后才创建成员关系
//	@Tags         系统管理
//	@Produce      json
//	@Param        request  body  createOwnerInviteRequest  false  "邀请信息（全部可选）"
//	@Success      201      {object}  map[string]interface{}
//	@Router       /system/admin/owners/invite [post]
func (h *OwnerManagementHandler) CreateOwnerInvite(c *gin.Context) {
	ctx := c.Request.Context()

	var req createOwnerInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	result, err := h.userManagementService.CreateOwnerInvite(
		ctx,
		strings.TrimSpace(req.Email),
		strings.TrimSpace(req.SpaceName),
		req.ExpiresInDays,
	)
	if err != nil {
		renderManagementError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"invite_url": buildInviteRegisterURL(h.config, result.Token),
			"tenant_id":  result.TenantID,
			"expires_at": result.ExpiresAt,
		},
	})
}

// renderManagementError 把服务层的 *AppError 原样交给全局错误处理器，
// 保持 400/403/404/409 状态码矩阵；其他错误统一 500，不泄露内部细节。
func renderManagementError(c *gin.Context, err error) {
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		_ = c.Error(appErr)
		return
	}
	logger.Errorf(c.Request.Context(), "owner management operation failed: %v", err)
	_ = c.Error(apperrors.NewInternalServerError("owner management operation failed"))
}
