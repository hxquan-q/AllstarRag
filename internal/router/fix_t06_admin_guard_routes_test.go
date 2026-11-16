package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T06 红阶段（真实 gin 路由）：普通管理员（admin）拿到 co-owner 能力集，
// 成员治理路由由 OwnerOrSystemAdmin 放宽为 AdminOrSystemAdmin。
//
// 断言方式：
//   - 需要 JSON body 的入口：故意发非法 JSON，守卫放行后 handler 会以 400
//     结束（不会触达 service），守卫拒绝则是 403；
//   - 无 body 的入口：stub service 返回成功，放行后 200，拒绝 403；
//   - owner-only 入口（transfer-owner、租户删除、API Key）保持 403；
//   - viewer 金丝雀保持 403，证明 RBAC 强制确实开启。

type t06RouterMemberService struct {
	interfaces.TenantMemberService
}

func (s *t06RouterMemberService) RemoveMember(
	_ context.Context, _ string, _ uint64,
) error {
	return nil
}

type t06RouterUserService struct {
	interfaces.UserService
}

type t06RouterInvitationService struct {
	interfaces.TenantInvitationService
}

func (s *t06RouterInvitationService) GetByID(
	_ context.Context, invID uint64,
) (*types.TenantInvitation, error) {
	return &types.TenantInvitation{ID: invID, TenantID: 7}, nil
}

func (s *t06RouterInvitationService) Revoke(_ context.Context, _ uint64) error {
	return nil
}

type t06RouterAuditService struct {
	interfaces.AuditLogService
}

func (s *t06RouterAuditService) List(
	_ context.Context, _ uint64, _ *interfaces.AuditLogQuery,
) ([]*types.AuditLog, error) {
	return []*types.AuditLog{}, nil
}

type t06RouterUMService struct {
	interfaces.UserManagementService
}

func (s *t06RouterUMService) ListUsers(
	_ context.Context, _ uint64, _ types.TenantRole, _ string, _, _ int,
) (*interfaces.UserManagementListResult, error) {
	return &interfaces.UserManagementListResult{}, nil
}

func t06RouterEngine(t *testing.T, role types.TenantRole, systemAdmin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "u-actor")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	memberHandler := handler.NewTenantMemberHandler(
		&t06RouterMemberService{}, &t06RouterUserService{})
	invitationHandler := handler.NewTenantInvitationHandler(
		&t06RouterInvitationService{}, &t06RouterUserService{}, nil, nil, nil, nil)
	auditHandler := handler.NewAuditLogHandler(&t06RouterAuditService{})
	umHandler := handler.NewUserManagementHandler(
		&t06RouterUMService{}, &t06RouterMemberService{})

	RegisterTenantRoutes(engine.Group("/api/v1"),
		&handler.TenantHandler{}, memberHandler, invitationHandler, auditHandler, g)
	RegisterUserManagementRoutes(engine.Group("/api/v1"), umHandler, g)
	return engine
}

func t06RouterDo(
	t *testing.T, engine *gin.Engine, method, path string, withBody bool,
) int {
	t.Helper()
	var body *strings.Reader
	if withBody {
		body = strings.NewReader("not-json")
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w.Code
}

func TestT06AdminMemberGovernanceRoutesPassGuard(t *testing.T) {
	cases := []struct {
		method   string
		path     string
		withBody bool
		want     int
	}{
		{"POST", "/api/v1/tenants/7/members", true, http.StatusBadRequest},
		{"POST", "/api/v1/tenants/7/members/create", true, http.StatusBadRequest},
		{"POST", "/api/v1/tenants/7/members/u-target/reset-password", true, http.StatusBadRequest},
		{"PUT", "/api/v1/tenants/7/members/u-target", true, http.StatusBadRequest},
		{"DELETE", "/api/v1/tenants/7/members/u-target", false, http.StatusOK},
		{"POST", "/api/v1/tenants/7/invitations", true, http.StatusBadRequest},
		{"DELETE", "/api/v1/tenants/7/invitations/5", false, http.StatusOK},
		{"POST", "/api/v1/tenants/7/invite-links", true, http.StatusBadRequest},
		{"GET", "/api/v1/tenants/7/user-management", false, http.StatusOK},
		{"GET", "/api/v1/tenants/7/audit-log", false, http.StatusOK},
		{"POST", "/api/v1/tenants/7/members/u-target/ban", true, http.StatusBadRequest},
		{"POST", "/api/v1/tenants/7/members/u-target/unban", true, http.StatusBadRequest},
		{"POST", "/api/v1/tenants/7/members/u-target/purge", true, http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code := t06RouterDo(t, t06RouterEngine(t, types.TenantRoleAdmin, false),
				tc.method, tc.path, tc.withBody)
			require.Equal(t, tc.want, code,
				"ordinary admin must pass the AdminOrSystemAdmin guard and reach the handler "+
					"(403 means the route is still owner-only)")
		})
	}
}

func TestT06OwnerOnlyRoutesStayOwnerOnly(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/tenants/7/transfer-owner"},
		{"DELETE", "/api/v1/tenants/7"},
		{"POST", "/api/v1/tenants/7/api-keys"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code := t06RouterDo(t, t06RouterEngine(t, types.TenantRoleAdmin, false),
				tc.method, tc.path, true)
			require.Equal(t, http.StatusForbidden, code,
				"%s must remain owner-only", tc.path)
		})
	}
}

func TestT06ViewerStillRejectedOnMemberGovernance(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/tenants/7/members"},
		{"DELETE", "/api/v1/tenants/7/members/u-target"},
		{"GET", "/api/v1/tenants/7/user-management"},
		{"GET", "/api/v1/tenants/7/audit-log"},
	} {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code := t06RouterDo(t, t06RouterEngine(t, types.TenantRoleViewer, false),
				tc.method, tc.path, tc.method != "GET")
			require.Equal(t, http.StatusForbidden, code,
				"viewers must stay rejected after widening to admin")
		})
	}
}
