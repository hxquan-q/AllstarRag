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
//	@Failure      400      {object}  map[string]interface{}  "请求体非法"
//	@Failure      403      {object}  map[string]interface{}  "无权限或目标保护（不能操作自己 / owner / 系统管理员 / 跨空间超管）"
//	@Failure      404      {object}  map[string]interface{}  "目标不是本空间 active 成员"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
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
//	@Failure      400      {object}  map[string]interface{}  "请求体非法"
//	@Failure      403      {object}  map[string]interface{}  "无权限、目标保护或跨租户解封需超管"
//	@Failure      404      {object}  map[string]interface{}  "目标不是本空间 active 成员"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
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
//	@Failure      400      {object}  map[string]interface{}  "请求体非法或 confirm_email 与目标邮箱不一致"
//	@Failure      403      {object}  map[string]interface{}  "无权限、目标保护、owner 未交接或跨空间账号需超管"
//	@Failure      404      {object}  map[string]interface{}  "目标不是本空间 active 成员（含重复删除）"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
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
//	@Failure      400      {object}  map[string]interface{}  "请求体非法、缺少 to_user_id 或 from/to 相同"
//	@Failure      403      {object}  map[string]interface{}  "非本空间 owner、系统管理员以外不能代他人交接"
//	@Failure      404      {object}  map[string]interface{}  "源 owner 或目标成员关系不存在"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
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
//	@Failure      400      {object}  map[string]interface{}  "用户名/邮箱/密码缺失或非法"
//	@Failure      403      {object}  map[string]interface{}  "非本空间 owner 或系统管理员"
//	@Failure      409      {object}  map[string]interface{}  "邮箱/用户名已注册或已是本空间成员"
//	@Failure      500      {object}  map[string]interface{}  "内部错误"
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

// UserManagementListData 是用户管理列表响应的 data 部分，仅用于 Swagger 文档。
// role_counts 始终包含 viewer/contributor/admin/owner 四个键。
type UserManagementListData struct {
	Items      []interfaces.UserManagementListItem `json:"items"`
	Total      int64                               `json:"total"`
	RoleCounts map[string]int64                    `json:"role_counts"`
}

// UserManagementListResponse 是 GET /tenants/{id}/user-management 的完整响应体，
// 仅用于 Swagger 文档，运行时仍由 gin.H 组装，字段与此一致。
type UserManagementListResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    UserManagementListData `json:"data"`
}

// ListUsers 返回用户管理页的成员列表（owner / 系统管理员）。
//
//	@Summary      列出用户管理成员
//	@Description  返回空间内 active 成员（含被封禁账号），支持角色过滤、邮箱/用户名搜索与分页；role_counts 为整空间目录计数；scope=other/all（系统管理员专属）切换到跨空间多空间聚合视图，memberships 列出各空间成员关系
//	@Tags         空间成员
//	@Produce      json
//	@Param        id         path   string  false  "空间 ID"
//	@Param        scope      query  string  false  "空间筛选：current（默认，仅本空间）/other（其它空间有成员关系的用户）/all（全平台用户），other/all 仅系统管理员"  default(current)
//	@Param        role       query  string  false  "角色筛选：viewer/contributor/admin/owner，缺省为全部；多空间视图匹配任意空间的成员关系角色"
//	@Param        q          query  string  false  "按邮箱/用户名模糊筛选"
//	@Param        page       query  int     false  "页码（从 1 起）"  default(1)
//	@Param        page_size  query  int     false  "每页数量（最大 100）"  default(20)
//	@Success      200  {object}  handler.UserManagementListResponse  "成员列表"
//	@Failure      400  {object}  map[string]interface{}  "role/scope 非法或分页参数非法"
//	@Failure      403  {object}  map[string]interface{}  "非本空间 owner 或系统管理员；scope=other/all 非系统管理员"
//	@Failure      500  {object}  map[string]interface{}  "内部错误"
//	@Security     Bearer
//	@Router       /tenants/{id}/user-management [get]
func (h *UserManagementHandler) ListUsers(c *gin.Context) {
	ctx := c.Request.Context()

	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}

	// scope 精确匹配（不宽容大小写）：缺省与 current 走旧单空间路径
	// （现有调用方零破坏）；other/all 走多空间路径；非法值在触达服务层
	// 之前直接 400。
	scope := interfaces.UserManagementListScope(strings.TrimSpace(c.Query("scope")))
	scoped := false
	switch scope {
	case "", interfaces.UserManagementScopeCurrent:
	case interfaces.UserManagementScopeOther, interfaces.UserManagementScopeAll:
		scoped = true
	default:
		_ = c.Error(apperrors.NewValidationError(
			"scope must be one of current, other, all"))
		return
	}

	// role 支持逗号分隔多值（T22「编辑」目录一次查 contributor+admin）。
	// 逐段 TrimSpace；任一段为空或非法 → 400 且不触达 service。单值
	// 继续走旧方法，保持既有调用方零破坏。
	roles, ok := parseUserManagementRoles(c.Query("role"))
	if !ok {
		_ = c.Error(apperrors.NewValidationError(
			"role must be one of viewer, contributor, admin, owner"))
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	page, pageSize, ok := parseListPagination(c)
	if !ok {
		return
	}

	var result *interfaces.UserManagementListResult
	var err error
	if len(roles) > 1 {
		if scoped {
			result, err = h.userManagementService.ListUsersScopedWithRoles(
				ctx, tenantID, scope, roles, q, page, pageSize)
		} else {
			result, err = h.userManagementService.ListUsersWithRoles(
				ctx, tenantID, roles, q, page, pageSize)
		}
	} else {
		role := types.TenantRole("")
		if len(roles) == 1 {
			role = roles[0]
		}
		if scoped {
			result, err = h.userManagementService.ListUsersScoped(
				ctx, tenantID, scope, role, q, page, pageSize)
		} else {
			result, err = h.userManagementService.ListUsers(ctx, tenantID, role, q, page, pageSize)
		}
	}
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

// parseUserManagementRoles 解析 role 查询参数：空串 = 全部；否则按逗号
// 拆分、逐段 TrimSpace，任一段为空或不是合法角色返回 ok=false（调用方
// 负责 400），并且不会触达 service。大小写不符也算非法（与既有口径一致）。
func parseUserManagementRoles(raw string) ([]types.TenantRole, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	roles := make([]types.TenantRole, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			return nil, false
		}
		role := types.TenantRole(trimmed)
		if !role.IsValid() {
			return nil, false
		}
		roles = append(roles, role)
	}
	return roles, true
}

// renderServiceError 把服务层的 *AppError 原样交给全局错误处理器，
// 保持 403/404/400 状态码矩阵；其他错误统一 500，不泄露内部细节。
func (h *UserManagementHandler) renderServiceError(c *gin.Context, err error) {
	renderManagementError(c, err)
}
