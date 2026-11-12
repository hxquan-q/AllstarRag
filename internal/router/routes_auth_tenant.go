package router

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterTenantRoutes 注册空间相关的路由
//
// Tenant-internal RBAC for /tenants/:id:
//   - GET   /:id          Viewer+ (read tenant settings)
//   - PUT   /:id          Owner+ (mutate tenant config)
//   - DELETE /:id         Owner+ (also normally a CanAccessAllTenants op)
//   - GET/POST/PUT/DELETE /:id/api-keys   Owner+ (scoped API key management)
//   - GET    /:id/members            Viewer+ (any member can see who else is in)
//   - POST   /:id/members            Owner+ (only Owner can add new members)
//   - POST   /:id/members/create     Admin+/系统管理员（系统管理员当前工作区
//     角色低于 admin 也放行，由 handler「系统管理员按 owner」矩阵把关）
//   - POST   /:id/members/:user_id/reset-password  Admin+/系统管理员（同上）
//   - PUT    /:id/members/:user_id   Owner+ / 系统管理员 (system admins
//     pass regardless of their workspace role; target-level owner
//     protection is enforced in the handler)
//   - DELETE /:id/members/:user_id   Owner+ (only Owner can remove members)
//   - POST   /:id/leave              Viewer+ (any member can quit on their own)
//
// All /tenants/:id endpoints share g.PathTenantMatch() at the group
// level: middleware/access.go enforces "URL :id == active tenant"
// (with the cross-tenant superuser carve-out) so an Owner-of-A cannot
// drive operations against tenant B by changing the URL. This used to
// be authorizeTenantAccess in tenant.go and resolveTenantIDFromPath in
// tenant_member.go; collapsing it into one route guard means the
// declaration itself documents the rule.
//
// Cross-tenant superuser endpoints (/tenants/all, /tenants/search) use
// g.CrossTenant(): RequireCrossTenantAccess in access.go combines the
// CanAccessAllTenants user attribute with the cluster-wide
// EnableCrossTenantAccess flag, replacing the 12-line if-block that
// previously opened ListAllTenants and SearchTenants.
//
// POST /tenants 是平台级建空间入口（T11 / AC-44），不再是上游的
// self-service 行为：只有系统管理员 / 跨空间超管 / 平台 API Key 能创建。
// GET /tenants 仍按当前空间列出。平台 API Key 通过 system_tenants_manage
// 创建空间；workspace key 对租户目录操作保持默认拒绝。
func RegisterTenantRoutes(
	r *gin.RouterGroup,
	handler *handler.TenantHandler,
	memberHandler *handler.TenantMemberHandler,
	invitationHandler *handler.TenantInvitationHandler,
	auditLogHandler *handler.AuditLogHandler,
	g *rbacGuards,
) {
	// Cross-tenant superuser endpoints — promoted from handler if-blocks
	// to middleware.RequireCrossTenantAccess at the route layer.
	g.apiKeyRoute(r, http.MethodGet, "/tenants/all",
		apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
		g.CrossTenant(), handler.ListAllTenants)
	g.apiKeyRoute(r, http.MethodGet, "/tenants/search",
		apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
		g.CrossTenant(), handler.SearchTenants)

	// 空间路由组
	tenantRoutes := r.Group("/tenants")
	{
		// Current-workspace operations cannot select another tenant, including
		// when the caller has cross-tenant privileges. JWT-only for now.
		tenantRoutes.GET("/current", g.Viewer(), handler.GetCurrentWorkspace)
		tenantRoutes.PATCH("/current", g.Owner(), handler.UpdateCurrentWorkspace)
		if memberHandler != nil {
			tenantRoutes.GET("/current/members", g.Viewer(), memberHandler.ListCurrentWorkspaceMembers)
		}
		// 创建空间是平台级操作（T11 / AC-44）：只有系统管理员、跨空间
		// 超管或平台 API Key 能创建；普通用户（owner/admin/contributor/
		// viewer）即使上游 self_service 开关开启，也会被 handler 返回 403。
		// 跨空间超管走同一个端点，可携带 storage_quota / status 等全字段
		// （见 handler.CreateTenant 内部分支）。
		// 安全说明：这里不挂 g.CrossTenant()；平台级判定在 handler 内完成
		// （见 tenant_policy.go 的产品覆盖说明）。
		// workspace API key 对建空间保持默认拒绝；平台 key 通过
		// system_tenants_manage 能力放行。
		g.apiKeyRoute(tenantRoutes, http.MethodPost, "",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage), handler.CreateTenant)
		g.apiKeyRoute(tenantRoutes, http.MethodGet, "", apiKeyManageTenantSettings(apiKeyFullAccess()), handler.ListTenants)

		// Generic KV configuration management (tenant-level). Tenant ID
		// is obtained from authentication context; the URL :key is a
		// config key, not a tenant ID, so these stay outside the
		// PathTenantMatch group. Tenant-level surface: full-access keys may
		// call it, and scoped keys need manage_tenant_settings.
		g.apiKeyRoute(tenantRoutes, http.MethodGet, "/kv/:key", apiKeyManageTenantSettings(apiKeyFullAccess()), g.Viewer(), handler.GetTenantKV)
		g.apiKeyRoute(tenantRoutes, http.MethodPut, "/kv/:key", apiKeyManageTenantSettings(apiKeyFullAccess()), g.Admin(), handler.UpdateTenantKV)

		// Per-tenant endpoints share PathTenantMatch at the group level.
		// Most /tenants/:id/* endpoints stay undeclared for API keys by
		// default — tenant lifecycle and key/principal management require
		// full tenant access or JWT ownership. Member/invitation management
		// opts in below through the manage_members capability.
		tenantByID := tenantRoutes.Group("/:id", g.PathTenantMatch())
		{
			g.apiKeyRoute(tenantByID, http.MethodGet, "",
				apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
				g.Viewer(), handler.GetTenant)
			g.apiKeyRoute(tenantByID, http.MethodPut, "",
				apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage), g.Owner(), handler.UpdateTenant)
			g.apiKeyRoute(tenantByID, http.MethodDelete, "",
				apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage), g.Owner(), handler.DeleteTenant)
			tenantByID.GET("/api-keys", g.Owner(), handler.ListAPIKeys)
			tenantByID.POST("/api-keys", g.Owner(), handler.CreateAPIKey)
			tenantByID.PUT("/api-keys/:key_id", g.Owner(), handler.UpdateAPIKey)
			tenantByID.DELETE("/api-keys/:key_id", g.Owner(), handler.DeleteAPIKey)
			tenantByID.GET("/api-principal-config", g.Owner(), handler.GetAPIPrincipalConfig)
			tenantByID.PUT("/api-principal-config", g.Owner(), handler.UpdateAPIPrincipalConfig)
			tenantByID.POST("/api-principal-test-token", g.Owner(), handler.CreateAPIPrincipalTestToken)

			// Tenant member management (PR 3 of #1303). Listing is
			// Viewer+ so any active member can see the roster; mutation
			// is Admin+ (Owner/普通管理员) because membership changes are the
			// highest-impact tenant op. /:id/leave is Viewer+ — any member
			// can quit on their own; the service still rejects when it would
			// leave the tenant without an Owner.
			if memberHandler != nil {
				g.apiKeyRoute(tenantByID, http.MethodGet, "/members", apiKeyManageMembers(apiKeyFullAccess()), g.Viewer(), memberHandler.ListMembers)
				// T06：普通管理员拿到 co-owner 能力集，成员治理入口放宽到
				// Admin+；目标级保护（不能操作 owner/同级/自己）在 service
				// 内判定，不靠路由守卫单独兜底（C4）。
				g.apiKeyRoute(tenantByID, http.MethodPost, "/members", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), memberHandler.AddMember)
				// 建号接口：路由层只保证 Admin+/系统管理员，细粒度角色矩阵
				// （owner/普通管理员可建 admin/viewer，禁止建 owner/contributor）
				// 在 handler 内判定，见 service.CanCreateRole；系统管理员即使
				// 当前工作区角色低于 admin 也放行进 handler，按「系统管理员按
				// owner」矩阵处理（tenant_member.go CreateMemberAccount 内）。
				// JWT-only：不通过 apiKeyRoute 声明，API key 默认拒绝。
				tenantByID.POST("/members/create", g.AdminOrSystemAdmin(), memberHandler.CreateMemberAccount)
				// 重置成员密码：路由层只保证 Admin+/系统管理员，细粒度矩阵
				// （owner 可重置 admin/viewer，普通管理员只能重置 viewer，
				// 不能重置同级/上级/本人）在 handler 内判定，见
				// service.CanResetRole；系统管理员即使当前工作区角色低于 admin
				// 也放行进 handler，按「系统管理员按 owner」矩阵处理
				// （tenant_member.go ResetMemberPassword 内），特权目标保护
				// （F1 门禁）不变。JWT-only：不通过 apiKeyRoute 声明，API key
				// 默认拒绝。
				tenantByID.POST("/members/:user_id/reset-password",
					g.AdminOrSystemAdmin(), memberHandler.ResetMemberPassword)
				// 改角色：Owner/普通管理员或系统管理员。超管不需要在本空间持有
				// owner 角色；目标级 owner/同级保护（不能直接降级 owner，必须走
				// transfer-owner）在 handler.UpdateMemberRole 内兜底。
				g.apiKeyRoute(tenantByID, http.MethodPut, "/members/:user_id", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), memberHandler.UpdateMemberRole)
				g.apiKeyRoute(tenantByID, http.MethodDelete, "/members/:user_id", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), memberHandler.RemoveMember)
				tenantByID.POST("/leave", g.Viewer(), memberHandler.LeaveTenant)
			}

			// Tenant invitation flow. The UI-driven "Invite Member"
			// button hits POST /invitations rather than POST /members,
			// so the invitee gets to confirm via /me/invitations
			// before any tenant_members row is written. List is
			// Viewer+ so any member can see pending invites in the
			// management view; create/revoke are Admin+ to match the
			// existing /members mutation gates. nil-skip pattern
			// mirrors memberHandler above for environments built
			// without the invitation dependency wired.
			if invitationHandler != nil {
				g.apiKeyRoute(tenantByID, http.MethodGet, "/invitations", apiKeyManageMembers(apiKeyFullAccess()), g.Viewer(), invitationHandler.ListTenantInvitations)
				g.apiKeyRoute(tenantByID, http.MethodPost, "/invitations", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), invitationHandler.CreateInvitation)
				g.apiKeyRoute(tenantByID, http.MethodDelete, "/invitations/:inv_id", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), invitationHandler.RevokeInvitation)
				// Share-link create lives under /invite-links so the URL
				// reads as "create a link" rather than another flavour
				// of /invitations; the underlying row still lives in the
				// tenant_invitations table and shows up in the GET above.
				g.apiKeyRoute(tenantByID, http.MethodPost, "/invite-links", apiKeyManageMembers(apiKeyFullAccess()), g.AdminOrSystemAdmin(), invitationHandler.CreateInviteLink)
			}

			// 空间操作记录（上游 PR 6 of #1303）。T06 起本空间 owner / 普通
			// 管理员与系统管理员可只读（contributor/viewer 仍 403）；跨空间
			// 读取已由组级 PathTenantMatch 拦截。不声明 apiKeyRoute，API Key
			// 走网关默认拒绝。auditLogHandler 为 nil 时跳过注册。
			if auditLogHandler != nil {
				tenantByID.GET("/audit-log", g.AdminOrSystemAdmin(), auditLogHandler.ListTenantAuditLog)
			}
		}
	}
}

// RegisterUserManagementRoutes 注册 Owner/普通管理员与系统管理员的成员管理接口
// （封禁 / 解封 / 彻底删除 / 空间交接 / 用户管理列表）。拆成独立的
// Register* 函数，避免改动 RegisterTenantRoutes 的签名和既有调用点。
//
// 守卫与 02-plan §3.1 的契约一致：
//   - /tenants/:id 组级：g.PathTenantMatch()（URL 空间必须等于当前激活
//     空间；跨空间超管按既有规则放行）；
//   - 封禁/解封/彻底删除/用户管理列表：g.AdminOrSystemAdmin()；
//   - 空间交接：g.OwnerOrSystemAdmin() —— 系统管理员即使当前工作区角色
//     低于 owner 也放行。
//
// 目标级保护（不能操作自己、owner 目标、系统管理员、别处 owner、跨租户
// 解封）在 UserManagementService 内判定。
func RegisterUserManagementRoutes(
	r *gin.RouterGroup,
	h *handler.UserManagementHandler,
	g *rbacGuards,
) {
	if h == nil {
		return
	}
	tenantByID := r.Group("/tenants/:id", g.PathTenantMatch())
	// T06：封禁/解封/彻底删除/用户管理列表放宽到普通管理员；目标保护
	// （owner/同级/自己）与跨空间规则在 UserManagementService 内判定。
	// 封禁/注销在事务内复核目标成员角色未变（CP3 B2），路由层注释的
	// 快照语义因此不再暴露「校验后被提权」的窗口。
	tenantByID.POST("/members/:user_id/ban", g.AdminOrSystemAdmin(), h.BanMember)
	tenantByID.POST("/members/:user_id/unban", g.AdminOrSystemAdmin(), h.UnbanMember)
	tenantByID.POST("/members/:user_id/purge", g.AdminOrSystemAdmin(), h.PurgeMember)
	// owner 空间交接：owner 只能传自己，系统管理员可传任意 owner。
	// 唯一 owner 模型下这是 owner 身份变更的唯一入口，保持 owner-only。
	tenantByID.POST("/transfer-owner", g.OwnerOrSystemAdmin(), h.TransferOwner)
	// 用户管理列表（左目录计数 + 右列表）：普通管理员可见本空间成员，
	// 系统管理员即使当前工作区角色低于 admin 也放行。业务判权在
	// UserManagementService.ListUsers 再证一次（C4）。
	tenantByID.GET("/user-management", g.AdminOrSystemAdmin(), h.ListUsers)
}

// RegisterPlatformUserManagementRoutes 注册平台级账号操作接口（T35）：
//   - POST /system/admin/users/:user_id/ban
//   - POST /system/admin/users/:user_id/unban
//   - POST /system/admin/users/:user_id/purge
//
// 平台接口按 users.id 定位账号，不依赖当前工作区成员关系，因此：
//   - 不挂 g.PathTenantMatch()（URL 中没有空间维度）；
//   - 组级只挂 g.SystemAdmin()，非系统管理员 403；
//   - 不走 apiKeyGroup 声明 → API Key 授权表查不到该路由，网关默认拒绝。
//
// 目标保护与跨空间规则仍在 UserManagementService 内完成，服务层会再证
// 一次系统管理员身份（C4）。
func RegisterPlatformUserManagementRoutes(
	r *gin.RouterGroup,
	h *handler.UserManagementHandler,
	g *rbacGuards,
) {
	if h == nil {
		return
	}
	adminRoutes := r.Group("/system/admin", g.SystemAdmin())
	adminRoutes.POST("/users/:user_id/ban", h.PlatformBanUser)
	adminRoutes.POST("/users/:user_id/unban", h.PlatformUnbanUser)
	adminRoutes.POST("/users/:user_id/purge", h.PlatformPurgeUser)
}

// RegisterOwnerManagementRoutes 注册仅系统管理员可用的 owner 分配接口：
//   - POST /system/admin/users/:user_id/owner-space
//
// 唯一 owner 模型下不再提供 owner 邀请创建路径。拆成独立的 Register*
// 函数，避免改动 RegisterSystemAdminRoutes 的签名和既有调用点。组级统一
// 挂 g.SystemAdmin()，与 /system/admin 下其他路由同一口径。
func RegisterOwnerManagementRoutes(
	r *gin.RouterGroup,
	h *handler.OwnerManagementHandler,
	g *rbacGuards,
) {
	if h == nil {
		return
	}
	adminRoutes := r.Group("/system/admin", g.SystemAdmin())
	adminRoutes.POST("/users/:user_id/owner-space", h.ProvisionOwnerSpace)
}

// RegisterMyInvitationRoutes wires the per-user invitation inbox under
// /me/invitations. The v1 group already applies middleware.Auth so we
// don't need a role gate here — the service enforces "only the invitee
// can accept/decline". The list endpoint mounts under /me to make the
// "show me MY invitations" semantics obvious in URLs and logs (vs the
// tenant-scoped /tenants/:id/invitations which lists ALL invitations
// for the tenant). pending-count is a separate, ultra-light endpoint
// the avatar-row badge polls; splitting it off so polling doesn't
// transfer the full list every cycle.
//
// invitationHandler may be nil in environments built without the
// invitation dependency wired; that's a no-op registration which is
// preferable to a startup crash.
func RegisterMyInvitationRoutes(r *gin.RouterGroup, invitationHandler *handler.TenantInvitationHandler) {
	if invitationHandler == nil {
		return
	}
	me := r.Group("/me")
	{
		me.GET("/invitations", invitationHandler.ListMyInvitations)
		me.GET("/invitations/pending-count", invitationHandler.CountMyPendingInvitations)
		me.POST("/invitations/:inv_id/accept", invitationHandler.AcceptMyInvitation)
		me.POST("/invitations/:inv_id/decline", invitationHandler.DeclineMyInvitation)
		// 已登录用户用共享链接 token 加入空间（对应 register-by-invite，但不建新账号）。
		me.POST("/invitations/accept-by-token", invitationHandler.AcceptMyInvitationByToken)
	}
}

// RegisterMyEnvVarRoutes wires the caller's own environment variables under
// /me/env-vars. The v1 group already applies middleware.Auth, and no role gate
// is added on purpose: these are the caller's own values, and the service
// derives whose they are from the context rather than the request.
//
// This deliberately does not reuse /sandbox-configs/:id/skills*, which is
// Admin+ even for reads (see routes_infra.go): an upload there drives a root
// shell whose output is baked into the image, and the listing names what that
// image carries. This endpoint returns declarations and set/unset status only.
//
// h may be nil in environments built without the dependency wired; a no-op
// registration is preferable to a startup crash, as with the invitation inbox.
func RegisterMyEnvVarRoutes(r *gin.RouterGroup, h *handler.MeEnvVarHandler) {
	if h == nil {
		return
	}
	me := r.Group("/me/env-vars")
	{
		me.GET("", h.List)
		me.PUT("/skill", h.SetSkill)
		me.DELETE("/skill", h.DeleteSkill)
		me.PUT("/sandbox", h.SetSandbox)
		me.DELETE("/sandbox", h.DeleteSandbox)
	}
}

// RegisterAuthRoutes registers authentication routes
func RegisterAuthRoutes(r *gin.RouterGroup, handler *handler.AuthHandler, g *rbacGuards) {
	// 登录/注册是公开的无认证入口,按 IP 限速:注册 3 次/小时挡住批量建号。
	// 登录的账号维度(邮箱+IP,5 次/分)在 handler 内计数(网关不传真实
	// 客户端 IP,纯 IP 桶会全员共享);这里保留 30 次/分的 IP 总闸,
	// 仅用于挡住高基数的洪水流量。两者各自独立计数,不共享预算。
	loginRL := middleware.PublicAuthRateLimitWith(60*time.Second, 30, "auth-login-ip")
	registerRL := middleware.PublicAuthRateLimitWith(time.Hour, 3, "auth-register")
	r.POST("/auth/register", registerRL, handler.Register)
	// Share-link surfaces are unauthenticated and accept a plaintext
	// token from the caller; rate-limit by IP to bound brute-force /
	// enumeration / abuse traffic. Limiter is shared across both
	// endpoints (see middleware/auth_public_ratelimit.go) so total
	// budget per IP is intuitive.
	publicAuthRL := middleware.PublicAuthRateLimit()
	r.POST("/auth/register-by-invite", publicAuthRL, handler.RegisterByInvite)
	r.POST("/auth/invitations/lookup", publicAuthRL, handler.LookupInvitationByToken)
	r.POST("/auth/login", loginRL, handler.Login)
	r.POST("/auth/auto-setup", handler.AutoSetup)
	r.GET("/auth/config", handler.GetAuthConfig)
	r.POST("/auth/switch-tenant", handler.SwitchTenant)
	r.GET("/auth/oidc/config", handler.GetOIDCConfig)
	r.GET("/auth/oidc/url", handler.GetOIDCAuthorizationURL)
	r.GET("/auth/oidc/callback", handler.OIDCRedirectCallback)
	// /auth/oidc/start：直连 302 跳转到 OIDC 提供方，供前端无法走 JS 拉取 URL 的场景直接发起登录
	r.GET("/auth/oidc/start", handler.OIDCStart)
	r.POST("/auth/refresh", handler.RefreshToken)
	r.GET("/auth/validate", handler.ValidateToken)
	r.POST("/auth/logout", handler.Logout)
	// auth/me returns only the caller's own identity/profile, so it is safe
	// for any valid API key. Chat clients / MCP call it to discover "who am I";
	// leaving it default-deny was why scoped keys got a 403 here.
	g.apiKeyRoute(r, http.MethodGet, "/auth/me", apiKeyAny(), handler.GetCurrentUser)
	// /auth/capabilities：所有已认证用户可读，返回当前角色→能力的唯一投影。
	// 未认证由全局 middleware.Auth 返回 401；不要加入无认证白名单。
	r.GET("/auth/capabilities", handler.GetCapabilities)
	r.PUT("/auth/me/preferences", handler.UpdateMyPreferences)
	r.POST("/auth/change-password", handler.ChangePassword)
}

// RegisterSystemRoutes registers system information routes
//
// Reads (GetSystemInfo / ListParserEngines / GetStorageEngineStatus)
// are gated to Viewer+ — any tenant member can see "is the parser
// reachable". The /*-check / /reconnect endpoints actively probe
// remote services with tenant credentials and could trigger network
// fanout, so they're Admin+.
func RegisterSystemRoutes(
	r *gin.RouterGroup,
	handler *handler.SystemHandler,
	g *rbacGuards,
) {
	// JWT-only: this pops a native dialog on the Lite machine. API keys must
	// not trigger it. Undeclared for the API-key gate, so keys are denied.
	r.POST("/system/host-project-dir", g.Viewer(), handler.PickHostProjectDir)

	systemRoutes := g.apiKeyGroup(r.Group("/system"), apiKeyManageVectorStores(apiKeyFullAccess()))
	{
		systemRoutes.With(apiKeyAny()).GET("/capabilities", g.Viewer(), handler.GetDeploymentCapabilities)
		systemRoutes.GET("/info", g.Viewer(), handler.GetSystemInfo)
		systemRoutes.GET("/parser-engines", g.Viewer(), handler.ListParserEngines)
		systemRoutes.POST("/parser-engines/check", g.Admin(), handler.CheckParserEngines)
		systemRoutes.POST("/docreader/reconnect", g.Admin(), handler.ReconnectDocReader)
		systemRoutes.GET("/storage-engine-status", g.Viewer(), handler.GetStorageEngineStatus)
		systemRoutes.POST("/storage-engine-check", g.Admin(), handler.CheckStorageEngine)
		systemRoutes.POST("/sandbox-check", g.Admin(), handler.CheckSandboxConfig)
	}
}

// RegisterSystemAdminRoutes registers system administration routes.
//
// All endpoints under this group are gated to SystemAdmin users (i.e.
// User.IsSystemAdmin == true). These are platform-wide operations
// independent of per-tenant Owner/Admin/Contributor/Viewer roles —
// they let org-level superusers grant/revoke system-admin status and,
// in later milestones, will host global settings, built-in models, and
// cross-tenant observability.
//
// Mounted under /api/v1/system/admin/* so the URL scheme stays aligned
// with the existing /api/v1/system/info family. Front-end clients live
// in frontend/src/api/system/index.ts.
//
// auditLogHandler may be nil in environments wired without the audit
// dependency; the /audit-log subroute is then omitted. This mirrors
// the optional wiring in RegisterTenantRoutes.
func RegisterSystemAdminRoutes(
	r *gin.RouterGroup,
	handler *handler.SystemHandler,
	auditLogHandler *handler.AuditLogHandler,
	g *rbacGuards,
) {
	// Apply SystemAdmin() at the group level — every route below inherits
	// the guard, so adding new endpoints can't accidentally drop the gate.
	adminRoutes := r.Group("/system/admin", g.SystemAdmin())
	{
		// Catalog mutation is reserved to authenticated system-admin users.
		// API keys remain default-denied by the API-key gate.
		adminRoutes.GET("/model-catalog", handler.GetModelCatalog)
		adminRoutes.POST("/model-catalog/preview", handler.PreviewModelCatalog)
		adminRoutes.PUT("/model-catalog", handler.PublishModelCatalog)

		// P0: SystemAdmin role management
		adminRoutes.POST("/promote", handler.PromoteUserToSystemAdmin)
		adminRoutes.POST("/revoke", handler.RevokeSystemAdmin)
		adminRoutes.GET("/list", handler.ListSystemAdmins)
		adminRoutes.GET("/users", handler.ListUsers)
		adminRoutes.POST("/users/status", handler.SetUserStatus)
		adminRoutes.POST("/users/reset-password", handler.ResetUserPassword)
		adminRoutes.POST("/users/create", handler.CreateSystemUser)
		adminRoutes.GET("/api-keys", handler.ListPlatformAPIKeys)
		adminRoutes.POST("/api-keys", handler.CreatePlatformAPIKey)
		adminRoutes.DELETE("/api-keys/:key_id", handler.DeletePlatformAPIKey)

		adminRoutes.GET("/settings-summary", handler.ListSystemSettingSummaries)

		// P1: platform-wide system settings (DB-backed runtime tunables).
		// Reads return raw model rows / arrays (no `gin.H{"data":...}`
		// wrapping), matching the project's axios interceptor convention
		// — see frontend/src/utils/request.ts:97.
		g.apiKeyRoute(adminRoutes, http.MethodGet, "/settings",
			apiKeyPlatform(types.APIKeyCapabilitySystemSettingsRead, types.APIKeyCapabilitySystemSettingsManage),
			handler.ListSystemSettings)
		g.apiKeyRoute(adminRoutes, http.MethodGet, "/settings/:key",
			apiKeyPlatform(types.APIKeyCapabilitySystemSettingsRead, types.APIKeyCapabilitySystemSettingsManage),
			handler.GetSystemSetting)
		g.apiKeyRoute(adminRoutes, http.MethodPut, "/settings/:key",
			apiKeyPlatform(types.APIKeyCapabilitySystemSettingsManage), handler.UpdateSystemSetting)
		g.apiKeyRoute(adminRoutes, http.MethodDelete, "/settings/:key",
			apiKeyPlatform(types.APIKeyCapabilitySystemSettingsManage), handler.ResetSystemSetting)

		// Runtime operations: live asynq queue depths, safe task projections,
		// and state-checked task actions for the SystemAdmin dashboard. Lite
		// mode returns available=false.
		g.apiKeyRoute(adminRoutes, http.MethodGet, "/runtime/queues",
			apiKeyPlatform(types.APIKeyCapabilitySystemRuntimeRead, types.APIKeyCapabilitySystemRuntimeManage),
			handler.GetRuntimeQueues)
		g.apiKeyRoute(adminRoutes, http.MethodGet, "/runtime/queues/:queue/tasks",
			apiKeyPlatform(types.APIKeyCapabilitySystemRuntimeRead, types.APIKeyCapabilitySystemRuntimeManage),
			handler.ListRuntimeTasks)
		g.apiKeyRoute(adminRoutes, http.MethodPost, "/runtime/queues/:queue/tasks/:task_id/actions/:action",
			apiKeyPlatform(types.APIKeyCapabilitySystemRuntimeManage), handler.MutateRuntimeTask)
		g.apiKeyRoute(adminRoutes, http.MethodDelete, "/runtime/queues/:queue/archived",
			apiKeyPlatform(types.APIKeyCapabilitySystemRuntimeManage), handler.PurgeArchivedRuntimeTasks)

		// Bulk action — write the current default-quota setting onto
		// every existing tenant. Lives under /tenants instead of
		// /settings because it changes tenants, not the setting row.
		g.apiKeyRoute(adminRoutes, http.MethodPost, "/tenants/apply-default-storage-quota",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage),
			handler.ApplyDefaultStorageQuotaToAllTenants)

		// Platform-wide audit feed (tenant_id=0 rows). Covers
		// system.setting_changed / system.admin_promoted /
		// system.admin_revoked etc. — events written by the routes
		// above. Without this endpoint those audit rows would have
		// no UI surface (per-tenant ListTenantAuditLog filters them
		// out by tenant_id). Optional: skip when audit deps are
		// absent, matching RegisterTenantRoutes' /audit-log handling.
		if auditLogHandler != nil {
			g.apiKeyRoute(adminRoutes, http.MethodGet, "/audit-log",
				apiKeyPlatform(types.APIKeyCapabilitySystemAuditRead), auditLogHandler.ListSystemAuditLog)
		}
	}
}
