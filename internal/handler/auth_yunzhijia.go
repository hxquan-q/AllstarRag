package handler

import (
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// yunzhijiaUnavailableMessage 与 service 层配置错误文案保持一致。
	yunzhijiaUnavailableMessage = "系统暂不可用，请联系管理员"
)

// yunzhijiaTicketPattern 是 ticket 的字符白名单与长度上限（外部输入边界）。
var yunzhijiaTicketPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

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
// @Failure      503      {object}  dto.AuthLoginResponse  "配置缺失、未启用或配置非法"
// @Router       /auth/yunzhijia/login [post]
func (h *YunzhijiaAuthHandler) Login(c *gin.Context) {
	// 配置缺失、关闭或非法时 fail-closed：不调用登录服务，也不签发 token。
	if h.cfg == nil || !h.cfg.Enabled || h.cfg.Validate() != nil {
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
	if !yunzhijiaTicketPattern.MatchString(ticket) {
		c.JSON(400, gin.H{"success": false, "message": "请求参数不合法"})
		return
	}

	resp, err := h.loginSvc.Login(c.Request.Context(), ticket)
	if err != nil {
		// 只记录错误分类；ticket 与员工姓名均不在 err 文本中。
		logger.Warnf(c.Request.Context(), "[Yunzhijia] 登录失败: %v", err)
		_ = c.Error(errors.NewInternalServerError("登录失败，请稍后再试"))
		return
	}
	if !resp.Success {
		logger.Warnf(c.Request.Context(), "[Yunzhijia] 登录被拒绝: %s", resp.Message)
		c.JSON(401, dto.NewAuthLoginResponse(resp))
		return
	}
	c.JSON(200, dto.NewAuthLoginResponse(resp))
}
