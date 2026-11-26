package handler

// 云之家同步管理端 HTTP 入口（T08）。
//
// 四个接口全部挂在 /api/v1/system/admin 组并由 g.SystemAdmin() 守卫；
// handler 再独立校验一次，避免只靠前端或单层中间件。
// 响应不包含 ticket、token、密码、手机号；blocked 清单只给
// open_id/姓名/部门/时间/操作人。

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// yunzhijiaOpenIDMaxLen 与 yunzhijia_employees.open_id 的 varchar(64) 对齐。
const yunzhijiaOpenIDMaxLen = 64

// YunzhijiaAdminHandler 暴露管理端同步状态与操作。
type YunzhijiaAdminHandler struct {
	svc *service.YunzhijiaAdminService
}

// NewYunzhijiaAdminHandler 构造 handler。
func NewYunzhijiaAdminHandler(
	svc *service.YunzhijiaAdminService,
) *YunzhijiaAdminHandler {
	return &YunzhijiaAdminHandler{svc: svc}
}

// GetSyncStatus godoc
// @Summary      云之家同步状态
// @Description  返回 enabled、最近一次真实同步运行摘要与 blocked 清单。
// @Description  不返回全量员工名单或手机号；异常清单随状态一并返回给系统管理员，
// @Description  其中可能包含异常员工的 user_id（仅超管可见）。
// @Tags         系统管理
// @Produce      json
// @Success      200  {object}  service.YunzhijiaAdminStatus
// @Failure      403  {object}  map[string]interface{}  "非系统管理员"
// @Failure      500  {object}  map[string]interface{}  "内部错误"
// @Router       /system/admin/yunzhijia/sync [get]
func (h *YunzhijiaAdminHandler) GetSyncStatus(c *gin.Context) {
	if !requireYunzhijiaSystemAdmin(c) {
		return
	}
	status, err := h.svc.Status(c.Request.Context())
	if err != nil {
		logger.Errorf(c.Request.Context(), "[Yunzhijia] 查询同步状态失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询同步状态失败"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// PreviewSync godoc
// @Summary      云之家同步预检
// @Description  只读抓取/校验/计数，不写员工记录与账号；enabled=false 时仍可用。
// @Tags         系统管理
// @Produce      json
// @Success      200  {object}  service.YunzhijiaPreviewResult
// @Failure      403  {object}  map[string]interface{}  "非系统管理员"
// @Failure      409  {object}  map[string]interface{}  "同步或预检进行中"
// @Failure      500  {object}  map[string]interface{}  "内部错误"
// @Router       /system/admin/yunzhijia/sync/preview [post]
func (h *YunzhijiaAdminHandler) PreviewSync(c *gin.Context) {
	if !requireYunzhijiaSystemAdmin(c) {
		return
	}
	result, err := h.svc.Preview(c.Request.Context())
	if err != nil {
		h.respondSyncError(c, "预检失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// TriggerSync godoc
// @Summary      手动触发云之家同步
// @Description  运行中返回 409；enabled=false 时仍可用于首次导入。
// @Tags         系统管理
// @Produce      json
// @Success      200  {object}  types.YunzhijiaSyncRun
// @Failure      403  {object}  map[string]interface{}  "非系统管理员"
// @Failure      409  {object}  map[string]interface{}  "同步或预检进行中"
// @Failure      500  {object}  map[string]interface{}  "内部错误"
// @Router       /system/admin/yunzhijia/sync [post]
func (h *YunzhijiaAdminHandler) TriggerSync(c *gin.Context) {
	if !requireYunzhijiaSystemAdmin(c) {
		return
	}
	run, err := h.svc.ManualSync(c.Request.Context())
	if err != nil {
		h.respondSyncError(c, "同步失败", err)
		return
	}
	c.JSON(http.StatusOK, run)
}

// UnblockEmployee godoc
// @Summary      解除云之家员工禁止重建
// @Description  清除 blocked 与 user_id；账号由下一轮同步重建，不在此立即开号。
// @Tags         系统管理
// @Produce      json
// @Param        open_id  path      string  true  "云之家员工 open_id"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}  "open_id 非法"
// @Failure      403      {object}  map[string]interface{}  "非系统管理员"
// @Failure      404      {object}  map[string]interface{}  "员工不存在或已解除"
// @Failure      500      {object}  map[string]interface{}  "内部错误"
// @Router       /system/admin/yunzhijia/employees/{open_id}/unblock [post]
func (h *YunzhijiaAdminHandler) UnblockEmployee(c *gin.Context) {
	if !requireYunzhijiaSystemAdmin(c) {
		return
	}
	openID := strings.TrimSpace(c.Param("open_id"))
	if openID == "" || len(openID) > yunzhijiaOpenIDMaxLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "open_id 不合法"})
		return
	}
	actorID, _ := types.UserIDFromContext(c.Request.Context())
	if err := h.svc.Unblock(c.Request.Context(), openID, actorID); err != nil {
		if errors.Is(err, service.ErrYunzhijiaEmployeeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "员工不存在或已解除"})
			return
		}
		logger.Errorf(c.Request.Context(), "[Yunzhijia] 解除 blocked 失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "已解除，账号将由下一轮同步重建",
	})
}

// requireYunzhijiaSystemAdmin 在 handler 内再校验一次系统管理员身份。
func requireYunzhijiaSystemAdmin(c *gin.Context) bool {
	if types.IsSystemAdminFromContext(c.Request.Context()) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "需要系统管理员权限"})
	return false
}

// respondSyncError 把单运行互斥映射为 409，其余错误按 500 处理。
func (h *YunzhijiaAdminHandler) respondSyncError(
	c *gin.Context, message string, err error,
) {
	if errors.Is(err, service.ErrSyncRunning) {
		c.JSON(http.StatusConflict, gin.H{"error": "同步或预检已在进行中"})
		return
	}
	logger.Errorf(c.Request.Context(), "[Yunzhijia] %s: %v", message, err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": message})
}
