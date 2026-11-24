package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// yunzhijiaTicketMaxLength 是登录 ticket 的合理上限；明显超长直接 400。
	yunzhijiaTicketMaxLength = 4096
	// yunzhijiaUnavailableMessage 与 service 层配置错误文案保持一致。
	yunzhijiaUnavailableMessage = "系统暂不可用，请联系管理员"
)

// YunzhijiaAuthHandler 暴露云之家 ticket 免登录 HTTP 入口。
type YunzhijiaAuthHandler struct {
	cfg      *config.YunzhijiaSSOConfig
	loginSvc interfaces.YunzhijiaLoginService
}

// NewYunzhijiaAuthHandler 构造 handler。
func NewYunzhijiaAuthHandler(
	cfg *config.YunzhijiaSSOConfig, loginSvc interfaces.YunzhijiaLoginService,
) *YunzhijiaAuthHandler {
	return &YunzhijiaAuthHandler{cfg: cfg, loginSvc: loginSvc}
}

// Login godoc
// @Summary      云之家 ticket 免登录
// @Description  用云之家 ticket 换取员工姓名并登录；ticket 只接受 POST body。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        request  body      dto.YunzhijiaLoginRequest  true  "云之家 ticket"
// @Success      200      {object}  dto.AuthLoginResponse
// @Failure      400      {object}  errors.AppError  "ticket 为空、超长或 JSON 非法"
// @Failure      401      {object}  dto.AuthLoginResponse  "业务拒绝"
// @Failure      429      {object}  errors.AppError  "too many requests"
// @Failure      500      {object}  errors.AppError  "内部错误"
// @Failure      503      {object}  dto.AuthLoginResponse  "配置缺失或未启用"
// @Router       /auth/yunzhijia/login [post]
func (h *YunzhijiaAuthHandler) Login(c *gin.Context) {
	// 配置缺失或关闭时 fail-closed：不调用登录服务，也不签发 token。
	if h == nil || h.cfg == nil || !h.cfg.Enabled {
		c.JSON(503, dto.AuthLoginResponse{
			Success: false,
			Message: yunzhijiaUnavailableMessage,
		})
		return
	}

	var req dto.YunzhijiaLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "请求参数不合法"})
		return
	}
	ticket := strings.TrimSpace(req.Ticket)
	if ticket == "" || len(ticket) > yunzhijiaTicketMaxLength {
		c.JSON(400, gin.H{"success": false, "message": "请求参数不合法"})
		return
	}

	resp, err := h.loginSvc.Login(c.Request.Context(), ticket)
	if err != nil {
		_ = c.Error(errors.NewInternalServerError("登录失败，请稍后再试"))
		return
	}
	if resp == nil {
		_ = c.Error(errors.NewInternalServerError("登录失败，请稍后再试"))
		return
	}
	if !resp.Success {
		c.JSON(401, dto.NewAuthLoginResponse(resp))
		return
	}
	c.JSON(200, dto.NewAuthLoginResponse(resp))
}
