package handler

// T11 TDD 红阶段：只有系统管理员能创建新空间（AC-44 / AC-45）。
//
// 本文件只写失败用例，不改实现：
//  1. POST /tenants 对 owner / admin / contributor / viewer 一律 403，
//     即使 tenant.self_service_creation_enabled 开启（产品决策：硬 403）；
//  2. 系统管理员即使该开关关闭也能创建（201）；
//  3. /auth/me 的 capabilities.can_create_tenant 只对系统管理员为 true，
//     与后端 POST /tenants 的判定保持一致（不得出现前端可点、后端 403）；
//  4. /auth/login 的 capabilities.can_create_tenant 使用同一口径。
//
// 说明：这里把「超管」按项目口径理解为系统管理员（IsSystemAdmin），
// 与 ROLE-MATRIX.md「超管 = 系统管理员」一致。CanAccessAllTenants
// （跨空间超管）是否仍可建空间，另见 05-tests.md 的 open question，
// 本轮不臆造断言。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// t11CreateTenantHandler 复用 tenant_self_service_policy_test.go 里的
// 测试替身，避免再引入一套 fake 造成重复事实源。
func t11CreateTenantHandler(user *types.User, selfServiceEnabled bool) (*TenantHandler, *tenantPolicyTenantService) {
	tenants := &tenantPolicyTenantService{}
	h := &TenantHandler{
		service:          tenants,
		userService:      &tenantPolicyUserService{user: user},
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: selfServiceEnabled},
	}
	return h, tenants
}

// t11AuthContext 复刻 middleware/auth.go 的 applyAuthSession：把用户 id /
// 工作区角色 / 系统管理员标志放进请求上下文，让实现无论是读用户对象还是
// 读上下文都能拿到一致的身份。
func t11AuthContext(role types.TenantRole, systemAdmin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.UserIDContextKey, "caller")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func t11NewCreateTenantRouter(h *TenantHandler, role types.TenantRole, systemAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(t11AuthContext(role, systemAdmin))
	r.POST("/tenants", h.CreateTenant)
	return r
}

func t11PostTenant(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tenants", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func t11BoolName(enabled bool) string {
	if enabled {
		return "self_service_enabled"
	}
	return "self_service_disabled"
}

// TestCreateTenantRejectsNonSystemAdminRoles 锁定 AC-44：四种非超管角色
// 都不能自助建空间，且与 self_service 开关无关（开关开启也必须 403）。
func TestCreateTenantRejectsNonSystemAdminRoles(t *testing.T) {
	roles := []types.TenantRole{
		types.TenantRoleOwner,
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	}
	for _, role := range roles {
		role := role
		t.Run(string(role), func(t *testing.T) {
			for _, enabled := range []bool{true, false} {
				enabled := enabled
				t.Run(t11BoolName(enabled), func(t *testing.T) {
					user := &types.User{
						ID:       "u-" + string(role),
						TenantID: 1,
					}
					h, tenants := t11CreateTenantHandler(user, enabled)
					r := t11NewCreateTenantRouter(h, role, false)

					w := t11PostTenant(t, r, `{"name":"should-not-exist"}`)

					if w.Code != http.StatusForbidden {
						t.Fatalf("role=%s self_service=%v: status=%d body=%s, want 403",
							role, enabled, w.Code, w.Body.String())
					}
					if tenants.createCalls != 0 {
						t.Fatalf("role=%s self_service=%v: TenantService.CreateTenant called %d times, want 0",
							role, enabled, tenants.createCalls)
					}
				})
			}
		})
	}
}

// TestCreateTenantAllowsSystemAdmin 锁定 AC-44 的正向面：系统管理员建空间
// 不受 self_service 开关限制（部署关闭自助建空间时超管仍要能建）。
func TestCreateTenantAllowsSystemAdmin(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		enabled := enabled
		t.Run(t11BoolName(enabled), func(t *testing.T) {
			user := &types.User{
				ID:            "sysadmin",
				TenantID:      1,
				IsSystemAdmin: true,
			}
			h, tenants := t11CreateTenantHandler(user, enabled)
			r := t11NewCreateTenantRouter(h, types.TenantRoleOwner, true)

			w := t11PostTenant(t, r, `{"name":"admin-created"}`)

			if w.Code != http.StatusCreated {
				t.Fatalf("self_service=%v: status=%d body=%s, want 201",
					enabled, w.Code, w.Body.String())
			}
			if tenants.createCalls != 1 {
				t.Fatalf("self_service=%v: TenantService.CreateTenant called %d times, want 1",
					enabled, tenants.createCalls)
			}
		})
	}
}

// t11AuthMePayload 只取本用例关心的字段。
type t11AuthMePayload struct {
	Data struct {
		Capabilities struct {
			CanCreateTenant *bool `json:"can_create_tenant"`
		} `json:"capabilities"`
	} `json:"data"`
}

// TestAuthMeCreateTenantCapability 锁定 AC-44 的投影一致性：
// /auth/me 的 can_create_tenant 必须与 POST /tenants 的判定一致。
func TestAuthMeCreateTenantCapability(t *testing.T) {
	cases := []struct {
		name               string
		role               types.TenantRole
		systemAdmin        bool
		selfServiceEnabled bool
		want               bool
	}{
		{
			name:               "owner_self_service_enabled",
			role:               types.TenantRoleOwner,
			systemAdmin:        false,
			selfServiceEnabled: true,
			want:               false,
		},
		{
			name:               "owner_self_service_disabled",
			role:               types.TenantRoleOwner,
			systemAdmin:        false,
			selfServiceEnabled: false,
			want:               false,
		},
		{
			name:               "system_admin_self_service_enabled",
			role:               types.TenantRoleOwner,
			systemAdmin:        true,
			selfServiceEnabled: true,
			want:               true,
		},
		{
			name:               "system_admin_self_service_disabled",
			role:               types.TenantRoleOwner,
			systemAdmin:        true,
			selfServiceEnabled: false,
			want:               true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			user := &types.User{
				ID:            "user-" + tc.name,
				Username:      tc.name,
				Email:         tc.name + "@example.com",
				IsSystemAdmin: tc.systemAdmin,
			}
			h := &AuthHandler{
				userService:      &tenantPolicyUserService{user: user},
				configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
				systemSettingSvc: &tenantPolicySettingService{enabled: tc.selfServiceEnabled},
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(middleware.ErrorHandler())
			r.Use(t11AuthContext(tc.role, tc.systemAdmin))
			r.GET("/auth/me", h.GetCurrentUser)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
			require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

			var payload t11AuthMePayload
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			require.NotNil(t, payload.Data.Capabilities.CanCreateTenant,
				"response missing data.capabilities.can_create_tenant: %s", w.Body.String())
			require.Equal(t, tc.want, *payload.Data.Capabilities.CanCreateTenant,
				"can_create_tenant mismatch: body=%s", w.Body.String())
		})
	}
}

// t11LoginUserService 只实现登录用例需要的方法。
type t11LoginUserService struct {
	interfaces.UserService
	user *types.User
}

func (s *t11LoginUserService) Login(context.Context, *types.LoginRequest) (*types.LoginResponse, error) {
	return &types.LoginResponse{
		Success:     true,
		User:        s.user,
		Memberships: []types.Membership{},
	}, nil
}

// t11LoginPayload 只取 capabilities。
type t11LoginPayload struct {
	Capabilities struct {
		CanCreateTenant *bool `json:"can_create_tenant"`
	} `json:"capabilities"`
}

// TestLoginCreateTenantCapability 锁定登录响应与 /auth/me 同口径，
// 避免「登录后能看到入口、刷新后消失」的闪烁（AC-44）。
func TestLoginCreateTenantCapability(t *testing.T) {
	cases := []struct {
		name               string
		systemAdmin        bool
		selfServiceEnabled bool
		want               bool
	}{
		{"owner_self_service_enabled", false, true, false},
		{"system_admin_self_service_disabled", true, false, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			user := &types.User{
				ID:            "user-" + tc.name,
				IsSystemAdmin: tc.systemAdmin,
			}
			h := &AuthHandler{
				userService:      &t11LoginUserService{user: user},
				configInfo:       &config.Config{Tenant: &config.TenantConfig{}},
				systemSettingSvc: &tenantPolicySettingService{enabled: tc.selfServiceEnabled},
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(middleware.ErrorHandler())
			r.POST("/auth/login", h.Login)

			w := httptest.NewRecorder()
			body := `{"email":"user@example.com","password":"secret123"}`
			req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

			var payload t11LoginPayload
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			require.NotNil(t, payload.Capabilities.CanCreateTenant,
				"login response missing capabilities.can_create_tenant: %s", w.Body.String())
			require.Equal(t, tc.want, *payload.Capabilities.CanCreateTenant,
				"login can_create_tenant mismatch: body=%s", w.Body.String())
		})
	}
}
