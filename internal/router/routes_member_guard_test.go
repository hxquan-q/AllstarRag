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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T01 (gpt-review-fixes) route-guard tests for the two member-management
// entries that create accounts / reset passwords:
//
//	POST /tenants/:id/members/create
//	POST /tenants/:id/members/:user_id/reset-password
//
// The handler layer already treats platform system admins as Owner
// (tenant_member.go: "系统管理员按 owner 处理"), so a system admin whose
// role in the CURRENT workspace is viewer/contributor must not be bounced
// by the route guard — otherwise the handler-level matrix is unreachable
// for exactly the callers it was written for.
//
// These tests register the REAL route tree via RegisterTenantRoutes (same
// guard chain as production: g.PathTenantMatch() on the /:id group plus
// the per-route role guard) and fire actual HTTP requests, so they turn
// green only when the real route declaration changes.
//
// Observable contract:
//
//	guard rejects  -> 403 (written directly by the RBAC middleware)
//	guard passes   -> 400 (request reaches the real handler, whose
//	                     ShouldBindJSON on the deliberately-empty body
//	                     fails and ErrorHandler renders the validation
//	                     error; no service call, no nil-service panic)
//
// RBAC enforcement is explicitly ON: RequireRole fails open when
// EnableRBAC is off, which would make every case pass and hide the bug.

const memberGuardTenantID uint64 = 7

// memberGuardSession mirrors what middleware/auth.go's JWT path attaches
// (via applyAuthSession) for a caller inside tenant 7: workspace role,
// user id, and the platform system-admin flag. Guards read these from the
// request context, so seeding the context is enough (same style as
// rbacTestHarness in internal/middleware/rbac_test.go).
type memberGuardSession struct {
	role        types.TenantRole
	userID      string
	systemAdmin bool
}

// memberGuardRouter builds a fresh engine with the real tenant route tree.
// A fresh engine per case keeps the seed middleware a simple closure over
// the session struct. EnableCrossTenantAccess stays off so no caller can
// accidentally trip the cross-tenant superuser bypass in RequireRole /
// RequirePathTenantMatch — this test is about the system-admin path only.
func memberGuardRouter(t *testing.T, s memberGuardSession) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, memberGuardTenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	// invitation/audit handlers are nil: their subroutes register
	// conditionally (same nil-skip pattern as production wiring), and the
	// two member routes under test live under `memberHandler != nil`.
	RegisterTenantRoutes(
		engine.Group("/api/v1"),
		&handler.TenantHandler{},
		&handler.TenantMemberHandler{},
		nil,
		nil,
		g,
	)
	return engine
}

// postMemberRoute fires the POST with a body that fails the handler's
// JSON binding, so "guard passed" surfaces as a deterministic 400 without
// invoking any (zero-value) service dependency.
func postMemberRoute(t *testing.T, s memberGuardSession, path string) int {
	t.Helper()
	r := memberGuardRouter(t, s)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code
}

func TestMemberGuardCreateMemberAccountRoute(t *testing.T) {
	const path = "/api/v1/tenants/7/members/create"

	// A: platform system admin whose workspace role is viewer/contributor
	// must reach the handler (which then applies the "system admin acts as
	// owner" matrix). Currently red: the route guard is Admin-only and
	// drops these callers before the handler's own rules can run.
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("system_admin_with_"+string(role)+"_reaches_handler", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-sysadmin", systemAdmin: true}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass the route guard and reach the handler (403 means the guard rejected them)", role)
		})
	}

	// B: ordinary viewer/contributor (not a system admin) must stay 403.
	// This is also the canary proving RBAC enforcement is really ON in
	// this harness — if it ever turns non-403 the config silently
	// fail-opened and cases A/C prove nothing.
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("ordinary_"+string(role)+"_rejected_403", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-ordinary"}, path)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the Admin+ route guard", role)
		})
	}

	// C: workspace admin/owner keep passing (no regression in the matrix).
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run("workspace_"+string(role)+"_passes", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-admin"}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace %s must keep passing the route guard", role)
		})
	}
}

func TestMemberGuardResetMemberPasswordRoute(t *testing.T) {
	// Target user id differs from every seeded caller id so the handler's
	// "cannot reset your own password" pre-check cannot short-circuit.
	const path = "/api/v1/tenants/7/members/u-guard-target/reset-password"

	// A: platform system admin with a low workspace role must reach the
	// handler (its own matrix grants owner-level reset rights).
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("system_admin_with_"+string(role)+"_reaches_handler", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-sysadmin", systemAdmin: true}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"system admin with workspace role %s must pass the route guard and reach the handler (403 means the guard rejected them)", role)
		})
	}

	// B: ordinary viewer/contributor stays 403.
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		t.Run("ordinary_"+string(role)+"_rejected_403", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-ordinary"}, path)
			require.Equal(t, http.StatusForbidden, code,
				"ordinary %s must be rejected by the Admin+ route guard", role)
		})
	}

	// C: workspace admin/owner keep passing.
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run("workspace_"+string(role)+"_passes", func(t *testing.T) {
			code := postMemberRoute(t, memberGuardSession{role: role, userID: "u-admin"}, path)
			require.Equal(t, http.StatusBadRequest, code,
				"workspace %s must keep passing the route guard", role)
		})
	}
}
