package handler

import (
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// 本文件是 T35 平台级账号操作的 HTTP 入口：/system/admin/users/:user_id/*
// 不依赖成员关系，因此 handler 不经过 memberService（构造函数里可以为 nil）。
// 权限守卫在路由组（g.SystemAdmin）与服务层各证一次；本层只做参数解析、
// 错误映射和最小响应组装。

// PlatformBanUser 平台封禁任意账号。
//
//	@Summary      平台封禁账号
//	@Description  系统管理员按 user_id 封禁任意账号（不依赖当前空间成员关系）：置 is_active=false 并吊销全部 token，写 tenant_id=0 的系统审计
//	@Tags         平台用户管理
//	@Produce      json
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberStatusChangeRequest  false  "封禁原因（可选）"
//	@Success      200      {object}  map[string]interface{}  "最小账号摘要（不含 preferences / 密码哈希 / 成员关系）"
//	@Failure      400      {object}  map[string]interface{}  "请求体非法"
//	@Failure      403      {object}  map[string]interface{}  "非系统管理员、操作自己或目标是最后一名活跃系统管理员"
//	@Failure      404      {object}  map[string]interface{}  "目标账号不存在"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
//	@Security     Bearer
//	@Router       /system/admin/users/{user_id}/ban [post]
func (h *UserManagementHandler) PlatformBanUser(c *gin.Context) {
	h.changePlatformStatus(c, true)
}

// PlatformUnbanUser 平台解封任意账号。
//
//	@Summary      平台解封账号
//	@Description  系统管理员按 user_id 解封任意账号：恢复登录能力，不恢复历史 token，不受「最近一次 ban 必须在当前空间」限制
//	@Tags         平台用户管理
//	@Produce      json
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberStatusChangeRequest  false  "解封原因（可选）"
//	@Success      200      {object}  map[string]interface{}  "最小账号摘要（不含 preferences / 密码哈希 / 成员关系）"
//	@Failure      400      {object}  map[string]interface{}  "请求体非法"
//	@Failure      403      {object}  map[string]interface{}  "非系统管理员"
//	@Failure      404      {object}  map[string]interface{}  "目标账号不存在"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
//	@Security     Bearer
//	@Router       /system/admin/users/{user_id}/unban [post]
func (h *UserManagementHandler) PlatformUnbanUser(c *gin.Context) {
	h.changePlatformStatus(c, false)
}

// changePlatformStatus 是平台封禁/解封的共用流程。先解析请求体，保证
// 守卫放行后的非法请求直接 400，不会触达服务层。
func (h *UserManagementHandler) changePlatformStatus(c *gin.Context, ban bool) {
	ctx := c.Request.Context()

	var req memberStatusChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	targetUserID := strings.TrimSpace(c.Param("user_id"))
	if targetUserID == "" {
		_ = c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}

	reason := strings.TrimSpace(req.Reason)
	var (
		user *types.User
		err  error
	)
	if ban {
		user, err = h.userManagementService.PlatformBanUser(ctx, targetUserID, reason)
	} else {
		user, err = h.userManagementService.PlatformUnbanUser(ctx, targetUserID, reason)
	}
	if err != nil {
		h.renderServiceError(c, err)
		return
	}

	// 平台接口不保证成员关系，响应只回最小账号摘要：不回 preferences
	// （含 last_active_tenant_id / bound_agent_ids_by_tenant 等跨空间数据），
	// 也不回 member。
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"user": newMemberUserSummary(user)},
	})
}

// PlatformPurgeUser 平台注销（彻底删除 / 墓碑化）任意账号。
//
//	@Summary      平台注销账号
//	@Description  系统管理员按 user_id 墓碑化任意账号（跨空间/无成员关系均可）：清空 PII、清理个人数据并写系统审计；owner 需先交接、系统管理员需先撤权，不可恢复
//	@Tags         平台用户管理
//	@Produce      json
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberPurgeRequest  true  "确认邮箱与原因（原因可选）"
//	@Success      200      {object}  map[string]interface{}  "仅包含被处理账号 id"
//	@Failure      400      {object}  map[string]interface{}  "请求体非法或 confirm_email 与目标邮箱不一致"
//	@Failure      403      {object}  map[string]interface{}  "非系统管理员、操作自己、目标仍有特权或 owner 未交接"
//	@Failure      404      {object}  map[string]interface{}  "目标账号不存在（含重复删除）"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
//	@Security     Bearer
//	@Router       /system/admin/users/{user_id}/purge [post]
func (h *UserManagementHandler) PlatformPurgeUser(c *gin.Context) {
	ctx := c.Request.Context()

	var req memberPurgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	targetUserID := strings.TrimSpace(c.Param("user_id"))
	if targetUserID == "" {
		_ = c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}

	if err := h.userManagementService.PlatformPurgeUser(
		ctx, targetUserID,
		strings.TrimSpace(req.ConfirmEmail), strings.TrimSpace(req.Reason),
	); err != nil {
		h.renderServiceError(c, err)
		return
	}

	// 目标已被墓碑化，响应只回 id，不回显邮箱等 PII。
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"user_id": targetUserID},
	})
}
