package handler

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// frontendBaseURLFor returns the externally-visible SPA origin used to
// compose absolute share-link URLs. Lookup order:
//  1. config.FrontendBaseURL (set in YAML or via ${FRONTEND_BASE_URL})
//  2. FRONTEND_BASE_URL env at request time (so an operator can roll
//     the value out without restarting the server)
//  3. empty — handler emits a host-relative URL ("/register?token=…")
//     and the SPA resolves it against the current origin.
//
// Trailing slashes are stripped so callers can append "/register?…"
// cleanly.
func frontendBaseURLFor(cfg *config.Config) string {
	candidate := ""
	if cfg != nil {
		candidate = strings.TrimSpace(cfg.FrontendBaseURL)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(os.Getenv("FRONTEND_BASE_URL"))
	}
	return strings.TrimRight(candidate, "/")
}

// buildInviteRegisterURL composes the registration URL Owners hand
// to invitees. Plaintext token is URL-safe by construction (base64url)
// so no extra escaping is required.
func buildInviteRegisterURL(cfg *config.Config, plainToken string) string {
	if plainToken == "" {
		return ""
	}
	return frontendBaseURLFor(cfg) + "/register?token=" + plainToken
}

// createInviteLinkRequest is the body for POST /tenants/:id/invite-links.
// role 决定被邀请人加入后的角色；message 是可选备注。
// max_registrations 限制该链接最多注册多少人：留空表示不限，填写须为 1–1000。
// expires_at 是该链接的有效期：留空走服务端默认 7 天，填写必须晚于当前时间。
type createInviteLinkRequest struct {
	Role             types.TenantRole `json:"role"              binding:"required"`
	Message          string           `json:"message"`
	MaxRegistrations *int             `json:"max_registrations" binding:"omitempty,min=1,max=1000"`
	ExpiresAt        *time.Time       `json:"expires_at"`
}

// CreateInviteLink godoc
// @Summary      生成共享邀请链接
// @Description  生成一条多次使用的共享邀请链接：谁拿到链接谁就能注册并加入当前空间。
// @Description  可通过 max_registrations 限制该链接最多注册人数（1–1000，留空不限），
// @Description  通过 expires_at 指定有效期（RFC3339，必须晚于当前时间，留空默认 7 天）。
// @Description  链接创建后不可修改配额与有效期，但可随时撤销。
// @Tags         空间邀请
// @Accept       json
// @Produce      json
// @Param        id       path  string                   true  "空间 ID"
// @Param        request  body  createInviteLinkRequest  true  "共享链接配置"
// @Success      201  {object}  handler.DataResponse{data=types.TenantInvitationResponse}
// @Failure      400  {object}  apperrors.AppError  "请求参数错误（人数非法或有效期已过）"
// @Failure      500  {object}  apperrors.AppError  "创建后补写配额/有效期失败"
// @Security     Bearer
// @Router       /tenants/{id}/invite-links [post]
func (h *TenantInvitationHandler) CreateInviteLink(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	var req createInviteLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	if !req.Role.IsValid() {
		c.Error(apperrors.NewValidationError("invalid role"))
		return
	}
	// T05：分享链接只分配 admin / viewer。
	if !service.IsAssignableMemberRole(req.Role) {
		c.Error(apperrors.NewForbiddenError(
			"role must be admin or viewer; owner is assigned via provisioning or handover"))
		return
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		_ = c.Error(apperrors.NewValidationError("expires_at must be in the future"))
		return
	}

	caller, _ := types.UserIDFromContext(ctx)
	var invitedBy *string
	if caller != "" && !types.IsSyntheticUserID(caller) {
		invitedBy = &caller
	}

	inv, _, err := h.invitationService.CreateShareLink(ctx, tenantID, req.Role, invitedBy, req.Message)
	if err != nil {
		if errors.Is(err, service.ErrAPIKeyCannotAssignOwner) {
			c.Error(apperrors.NewForbiddenError(err.Error()))
			return
		}
		if errors.Is(err, service.ErrInvitationRoleNotAssignable) {
			c.Error(apperrors.NewValidationError(err.Error()))
			return
		}
		logger.Errorf(ctx, "CreateShareLink failed: tenant=%d err=%v", tenantID, err)
		c.Error(apperrors.NewInternalServerError("failed to create share link").WithDetails(err.Error()))
		return
	}
	// 创建成功后补写配额/有效期；任选项为空时不写库。
	if req.MaxRegistrations != nil || req.ExpiresAt != nil {
		if err := h.invitationService.ApplyShareLinkOptions(
			ctx, inv.ID, req.MaxRegistrations, req.ExpiresAt,
		); err != nil {
			logger.Errorf(ctx, "ApplyShareLinkOptions failed: tenant=%d inv=%d err=%v", tenantID, inv.ID, err)
			// 补写失败时撤销刚创建的孤儿链接，避免 owner 把「不限人数」的默认链接
			// 误当成刚请求的限人链接使用；撤销失败只记日志，仍按原错误返回 500。
			if revokeErr := h.invitationService.Revoke(ctx, inv.ID); revokeErr != nil {
				logger.Errorf(ctx, "revoke orphan share link failed: tenant=%d inv=%d err=%v",
					tenantID, inv.ID, revokeErr)
			}
			_ = c.Error(apperrors.NewInternalServerError("failed to apply share link options").WithDetails(err.Error()))
			return
		}
		// 同步内存对象，保证响应与刚写入的选项一致。
		inv.MaxRegistrations = req.MaxRegistrations
		if req.ExpiresAt != nil {
			inv.ExpiresAt = *req.ExpiresAt
		}
	}
	usersByID := map[string]*types.User{}
	if invitedBy != nil {
		if iu, lookupErr := h.userService.GetUserByID(ctx, *invitedBy); lookupErr == nil && iu != nil {
			usersByID[iu.ID] = iu
		}
	}
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data":    h.projectInvitationWithLink(inv, usersByID, nil),
	})
}
