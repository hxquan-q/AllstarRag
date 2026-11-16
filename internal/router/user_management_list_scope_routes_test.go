package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T16 路由层红阶段用例（T16-1/6/9、AC-38、C4）。
//
// 与 T05 路由测试用「非法参数 400」当守卫信号不同：scope=other/all 的 403
// 判权在 service 层，本文件构造真实 user management service（sqlite），
// 走完整 router → handler → service 链路断言：
//   - 路由注册与守卫不变（现有 OwnerOrSystemAdmin + PathTenantMatch）；
//   - owner（非系统管理员）请求 scope=other/all → 403；
//   - 系统管理员（工作区角色 viewer）请求 scope=other → 200，
//     items[].memberships 带 tenant_id/tenant_name/role；
//   - 不带 scope 的既有请求 → 200（零破坏）。

const (
	umScopeTenantA uint64 = 10
	umScopeTenantB uint64 = 20
)

// umScopeRouteEnv 持有真实 service 的路由引擎 + 种子用的 db。
type umScopeRouteEnv struct {
	engine *gin.Engine
	db     *gorm.DB
}

// newUMScopeRouteEnv 构造带真实 user management service 的路由引擎。
func newUMScopeRouteEnv(t *testing.T, s umRouteSession) *umScopeRouteEnv {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "um_scope_routes.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&types.User{}, &types.Tenant{}, &types.TenantMember{}, &types.AuditLog{}))

	for _, tn := range []struct {
		id   uint64
		name string
	}{{umScopeTenantA, "空间A"}, {umScopeTenantB, "空间B"}} {
		require.NoError(t, db.Create(&types.Tenant{
			ID: tn.id, Name: tn.name, Status: "active",
		}).Error)
	}

	userRepo := apprepo.NewUserRepository(db)
	memberRepo := apprepo.NewTenantMemberRepository(db)
	auditSvc := service.NewAuditLogService(apprepo.NewAuditLogRepository(db))
	umSvc := service.NewUserManagementService(userRepo, memberRepo, auditSvc)
	umHandler := handler.NewUserManagementHandler(umSvc, nil)

	gin.SetMode(gin.TestMode)
	rbacOn := true
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &rbacOn}}}

	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		tenantID := s.tenantID
		if tenantID == 0 {
			tenantID = umScopeTenantA
		}
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, s.userID)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, s.role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, s.systemAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	RegisterUserManagementRoutes(engine.Group("/api/v1"), umHandler, g)
	return &umScopeRouteEnv{engine: engine, db: db}
}

// seedMember 种一个用户 + 一条 active 成员关系（types.User 无 GORM hook，
// 直接建行与仓储路径等价）。
func (e *umScopeRouteEnv) seedMember(
	t *testing.T, userID, email, username string, tenantID uint64, role types.TenantRole,
) {
	t.Helper()
	require.NoError(t, e.db.Create(&types.User{
		ID:           userID,
		Username:     username,
		Email:        email,
		PasswordHash: "hash-" + userID,
		IsActive:     true,
	}).Error)
	require.NoError(t, e.db.Create(&types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: time.Now(),
	}).Error)
}

func umScopeDoGet(t *testing.T, e *umScopeRouteEnv, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	e.engine.ServeHTTP(w, req)
	return w
}

// umScopeRouteMembership / umScopeRouteItem / umScopeRoutePayload 解析路由层
// 响应中的 memberships。
type umScopeRouteMembership struct {
	TenantID   uint64 `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Role       string `json:"role"`
}

type umScopeRouteItem struct {
	UserID      string                   `json:"user_id"`
	Memberships []umScopeRouteMembership `json:"memberships"`
}

type umScopeRoutePayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []umScopeRouteItem `json:"items"`
		Total int64              `json:"total"`
	} `json:"data"`
}

// TestUserManagementListScopeRouteCurrentStillWorks 锁定 T16-9：不带 scope
// 的既有请求在完整链路上仍 200，行为不回归。
func TestUserManagementListScopeRouteCurrentStillWorks(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner-a",
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-viewer-a", "viewer-a@example.com", "viewer-a",
		umScopeTenantA, types.TenantRoleViewer)

	w := umScopeDoGet(t, env, "/api/v1/tenants/10/user-management")
	require.Equal(t, http.StatusOK, w.Code,
		"不带 scope 的既有请求必须保持 200（零破坏）")

	var payload umScopeRoutePayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, int64(2), payload.Data.Total)
}

// TestUserManagementListScopeRouteOwnerForbiddenForOtherAndAll 锁定 T16-6：
// owner（非系统管理员）请求 scope=other/all 一律 403，判权在 service 层。
func TestUserManagementListScopeRouteOwnerForbiddenForOtherAndAll(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleOwner, userID: "u-owner-a",
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-only-b", "only-b@example.com", "only-b",
		umScopeTenantB, types.TenantRoleViewer)

	for _, scope := range []string{"other", "all"} {
		scope := scope
		t.Run(scope, func(t *testing.T) {
			w := umScopeDoGet(t, env, "/api/v1/tenants/10/user-management?scope="+scope)
			require.Equal(t, http.StatusForbidden, w.Code,
				"owner（非系统管理员）请求 scope=%s 必须 403", scope)
		})
	}
}

// TestUserManagementListScopeRouteSystemAdminSeesOtherSpaces 锁定 T16-3/6：
// 系统管理员（工作区角色 viewer）scope=other → 200，返回其它空间成员且
// memberships 带 tenant_id/tenant_name/role。
func TestUserManagementListScopeRouteSystemAdminSeesOtherSpaces(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleViewer, userID: "u-sysadmin", systemAdmin: true,
	})
	env.seedMember(t, "u-owner-a", "owner-a@example.com", "owner-a",
		umScopeTenantA, types.TenantRoleOwner)
	env.seedMember(t, "u-owner-b", "owner-b@example.com", "owner-b",
		umScopeTenantB, types.TenantRoleOwner)
	env.seedMember(t, "u-only-b", "only-b@example.com", "only-b",
		umScopeTenantB, types.TenantRoleViewer)

	w := umScopeDoGet(t, env, "/api/v1/tenants/10/user-management?scope=other")
	require.Equal(t, http.StatusOK, w.Code,
		"系统管理员即使工作区角色为 viewer 也必须能看 other 视图")

	var payload umScopeRoutePayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, int64(2), payload.Data.Total,
		"other 视图只含空间 B 的两个成员，不含本空间 owner")

	var onlyB *umScopeRouteItem
	for i := range payload.Data.Items {
		if payload.Data.Items[i].UserID == "u-only-b" {
			onlyB = &payload.Data.Items[i]
		}
	}
	require.NotNil(t, onlyB, "u-only-b 必须出现在 other 视图")
	require.Len(t, onlyB.Memberships, 1)
	require.Equal(t, umScopeTenantB, onlyB.Memberships[0].TenantID)
	require.Equal(t, "空间B", onlyB.Memberships[0].TenantName)
	require.Equal(t, "viewer", onlyB.Memberships[0].Role)
}

// TestUserManagementListScopeRouteAdminBlockedByGuard 锁定普通 admin 连路由
// 守卫都过不了（OwnerOrSystemAdmin 不因 scope 放松）。
func TestUserManagementListScopeRouteAdminBlockedByGuard(t *testing.T) {
	env := newUMScopeRouteEnv(t, umRouteSession{
		role: types.TenantRoleAdmin, userID: "u-admin-a",
	})
	env.seedMember(t, "u-admin-a", "admin-a@example.com", "admin-a",
		umScopeTenantA, types.TenantRoleAdmin)

	w := umScopeDoGet(t, env, "/api/v1/tenants/10/user-management?scope=other")
	require.Equal(t, http.StatusForbidden, w.Code,
		"非 owner 非超管的 scope=other 必须被 OwnerOrSystemAdmin 守卫拒绝")
}
