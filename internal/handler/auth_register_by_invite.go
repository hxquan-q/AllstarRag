package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// registerByInviteRequest is the body for /auth/register-by-invite.
//
// Email is collected from the invitee themselves: share-link rows have
// no specific invitee, so we cannot pre-bind any address. The token
// IS the authorisation; the email is whatever account the invitee
// wants to register.
type registerByInviteRequest struct {
	Token    string `json:"token"    binding:"required"`
	Email    string `json:"email"    binding:"required,email,max=320"`
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required,min=6"`
}

// invitationLookupResponse is the public projection of a share-link
// row returned by /auth/invitations/lookup. Kept narrow on purpose:
// just enough for the registration page to render context ("X invited
// you to Y") without exposing inviter audit fields.
type invitationLookupResponse struct {
	TenantID   uint64           `json:"tenant_id"`
	TenantName string           `json:"tenant_name,omitempty"`
	Role       types.TenantRole `json:"role"`
	ExpiresAt  string           `json:"expires_at"`
}

// invitationLookupRequest carries the token in the request body
// instead of the URL path: GET /auth/invitations/:token would have
// landed the plaintext token in access logs, browser history, and
// tracing spans. POST + body keeps the token out of those surfaces
// at the cost of "this endpoint reads, why is it POST" — worth it
// because the token is the sole authorisation for registration.
type invitationLookupRequest struct {
	Token string `json:"token" binding:"required"`
}

// replyInvitationGone 统一输出「链接无效 / 已撤销 / 名额已满」的 410。
// 调用方不得暴露具体失效原因，避免泄露 token 曾经的状态。
func replyInvitationGone(c *gin.Context) {
	_ = c.Error(&apperrors.AppError{
		Code:     apperrors.ErrNotFound,
		Message:  "invitation link is invalid or has been revoked",
		HTTPCode: http.StatusGone,
	})
}

// LookupInvitationByToken godoc
// @Summary      解析共享邀请链接 token
// @Description  根据邀请链接中的 token 返回邀请上下文（空间名 / 角色 / 过期时间），
// @Description  供注册页展示。无认证；token 无效或被撤销返回 410。
// @Description  使用 POST + body 而非 GET + path，避免 token 落入访问日志 / 浏览器历史 / tracing。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        request  body      invitationLookupRequest  true  "邀请 token"
// @Success      200      {object}  invitationLookupResponse
// @Failure      410      {object}  apperrors.AppError  "链接无效或已撤销"
// @Router       /auth/invitations/lookup [post]
func (h *AuthHandler) LookupInvitationByToken(c *gin.Context) {
	ctx := c.Request.Context()

	if h.invitationSvc == nil {
		c.Error(apperrors.NewInternalServerError("invitation service unavailable"))
		return
	}

	var req invitationLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("token is required").WithDetails(err.Error()))
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		c.Error(apperrors.NewValidationError("token is required"))
		return
	}

	inv, err := h.invitationSvc.LookupByToken(ctx, token)
	if err != nil {
		// Collapse "unknown / expired / revoked" into 410 to avoid
		// leaking which slot a stolen token used to occupy.
		replyInvitationGone(c)
		return
	}
	if inv.IsRegistrationFull() {
		// 名额已满与过期 / 撤销同语义：页面统一展示「链接已失效」。
		replyInvitationGone(c)
		return
	}

	resp := invitationLookupResponse{
		TenantID:  inv.TenantID,
		Role:      inv.Role,
		ExpiresAt: inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if tenant, terr := h.tenantService.GetTenantByID(ctx, inv.TenantID); terr == nil && tenant != nil {
		resp.TenantName = tenant.Name
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    resp,
	})
}

// RegisterByInvite godoc
// @Summary      使用共享链接注册
// @Description  通过共享邀请链接 token 完成注册，仅 self_serve / invite_register 模式允许。
// @Description  注册者自填邮箱（与 token 不绑定）；注册成功后自动加入对应空间。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        request  body      registerByInviteRequest  true  "邀请注册请求"
// @Success      201      {object}  types.LoginResponse
// @Failure      400      {object}  apperrors.AppError  "请求参数错误"
// @Failure      403      {object}  apperrors.AppError  "注册功能已禁用"
// @Failure      409      {object}  apperrors.AppError  "邮箱已注册"
// @Failure      410      {object}  apperrors.AppError  "链接无效或已撤销"
// @Router       /auth/register-by-invite [post]
//
// RegisterByInvite requires both an enabled registration mode and a valid token.
func (h *AuthHandler) RegisterByInvite(c *gin.Context) {
	ctx := c.Request.Context()

	mode := h.resolveRegistrationMode(ctx)
	if mode != config.AuthRegistrationModeSelfServe && mode != config.AuthRegistrationModeInviteRegister {
		_ = c.Error(apperrors.NewForbiddenError("Registration is disabled"))
		return
	}

	if h.invitationSvc == nil {
		c.Error(apperrors.NewInternalServerError("invitation service unavailable"))
		return
	}

	var req registerByInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("Invalid registration parameters").WithDetails(err.Error()))
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Username = strings.TrimSpace(req.Username)
	// Password is intentionally NOT sanitized: SanitizeForLog strips
	// \n, \r, \t and control chars to make a string safe to write into
	// a log line — applying it to a real password would silently
	// rewrite the stored credential and lock the user out. Passwords
	// must never be logged, so they don't need that defence here.
	if req.Token == "" || req.Email == "" || req.Username == "" || req.Password == "" {
		c.Error(apperrors.NewValidationError("token, email, username and password are required"))
		return
	}

	inv, err := h.invitationSvc.LookupByToken(ctx, req.Token)
	if err != nil {
		replyInvitationGone(c)
		return
	}

	// If the email already has an account we shouldn't quietly succeed
	// here: register would fail with a duplicate-email error, but the
	// share-link flow is "create new account + join tenant", not "join
	// my existing account". Surface 409 so the SPA can prompt them to
	// log in instead, and the invitation can be applied to their
	// existing account via /me/invitations once we wire that path.
	if existing, _ := h.userService.GetUserByEmail(ctx, req.Email); existing != nil {
		c.Error(apperrors.NewConflictError(
			"this email already has an account; please log in to join the workspace"))
		return
	}

	if err := service.ValidatePasswordPolicy(req.Password, h.complexPasswordEnabled(ctx)); err != nil {
		logger.Error(ctx, "Invalid password policy")
		_ = c.Error(apperrors.NewValidationError(err.Error()))
		return
	}

	// 先预占名额再建号：满额 / 过期 / 已撤销时 reserve 返回
	// ErrInvitationTokenInvalid，对外仍统一 410，不暴露具体原因。
	// 预占后的任何失败都通过 defer 释放，避免名额泄漏。
	if err := h.invitationSvc.ReserveRegistrationQuota(ctx, inv.ID); err != nil {
		logger.Errorf(ctx, "register-by-invite: reserve quota failed for invitation %d: %v", inv.ID, err)
		replyInvitationGone(c)
		return
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// 用独立上下文释放：请求上下文可能已被取消，释放仍须完成。
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if releaseErr := h.invitationSvc.ReleaseRegistrationQuota(releaseCtx, inv.ID); releaseErr != nil {
			logger.Errorf(releaseCtx, "register-by-invite: release quota failed for invitation %d: %v",
				inv.ID, releaseErr)
		}
	}()

	user, err := h.userService.Register(ctx, &types.RegisterRequest{
		Username:           req.Username,
		Email:              req.Email,
		Password:           req.Password,
		TenantProvisioning: types.TenantProvisioningTenantless,
	})
	if err != nil {
		logger.Errorf(ctx, "register-by-invite: user create failed for %s: %v",
			secutils.SanitizeForLog(req.Email), err)
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}

	// The invited tenant becomes the user's initial/default tenant. No
	user.TenantID = inv.TenantID
	if err := h.userService.UpdateUser(ctx, user); err != nil {
		logger.Errorf(ctx, "register-by-invite: failed to set home tenant for user %s: %v", user.ID, err)
		_ = h.userService.DeleteUser(ctx, user.ID)
		c.Error(apperrors.NewInternalServerError("failed to finalise invited account").WithDetails(err.Error()))
		return
	}

	if _, err := h.invitationSvc.AcceptByToken(ctx, req.Token, user.ID); err != nil {
		// Race: link was revoked between Lookup and Accept. Keep the new
		// account, but restore it to tenantless so it does not point at a
		// tenant for which no membership was created. If even that repair
		// fails, remove the half-provisioned identity.
		logger.Errorf(ctx, "register-by-invite: accept failed for user %s: %v", user.ID, err)
		user.TenantID = 0
		if rollbackErr := h.userService.UpdateUser(ctx, user); rollbackErr != nil {
			logger.Errorf(ctx, "register-by-invite: failed to restore tenantless user %s: %v", user.ID, rollbackErr)
			_ = h.userService.DeleteUser(ctx, user.ID)
		}
		c.Error(&apperrors.AppError{
			Code:     apperrors.ErrNotFound,
			Message:  "invitation link is no longer valid; please log in to your new account",
			HTTPCode: http.StatusGone,
		})
		return
	}
	// owner 邀请创建的 pending 空间在第一个成员接受后激活（T04-2）。
	activatePendingTenant(ctx, h.tenantService, inv.TenantID)

	// 账号与成员关系都已建立，视为一次成功注册：后续签发 token
	// 失败不再释放名额。
	committed = true

	accessToken, refreshToken, err := h.userService.GenerateTokens(ctx, user)
	if err != nil {
		logger.Errorf(ctx, "register-by-invite: token generation failed for user %s: %v", user.ID, err)
		c.Error(apperrors.NewInternalServerError("token generation failed").WithDetails(err.Error()))
		return
	}

	tenant, _ := h.tenantService.GetTenantByID(ctx, inv.TenantID)
	c.JSON(http.StatusCreated, dto.NewAuthLoginResponse(&types.LoginResponse{
		Success:      true,
		Message:      "Registration successful",
		User:         user,
		ActiveTenant: tenant,
		Memberships: []types.Membership{{
			TenantID:   inv.TenantID,
			TenantName: tenantNameOrEmpty(tenant),
			Role:       inv.Role,
		}},
		Token:        accessToken,
		RefreshToken: refreshToken,
	}))
}
