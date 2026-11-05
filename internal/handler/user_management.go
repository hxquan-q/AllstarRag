package handler

import (
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// UserManagementHandler 负责用户管理页的成员状态变更接口
// （封禁 / 解封）。权限矩阵与目标保护在 service 层完成；本层只做
// 参数解析、错误映射和响应组装。
type UserManagementHandler struct {
	userManagementService interfaces.UserManagementService
	memberService         interfaces.TenantMemberService
}

// NewUserManagementHandler 注入用户管理服务与成员服务（用于回填成员行）。
func NewUserManagementHandler(
	userManagementService interfaces.UserManagementService,
	memberService interfaces.TenantMemberService,
) *UserManagementHandler {
	return &UserManagementHandler{
		userManagementService: userManagementService,
		memberService:         memberService,
	}
}

// memberStatusChangeRequest 是封禁/解封接口的请求体。
type memberStatusChangeRequest struct {
	Reason string `json:"reason"`
}

// memberPurgeRequest 是彻底删除接口的请求体。confirm_email 必须与目标
// 当前邮箱一致；不匹配由服务层返回 400。
type memberPurgeRequest struct {
	ConfirmEmail string `json:"confirm_email"`
	Reason       string `json:"reason"`
}

// BanMember 封禁空间成员。
//
//	@Summary      封禁空间成员
//	@Description  将目标账号置为不可登录并吊销其全部 token，保留成员关系；owner 不能操作 owner/超管
//	@Tags         空间成员
//	@Produce      json
//	@Param        id       path  string  true  "空间 ID"
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberStatusChangeRequest  false  "封禁原因（可选）"
//	@Success      200      {object}  map[string]interface{}
//	@Router       /tenants/{id}/members/{user_id}/ban [post]
func (h *UserManagementHandler) BanMember(c *gin.Context) {
	h.changeMemberStatus(c, true)
}

// UnbanMember 解封空间成员。
//
//	@Summary      解封空间成员
//	@Description  恢复目标账号的登录能力，不恢复历史 token；普通 owner 只能解封在本空间被封禁的账号
//	@Tags         空间成员
//	@Produce      json
//	@Param        id       path  string  true  "空间 ID"
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberStatusChangeRequest  false  "解封原因（可选）"
//	@Success      200      {object}  map[string]interface{}
//	@Router       /tenants/{id}/members/{user_id}/unban [post]
func (h *UserManagementHandler) UnbanMember(c *gin.Context) {
	h.changeMemberStatus(c, false)
}

// changeMemberStatus 是封禁/解封的共用流程。先解析请求体，确保路由
// 守卫放行后的非法请求直接得到 400，不会触达下游服务。
func (h *UserManagementHandler) changeMemberStatus(c *gin.Context, ban bool) {
	ctx := c.Request.Context()

	var req memberStatusChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
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
		user, err = h.userManagementService.BanUser(ctx, tenantID, targetUserID, reason)
	} else {
		user, err = h.userManagementService.UnbanUser(ctx, tenantID, targetUserID, reason)
	}
	if err != nil {
		h.renderServiceError(c, err)
		return
	}

	// 成员行用于前端就地刷新角色/状态；查询失败不能被吞掉后返回
	// 200 + 空 member，否则前端会误显示"已封禁/已解封"。
	member, err := h.memberService.GetMembership(ctx, targetUserID, tenantID)
	if err != nil {
		logger.Errorf(ctx, "GetMembership failed after status change: user=%s tenant=%d err=%v",
			targetUserID, tenantID, err)
		_ = c.Error(apperrors.NewInternalServerError("failed to look up membership"))
		return
	}
	if member == nil {
		_ = c.Error(apperrors.NewNotFoundError("membership not found"))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user":   user,
			"member": member,
		},
	})
}

// PurgeMember 彻底删除（墓碑化）空间成员。
//
//	@Summary      彻底删除空间成员
//	@Description  墓碑化目标账号：清空 PII、清理成员/邀请/token/个人数据并写审计；资产保留。不可恢复
//	@Tags         空间成员
//	@Produce      json
//	@Param        id       path  string  true  "空间 ID"
//	@Param        user_id  path  string  true  "用户 ID"
//	@Param        request  body  memberPurgeRequest  true  "确认邮箱与原因（原因可选）"
//	@Success      200      {object}  map[string]interface{}
//	@Router       /tenants/{id}/members/{user_id}/purge [post]
func (h *UserManagementHandler) PurgeMember(c *gin.Context) {
	ctx := c.Request.Context()

	var req memberPurgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	targetUserID := strings.TrimSpace(c.Param("user_id"))
	if targetUserID == "" {
		_ = c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}

	if err := h.userManagementService.PurgeUser(
		ctx, tenantID, targetUserID,
		strings.TrimSpace(req.ConfirmEmail), strings.TrimSpace(req.Reason),
	); err != nil {
		h.renderServiceError(c, err)
		return
	}

	// 成员关系已在墓碑化事务里软删，无法也不应回填 member；只返回
	// 被处理的目标 id，响应不含邮箱等 PII。
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"user_id": targetUserID},
	})
}

// transferOwnerRequest 是空间交接接口的请求体。from_user_id 可省略，
// 服务层按"调用者自己"处理；to_user_id 必填。
type transferOwnerRequest struct {
	FromUserID string `json:"from_user_id"`
	ToUserID   string `json:"to_user_id"`
}

// createCoOwnerRequest 是 owner 新建 co-owner 账号的请求体。
type createCoOwnerRequest struct {
	Username string  `json:"username"`
	Email    string  `json:"email"`
	Password *string `json:"password"`
}

// TransferOwner 交接空间 owner 身份。
//
//	@Summary      交接空间 owner
//	@Description  把 owner 身份交给本空间 active 成员；owner 只能传自己，系统管理员可传任意 owner
//	@Tags         空间成员
//	@Produce      json
//	@Param        id       path  string               true  "空间 ID"
//	@Param        request  body  transferOwnerRequest  true  "交接目标（from 可选）"
//	@Success      200      {object}  map[string]interface{}
//	@Router       /tenants/{id}/transfer-owner [post]
func (h *UserManagementHandler) TransferOwner(c *gin.Context) {
	ctx := c.Request.Context()

	var req transferOwnerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	toUserID := strings.TrimSpace(req.ToUserID)
	if toUserID == "" {
		_ = c.Error(apperrors.NewValidationError("to_user_id is required"))
		return
	}

	if err := h.userManagementService.TransferOwner(
		ctx, tenantID, strings.TrimSpace(req.FromUserID), toUserID,
	); err != nil {
		renderManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// CreateCoOwnerAccount 由 owner 新建 co-owner 账号。
//
//	@Summary      新建 co-owner 账号
//	@Description  创建一个 tenantless 账号并加入当前空间，角色为 owner；入组失败会回滚账号
//	@Tags         空间成员
//	@Produce      json
//	@Param        id       path  string                true  "空间 ID"
//	@Param        request  body  createCoOwnerRequest  true  "账号信息"
//	@Success      201      {object}  map[string]interface{}
//	@Router       /tenants/{id}/owners/create [post]
func (h *UserManagementHandler) CreateCoOwnerAccount(c *gin.Context) {
	ctx := c.Request.Context()

	var req createCoOwnerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}

	user, err := h.userManagementService.CreateCoOwnerAccount(
		ctx, tenantID, &interfaces.CreateCoOwnerRequest{
			Username: req.Username,
			Email:    req.Email,
			Password: req.Password,
		},
	)
	if err != nil {
		renderManagementError(c, err)
		return
	}
	// types.User 的 PasswordHash 带 json:"-"，不会泄露哈希。
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data":    gin.H{"user": user},
	})
}

// ListUsers 返回用户管理页的成员列表（owner / 系统管理员）。
//
//	@Summary      列出用户管理成员
//	@Description  返回空间内 active 成员（含被封禁账号），支持角色过滤、邮箱/用户名搜索与分页；role_counts 为整空间目录计数
//	@Tags         空间成员
//	@Produce      json
//	@Param        id         path   string  true   "空间 ID"
//	@Param        role       query  string  false  "角色筛选：viewer/contributor/admin/owner，缺省为全部"
//	@Param        q          query  string  false  "按邮箱/用户名模糊筛选"
//	@Param        page       query  int     false  "页码（从 1 起）"  default(1)
//	@Param        page_size  query  int     false  "每页数量（最大 100）"  default(20)
//	@Success      200  {object}  map[string]interface{}
//	@Security     Bearer
//	@Router       /tenants/{id}/user-management [get]
func (h *UserManagementHandler) ListUsers(c *gin.Context) {
	ctx := c.Request.Context()

	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}

	role := types.TenantRole(strings.TrimSpace(c.Query("role")))
	if role != "" && !role.IsValid() {
		_ = c.Error(apperrors.NewValidationError(
			"role must be one of viewer, contributor, admin, owner"))
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	page, pageSize, ok := parseListPagination(c)
	if !ok {
		return
	}

	result, err := h.userManagementService.ListUsers(ctx, tenantID, role, q, page, pageSize)
	if err != nil {
		h.renderServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items":       result.Items,
			"total":       result.Total,
			"role_counts": result.RoleCounts,
		},
	})
}

// renderServiceError 把服务层的 *AppError 原样交给全局错误处理器，
// 保持 403/404/400 状态码矩阵；其他错误统一 500，不泄露内部细节。
func (h *UserManagementHandler) renderServiceError(c *gin.Context, err error) {
	renderManagementError(c, err)
}
