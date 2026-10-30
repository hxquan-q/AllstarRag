package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// security_t12_revocation_lifecycle_test.go 是 T12 对 R1-1 的独立复验:
// 软删成员行的来源不同,access_revoked 的结论也必须不同。
//  1. 删除空间:tenant 软删 + 成员级联软删 -> 原成员不再被撤销,可重建空间;
//  2. 被 owner 移出(空间存活)-> 仍撤销,创建 403;
//  3. 主动退出(空间存活)-> 当前保守拦截(已知语义,不是缺陷)。
// 前两条用真实 repo+service+sqlite 驱动,不依赖实现方新增的单元断言。

func t12NewLifecycleFixture(t *testing.T) (interfaces.TenantMemberService, interfaces.TenantRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// :memory: 每个连接都是独立库;固定单连接,避免连接池切换导致表丢失。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	memberRepo := apprepo.NewTenantMemberRepository(db)
	tenantRepo := apprepo.NewTenantRepository(db)
	return service.NewTenantMemberService(memberRepo, nil, nil, nil), tenantRepo, db
}

func t12CreateTenantRow(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	if err := db.Create(&types.Tenant{ID: id, Name: "t12-workspace", Status: "active"}).Error; err != nil {
		t.Fatalf("create tenant %d: %v", id, err)
	}
}

func t12AddActiveMember(t *testing.T, db *gorm.DB, userID string, tenantID uint64, role types.TenantRole) {
	t.Helper()
	member := &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create membership %s@%d: %v", userID, tenantID, err)
	}
}

func t12NewCreateTenantHandler(memberSvc interfaces.TenantMemberService, userID string, tenants interfaces.TenantService) *TenantHandler {
	return &TenantHandler{
		service:          tenants,
		userService:      &revokedTenantUserService{user: &types.User{ID: userID}},
		memberService:    memberSvc,
		config:           &config.Config{Tenant: &config.TenantConfig{}},
		systemSettingSvc: &tenantPolicySettingService{enabled: true},
	}
}

func t12TenantAlive(t *testing.T, db *gorm.DB, tenantID uint64) bool {
	t.Helper()
	var tenant types.Tenant
	err := db.Unscoped().Where("id = ?", tenantID).First(&tenant).Error
	if err != nil {
		t.Fatalf("load tenant %d: %v", tenantID, err)
	}
	return !tenant.DeletedAt.Valid
}

// ① 删除空间(tenant 软删 + 成员级联软删):原成员 access_revoked=false,可再次创建。
func TestT12_R1_1_DeletedWorkspaceAllowsRecreate(t *testing.T) {
	ctx := context.Background()
	memberSvc, tenantRepo, db := t12NewLifecycleFixture(t)
	const user = "t12-deleted-owner"

	t12CreateTenantRow(t, db, 1)
	t12AddActiveMember(t, db, user, 1, types.TenantRoleOwner)

	// 删除前:活跃成员存在,不属于撤销。
	if revoked, err := memberSvc.HasRevokedAccess(ctx, user); err != nil || revoked {
		t.Fatalf("before delete: revoked=%v err=%v, want false/nil", revoked, err)
	}

	// 走真实的仓库删除路径:同一事务内级联软删成员 + 软删空间。
	if err := tenantRepo.DeleteTenant(ctx, 1); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	if t12TenantAlive(t, db, 1) {
		t.Fatal("fixture invalid: tenant 1 should be soft-deleted")
	}
	var cascadeDeleted int64
	if err := db.Unscoped().
		Model(&types.TenantMember{}).
		Where("tenant_id = ? AND deleted_at IS NOT NULL", 1).
		Count(&cascadeDeleted).Error; err != nil {
		t.Fatalf("count cascade rows: %v", err)
	}
	if cascadeDeleted != 1 {
		t.Fatalf("cascade soft-deleted memberships = %d, want 1", cascadeDeleted)
	}

	// 删除空间后:软删行不再是「被移出」的证据,不能被判撤销。
	revoked, err := memberSvc.HasRevokedAccess(ctx, user)
	if err != nil {
		t.Fatalf("HasRevokedAccess after delete: %v", err)
	}
	if revoked {
		t.Fatal("after deleting the workspace: revoked=true, want false (cascade must not count as removal)")
	}

	// 真实 handler 路径:可以再次创建空间,并自动成为新空间 Owner。
	tenants := &tenantPolicyTenantService{}
	h := t12NewCreateTenantHandler(memberSvc, user, tenants)
	w := t09PostTenant(t, newT09CreateTenantRouter(h), `{"name":"rebuilt-workspace"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("recreate after delete: status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 1 {
		t.Fatalf("CreateTenant calls = %d, want 1", tenants.createCalls)
	}
	memberships, err := memberSvc.ListByUser(ctx, user)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(memberships) != 1 || memberships[0].TenantID != 99 || memberships[0].Role != types.TenantRoleOwner {
		t.Fatalf("new owner membership = %+v, want one owner row on tenant 99", memberships)
	}
}

// ② 被 owner 移出但空间存活:仍判撤销,创建 403。
func TestT12_R1_1_OwnerRemovalStillRevokes(t *testing.T) {
	ctx := context.Background()
	memberSvc, _, db := t12NewLifecycleFixture(t)
	const owner = "t12-owner"
	const viewer = "t12-removed-viewer"

	t12CreateTenantRow(t, db, 2)
	t12AddActiveMember(t, db, owner, 2, types.TenantRoleOwner)
	t12AddActiveMember(t, db, viewer, 2, types.TenantRoleViewer)

	// 空间存活时被移出(LeaveTenant/RemoveMember 的同一服务路径)。
	if err := memberSvc.RemoveMember(ctx, viewer, 2); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if !t12TenantAlive(t, db, 2) {
		t.Fatal("fixture invalid: tenant 2 must stay alive")
	}

	revoked, err := memberSvc.HasRevokedAccess(ctx, viewer)
	if err != nil {
		t.Fatalf("HasRevokedAccess: %v", err)
	}
	if !revoked {
		t.Fatal("removed from a live workspace: revoked=false, want true")
	}

	tenants := &tenantPolicyTenantService{}
	h := t12NewCreateTenantHandler(memberSvc, viewer, tenants)
	w := t09PostTenant(t, newT09CreateTenantRouter(h), `{"name":"should-not-exist"}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if tenants.createCalls != 0 {
		t.Fatalf("CreateTenant calls = %d, want 0", tenants.createCalls)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte(`"details":"access_revoked"`)) {
		t.Fatalf("403 body missing details access_revoked: %s", body)
	}
}

// ③ 主动退出(空间存活):当前保守拦截。记录为已知语义,不是 FAIL。
func TestT12_R1_1_SelfLeaveConservativeBlockKnownSemantics(t *testing.T) {
	ctx := context.Background()
	memberSvc, _, db := t12NewLifecycleFixture(t)
	const leaver = "t12-leaver"

	t12CreateTenantRow(t, db, 3)
	t12AddActiveMember(t, db, leaver, 3, types.TenantRoleViewer)

	// 走真实 handler:POST /tenants/3/leave。
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		reqCtx := context.WithValue(c.Request.Context(), types.UserIDContextKey, leaver)
		c.Request = c.Request.WithContext(reqCtx)
		c.Next()
	})
	memberHandler := &TenantMemberHandler{memberService: memberSvc}
	router.POST("/tenants/:id/leave", memberHandler.LeaveTenant)

	req := httptest.NewRequest(http.MethodPost, "/tenants/3/leave", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("leave: status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !t12TenantAlive(t, db, 3) {
		t.Fatal("fixture invalid: tenant 3 must stay alive after leave")
	}

	// 已知语义(OQ):空间存活 + 成员软删 => 当前仍判撤销(保守拦截)。
	revoked, err := memberSvc.HasRevokedAccess(ctx, leaver)
	if err != nil {
		t.Fatalf("HasRevokedAccess after leave: %v", err)
	}
	if !revoked {
		t.Fatalf("conservative policy changed: revoked=false after self-leave; " +
			"if product has decided to allow recreate, update this characterization test")
	}

	tenants := &tenantPolicyTenantService{}
	h := t12NewCreateTenantHandler(memberSvc, leaver, tenants)
	cw := t09PostTenant(t, newT09CreateTenantRouter(h), `{"name":"after-leave"}`, nil)
	if cw.Code != http.StatusForbidden {
		t.Fatalf("conservative policy changed: create status = %d, want 403 (body=%s)", cw.Code, cw.Body.String())
	}
	t.Log("已知语义:主动退出空间后仍被保守拦截(业务待拍板,非本轮缺陷)")
}
