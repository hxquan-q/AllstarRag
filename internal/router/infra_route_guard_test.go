package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/models/utils/ollama"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T01 TDD 红阶段：Ollama 6 条 + 小爱云 2 条路由收紧到「仅系统管理员」。
//
// 契约（T01 验收条件 1-6、AC-6/AC-7、C4）：
//   - 路由守卫目标为 g.SystemAdmin()（现 g.Viewer()/g.Admin()）；
//   - JWT 空间角色（owner/admin/contributor/viewer）→ 403，且落 LogDenied 审计；
//   - 系统管理员（is_system_admin）不论工作区角色 → 放行；
//   - API-key 主体：平台级 key 放行；租户级 key（apiKeyManageModels 声明）→ 403；
//   - EnableRBAC=false 时仍拒绝（RequireSystemAdmin 不受开关影响）；
//   - 与「模型」分区共用的 POST/GET /models、embedding/test 行为零变化（对照组）。
//
// 测试走真实 RegisterInitializationRoutes / RegisterWeKnoraCloudRoutes /
// RegisterModelRoutes（含 API-key 网关）。Ollama 服务地址指向本机必拒绝的
// 端口，探测确定性失败：放行组断言 handler 真实产出的状态码（allowedCode），
// 证明请求穿透了守卫、不是 gin 的路由 404；拒绝组断言恰好 403。
// 现状守卫未换，本文件按目标行为断言，现在跑必红（红阶段预期产物）。

// infraGuardCloudStub 只实现两个方法，其余调用即 panic（不应被触达）。
type infraGuardCloudStub struct {
	interfaces.WeKnoraCloudService
}

func (infraGuardCloudStub) SaveCredentials(context.Context, string, string) error {
	return nil
}

func (infraGuardCloudStub) CheckStatus(context.Context) (*types.WeKnoraCloudStatusResult, error) {
	return &types.WeKnoraCloudStatusResult{}, nil
}

// infraGuardModelStub 只实现 ListModels（对照组 GET /models 用）。
type infraGuardModelStub struct {
	interfaces.ModelService
}

func (infraGuardModelStub) ListModels(context.Context) ([]*types.Model, error) {
	return nil, nil
}

// infraGuardAuditStub 捕获 LogDenied 调用（T01 验收条件 1 的审计断言）。
type infraGuardAuditStub struct {
	interfaces.AuditLogService
	mu    sync.Mutex
	calls []infraGuardDenyCall
}

type infraGuardDenyCall struct {
	tenantID uint64
	userID   string
	required types.TenantRole
}

func (s *infraGuardAuditStub) LogDenied(
	_ context.Context, _ *gin.Context, tenantID uint64,
	userID, _ string, required types.TenantRole,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, infraGuardDenyCall{tenantID: tenantID, userID: userID, required: required})
	return nil
}

// infraGuardSession 请求身份：JWT（工作区角色 + 是否系统管理员）或 API-key 主体。
type infraGuardSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
	tenantID    uint64
	apiKey      *types.TenantAPIKeyScope
}

// infraGuardRoute.allowedCode 语义：守卫放行后 handler 真实产出的状态码。
// Ollama 探测必失败时：status 失败也回 200（available=false）；models 列表与
// check/download 回 500；progress 查不存在的任务回 404；tasks 空表回 200。
// 小爱云两条走桩服务回 200。
type infraGuardRoute struct {
	name        string
	method      string
	path        string
	body        string
	allowedCode int
}

var infraGuardRoutes = []infraGuardRoute{
	{name: "ollama_status", method: http.MethodGet,
		path: "/api/v1/initialization/ollama/status", allowedCode: http.StatusOK},
	{name: "ollama_models_list", method: http.MethodGet,
		path: "/api/v1/initialization/ollama/models", allowedCode: http.StatusInternalServerError},
	{name: "ollama_models_check", method: http.MethodPost,
		path: "/api/v1/initialization/ollama/models/check",
		body: `{"models":["qwen3:0.6b"]}`, allowedCode: http.StatusInternalServerError},
	{name: "ollama_models_download", method: http.MethodPost,
		path: "/api/v1/initialization/ollama/models/download",
		body: `{"modelName":"qwen3:0.6b"}`, allowedCode: http.StatusInternalServerError},
	{name: "ollama_download_progress", method: http.MethodGet,
		path:        "/api/v1/initialization/ollama/download/progress/t01-missing-task",
		allowedCode: http.StatusNotFound},
	{name: "ollama_download_tasks", method: http.MethodGet,
		path: "/api/v1/initialization/ollama/download/tasks", allowedCode: http.StatusOK},
	{name: "weknoracloud_credentials", method: http.MethodPost,
		path: "/api/v1/weknoracloud/credentials",
		body: `{"app_id":"app","app_secret":"secret"}`, allowedCode: http.StatusOK},
	{name: "weknoracloud_status", method: http.MethodGet,
		path: "/api/v1/models/weknoracloud/status", allowedCode: http.StatusOK},
}

func infraGuardEngine(
	t *testing.T, s infraGuardSession, rbacOn bool, audit *infraGuardAuditStub,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if s.tenantID == 0 {
		s.tenantID = memberGuardTenantID
	}
	flag := rbacOn
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &flag}}
	g := &rbacGuards{cfg: cfg}

	// Ollama 服务指向本机未监听端口：探测立即 connection refused，结果确定。
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	ollamaSvc, err := ollama.GetOllamaService()
	require.NoError(t, err)
	initHandler := handler.NewInitializationHandler(
		cfg, nil, nil, nil, nil, nil, ollamaSvc, nil, nil, nil,
	)

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	if audit != nil {
		engine.Use(middleware.AuditServiceProvider(audit))
	}
	engine.Use(func(c *gin.Context) {
		// 与生产 applyAuthSession 同款：身份键同时写 gin keys 与 request
		// context 两个面（handler 两种读法都有：model.go 走 c.GetUint64，
		// rbac 中间件走 *FromContext），只写一面会出现假失败。
		ctx := c.Request.Context()
		set := func(key types.ContextKey, v any) {
			c.Set(key.String(), v)
			ctx = context.WithValue(ctx, key, v)
		}
		set(types.TenantIDContextKey, s.tenantID)
		set(types.UserIDContextKey, s.userID)
		if s.role != "" {
			set(types.TenantRoleContextKey, s.role)
		}
		set(types.SystemAdminContextKey, s.systemAdmin)
		if s.apiKey != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *s.apiKey)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	// 与生产一致：API-key 网关挂在路由守卫之前，按声明表默认拒绝。
	engine.Use(g.ensureAPIKeyAuthorizer().Middleware())

	v1 := engine.Group("/api/v1")
	RegisterInitializationRoutes(v1, initHandler, g)
	RegisterWeKnoraCloudRoutes(v1, handler.NewWeKnoraCloudHandler(infraGuardCloudStub{}), g)
	RegisterModelRoutes(v1, handler.NewModelHandler(infraGuardModelStub{}),
		&handler.ModelCredentialsHandler{}, g)
	return engine
}

func infraGuardDo(
	t *testing.T, s infraGuardSession, rbacOn bool, audit *infraGuardAuditStub,
	method, path, body string,
) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	w := httptest.NewRecorder()
	infraGuardEngine(t, s, rbacOn, audit).ServeHTTP(w, req)
	return w.Code
}

// 红点 1：JWT 空间角色（owner/admin/contributor/viewer）调 8 条路由全部 403。
// 现状：Viewer 档 5 条全员放行、Admin 档 3 条 owner/admin 放行 → 现在跑必红。
func TestInfraRoutesRejectWorkspaceRoles(t *testing.T) {
	roles := map[string]types.TenantRole{
		"owner":       types.TenantRoleOwner,
		"admin":       types.TenantRoleAdmin,
		"contributor": types.TenantRoleContributor,
		"viewer":      types.TenantRoleViewer,
	}
	for name, role := range roles {
		role := role
		t.Run(name, func(t *testing.T) {
			s := infraGuardSession{role: role, userID: "u-" + name}
			for _, rt := range infraGuardRoutes {
				code := infraGuardDo(t, s, true, nil, rt.method, rt.path, rt.body)
				require.Equal(t, http.StatusForbidden, code,
					"%s %s 必须拒绝空间角色 %s（T01：仅系统管理员）", rt.method, rt.path, name)
			}
		})
	}
}

// 系统管理员 8 条全部放行，不论工作区角色。现状：owner/admin 工作区角色的
// 超管已放行（绿）；viewer/无成员关系的超管在 Admin 档 3 条被现状守卫拒绝
// → 这些子用例现在跑红，属预期红点（AC：行为与现状超管一致=全放行）。
func TestInfraRoutesAllowSystemAdmin(t *testing.T) {
	cases := map[string]types.TenantRole{
		"owner_workspace":  types.TenantRoleOwner,
		"viewer_workspace": types.TenantRoleViewer,
		"no_membership":    "",
	}
	for name, role := range cases {
		role := role
		t.Run(name, func(t *testing.T) {
			s := infraGuardSession{role: role, userID: "u-sysadmin", systemAdmin: true}
			for _, rt := range infraGuardRoutes {
				code := infraGuardDo(t, s, true, nil, rt.method, rt.path, rt.body)
				require.Equal(t, rt.allowedCode, code,
					"系统管理员（工作区角色 %q）必须放行 %s %s", name, rt.method, rt.path)
			}
		})
	}
}

// 平台级 key（带 manage_models 声明）放行：现状与目标一致，绿组保护网。
func TestInfraRoutesAllowPlatformAPIKey(t *testing.T) {
	s := infraGuardSession{
		role: types.TenantRoleOwner, userID: "",
		apiKey: &types.TenantAPIKeyScope{
			KeyID:        101,
			ScopeType:    types.APIKeyScopePlatform,
			Capabilities: types.StringArray{string(types.APIKeyCapabilityManageModels)},
		},
	}
	for _, rt := range infraGuardRoutes {
		code := infraGuardDo(t, s, true, nil, rt.method, rt.path, rt.body)
		require.Equal(t, rt.allowedCode, code,
			"平台级 key 必须放行 %s %s（T01 验收条件 3）", rt.method, rt.path)
	}
}

// 红点 2：租户级机器 key（全权限 / manage_models 声明）8 条一律 403。
// 现状：RequireRole 对 API-key 主体短路放行 → 现在跑必红。
func TestInfraRoutesRejectTenantAPIKeys(t *testing.T) {
	scopes := map[string]*types.TenantAPIKeyScope{
		"tenant_full_access": {KeyID: 102, FullAccess: true},
		"tenant_manage_models": {KeyID: 103,
			Capabilities: types.StringArray{string(types.APIKeyCapabilityManageModels)}},
	}
	for name, scope := range scopes {
		scope := scope
		t.Run(name, func(t *testing.T) {
			s := infraGuardSession{role: types.TenantRoleOwner, userID: "", apiKey: scope}
			for _, rt := range infraGuardRoutes {
				code := infraGuardDo(t, s, true, nil, rt.method, rt.path, rt.body)
				require.Equal(t, http.StatusForbidden, code,
					"租户级 key（%s）必须被拒 %s %s（T01 验收条件 3）", name, rt.method, rt.path)
			}
		})
	}
}

// 红点 3：EnableRBAC=false 时 8 条仍 403（RequireSystemAdmin always-enforced）。
// 现状：Viewer/Admin 守卫在开关关闭时只记日志放行 → 现在跑必红。
func TestInfraRoutesRejectEvenWhenRBACDisabled(t *testing.T) {
	for name, role := range map[string]types.TenantRole{
		"owner":       types.TenantRoleOwner,
		"admin":       types.TenantRoleAdmin,
		"contributor": types.TenantRoleContributor,
		"viewer":      types.TenantRoleViewer,
	} {
		role := role
		t.Run(name, func(t *testing.T) {
			s := infraGuardSession{role: role, userID: "u-" + name}
			for _, rt := range infraGuardRoutes {
				code := infraGuardDo(t, s, false, nil, rt.method, rt.path, rt.body)
				require.Equal(t, http.StatusForbidden, code,
					"RBAC 关闭时 %s %s 仍必须拒绝空间角色 %s（always-enforced）",
					rt.method, rt.path, name)
			}
		})
	}
}

// 红点 4：拒绝必须落审计（沿 LogDenied 先例，T01 验收条件 1）。
// 现状：owner 放行、无 403 也无审计行 → 现在跑必红。
func TestInfraRoutesDenialFiresAudit(t *testing.T) {
	audit := &infraGuardAuditStub{}
	s := infraGuardSession{role: types.TenantRoleOwner, userID: "u-owner-audit"}
	code := infraGuardDo(t, s, true, audit, http.MethodGet,
		"/api/v1/initialization/ollama/status", "")
	require.Equal(t, http.StatusForbidden, code, "owner 调 Ollama 状态接口必须 403")
	require.Len(t, audit.calls, 1, "拒绝必须恰好落一条 rbac 拒绝审计")
	got := audit.calls[0]
	require.Equal(t, memberGuardTenantID, got.tenantID)
	require.Equal(t, "u-owner-audit", got.userID)
	require.Equal(t, types.TenantRole("system_admin"), got.required,
		"RequireSystemAdmin 拒绝路径的 required 参数必须是 system_admin")
}

// 对照组（绿组保护网）：与「模型」分区共用的接口行为零变化（验收条件 4）。
// 现状与目标都应通过——防止守卫收紧波及 owner 端保留的模型分区。
func TestInfraSharedModelRoutesStayOpenToWorkspaceRoles(t *testing.T) {
	owner := infraGuardSession{role: types.TenantRoleOwner, userID: "u-owner"}
	admin := infraGuardSession{role: types.TenantRoleAdmin, userID: "u-admin"}

	// GET /models：Viewer+ 可读；owner 放行后由桩服务回 200 空表。
	require.Equal(t, http.StatusOK,
		infraGuardDo(t, owner, true, nil, http.MethodGet, "/api/v1/models", ""),
		"owner 必须仍能读模型列表（owner 端「模型」分区保留）")

	// POST /models：Admin+ 可写；owner/admin 过守卫后因缺必填字段回 400（≠403）。
	require.Equal(t, http.StatusBadRequest,
		infraGuardDo(t, owner, true, nil, http.MethodPost, "/api/v1/models", "{}"),
		"owner 必须仍能到达创建模型接口（400=参数校验，不是守卫拒绝）")
	require.Equal(t, http.StatusBadRequest,
		infraGuardDo(t, admin, true, nil, http.MethodPost, "/api/v1/models", "{}"),
		"admin 必须仍能到达创建模型接口")

	// embedding/test：Admin+；owner 过守卫后因缺 modelName 回 400（≠403）。
	require.Equal(t, http.StatusBadRequest,
		infraGuardDo(t, owner, true, nil, http.MethodPost,
			"/api/v1/initialization/embedding/test", "{}"),
		"owner 必须仍能到达 embedding 连通性测试接口")

	// 反向对照：viewer 调 Admin 档写接口维持 403（守卫没有被放松）。
	viewer := infraGuardSession{role: types.TenantRoleViewer, userID: "u-viewer"}
	require.Equal(t, http.StatusForbidden,
		infraGuardDo(t, viewer, true, nil, http.MethodPost, "/api/v1/models", "{}"),
		"viewer 调创建模型接口必须维持 403")
}
