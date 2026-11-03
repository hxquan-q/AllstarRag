package handler

// 整体回归（T02 + AC-7/10/11/12/17）：共享链接创建选项与列表投影。
//
// 链路使用真实 sqlite + 真实 service/repository；用户目录、租户名用最小桩。
// 与 T03 配额测试共用 quotaTestUserService / quotaTestTenantService，避免重复
// 造轮子；本文件只补创建接口的请求校验、持久化、响应与列表字段。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

const shareLinkTestTenantID uint64 = 901

// shareLinkUserService 在最小用户目录上补列表投影需要的批量查询。
type shareLinkUserService struct {
	*quotaTestUserService
}

func (s *shareLinkUserService) GetUsersByIDs(
	_ context.Context, _ []string,
) (map[string]*types.User, error) {
	return map[string]*types.User{}, nil
}

type shareLinkTestEnv struct {
	db     *gorm.DB
	users  *shareLinkUserService
	router *gin.Engine
}

// newShareLinkTestEnv 装配创建 / 列表两条路由。权限中间件不在本测试范围
// （owner 门禁由 routes_auth_tenant.go 的 g.Owner() 负责，另有路由接线测试），
// 但列表投影需要 Owner 角色才返回 invite_url，故在上下文里写入角色。
func newShareLinkTestEnv(t *testing.T) *shareLinkTestEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}, &types.TenantInvitation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	invRepo := apprepo.NewTenantInvitationRepository(db)
	memberSvc := service.NewTenantMemberService(apprepo.NewTenantMemberRepository(db), nil, nil, nil)
	invSvc := service.NewTenantInvitationService(invRepo, memberSvc, nil)
	users := &shareLinkUserService{quotaTestUserService: newQuotaTestUserService()}
	tenantSvc := &quotaTestTenantService{}

	h := &TenantInvitationHandler{
		invitationService: invSvc,
		userService:       users,
		tenantService:     tenantSvc,
		memberService:     memberSvc,
		configInfo:        &config.Config{},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(
			c.Request.Context(), types.TenantRoleContextKey, types.TenantRoleOwner)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, errorCapture())
	r.POST("/tenants/:id/invite-links", h.CreateInviteLink)
	r.GET("/tenants/:id/invitations", h.ListTenantInvitations)
	return &shareLinkTestEnv{db: db, users: users, router: r}
}

func (e *shareLinkTestEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

type createShareLinkResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID               uint64    `json:"id"`
		ExpiresAt        time.Time `json:"expires_at"`
		MaxRegistrations *int      `json:"max_registrations"`
		RegisteredCount  int       `json:"registered_count"`
		InviteURL        string    `json:"invite_url"`
		IsShareLink      bool      `json:"is_share_link"`
	} `json:"data"`
}

func decodeCreate(t *testing.T, w *httptest.ResponseRecorder) createShareLinkResponse {
	t.Helper()
	var out createShareLinkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create response: %v (body=%s)", err, w.Body.String())
	}
	return out
}

const shareLinkPath = "/tenants/901/invite-links"

// AC-10：人数必须为 1–1000 的整数；非法值由服务端拒绝。
func TestT02_CreateInviteLinkRejectsInvalidMaxRegistrations(t *testing.T) {
	cases := map[string]string{
		"zero":        `{"role":"viewer","max_registrations":0}`,
		"negative":    `{"role":"viewer","max_registrations":-3}`,
		"over_limit":  `{"role":"viewer","max_registrations":1001}`,
		"non_integer": `{"role":"viewer","max_registrations":1.5}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			env := newShareLinkTestEnv(t)
			w := env.do(t, http.MethodPost, shareLinkPath, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("非法人数 status=%d, want 400 (body=%s)", w.Code, w.Body.String())
			}
		})
	}
}

// AC-11：过期时间必须晚于当前时间。
func TestT02_CreateInviteLinkRejectsPastExpiry(t *testing.T) {
	env := newShareLinkTestEnv(t)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	body := fmt.Sprintf(`{"role":"viewer","expires_at":%q}`, past)
	w := env.do(t, http.MethodPost, shareLinkPath, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("过去时间 status=%d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

// AC-10/11/17：合法选项落库，响应带出 max_registrations / registered_count。
func TestT02_CreateInviteLinkPersistsOptionsAndEchoesFields(t *testing.T) {
	env := newShareLinkTestEnv(t)
	wantExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	body := fmt.Sprintf(`{"role":"viewer","max_registrations":5,"expires_at":%q}`,
		wantExpiry.Format(time.RFC3339))
	w := env.do(t, http.MethodPost, shareLinkPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("合法创建 status=%d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	out := decodeCreate(t, w)
	if out.Data.MaxRegistrations == nil || *out.Data.MaxRegistrations != 5 {
		t.Fatalf("响应 max_registrations=%v, want 5", out.Data.MaxRegistrations)
	}
	if out.Data.RegisteredCount != 0 {
		t.Fatalf("新链接 registered_count=%d, want 0", out.Data.RegisteredCount)
	}
	if !out.Data.IsShareLink || out.Data.InviteURL == "" {
		t.Fatalf("共享链接响应缺少 is_share_link/invite_url: %+v", out.Data)
	}
	if !out.Data.ExpiresAt.Truncate(time.Second).Equal(wantExpiry) {
		t.Fatalf("响应 expires_at=%s, want %s", out.Data.ExpiresAt, wantExpiry)
	}

	var row types.TenantInvitation
	if err := env.db.First(&row, out.Data.ID).Error; err != nil {
		t.Fatalf("读取落库行失败: %v", err)
	}
	if row.MaxRegistrations == nil || *row.MaxRegistrations != 5 {
		t.Fatalf("落库 max_registrations=%v, want 5", row.MaxRegistrations)
	}
	if row.RegisteredCount != 0 {
		t.Fatalf("落库 registered_count=%d, want 0", row.RegisteredCount)
	}
	if !row.ExpiresAt.Truncate(time.Second).Equal(wantExpiry) {
		t.Fatalf("落库 expires_at=%s, want %s", row.ExpiresAt, wantExpiry)
	}
}

// AC-10：不传人数时表示不限；响应不出现具体上限、计数为 0。
func TestT02_CreateInviteLinkDefaultsToUnlimited(t *testing.T) {
	env := newShareLinkTestEnv(t)
	w := env.do(t, http.MethodPost, shareLinkPath, `{"role":"viewer"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("缺省选项创建 status=%d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	out := decodeCreate(t, w)
	if out.Data.MaxRegistrations != nil {
		t.Fatalf("缺省 max_registrations=%v, want nil（不限）", *out.Data.MaxRegistrations)
	}
	if out.Data.RegisteredCount != 0 {
		t.Fatalf("缺省 registered_count=%d, want 0", out.Data.RegisteredCount)
	}
	if !out.Data.ExpiresAt.After(time.Now()) {
		t.Fatalf("缺省 expires_at=%s, want 未来时间", out.Data.ExpiresAt)
	}
}

// AC-10：角色仍为必填，非法请求体被拒绝。
func TestT02_CreateInviteLinkRejectsMissingRole(t *testing.T) {
	env := newShareLinkTestEnv(t)
	w := env.do(t, http.MethodPost, shareLinkPath, `{"max_registrations":5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 role status=%d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

// AC-17：列表投影同时带出「已注册 X/Y」与「已加入 N」所需的字段。
func TestT02_ListInvitationsProjectsRegisteredAndAcceptedCounts(t *testing.T) {
	env := newShareLinkTestEnv(t)
	max := 10
	row := &types.TenantInvitation{
		TenantID:         shareLinkTestTenantID,
		Token:            "list-share-token",
		Role:             types.TenantRoleViewer,
		Status:           types.TenantInvitationStatusPending,
		ExpiresAt:        time.Now().Add(time.Hour),
		MaxRegistrations: &max,
		RegisteredCount:  3,
		AcceptedCount:    7,
	}
	if err := env.db.Create(row).Error; err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	w := env.do(t, http.MethodGet, "/tenants/901/invitations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表 status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Invitations []types.TenantInvitationResponse `json:"invitations"`
			Total       int                              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if out.Data.Total != 1 || len(out.Data.Invitations) != 1 {
		t.Fatalf("列表行数 total=%d len=%d, want 1", out.Data.Total, len(out.Data.Invitations))
	}
	got := out.Data.Invitations[0]
	if got.MaxRegistrations == nil || *got.MaxRegistrations != 10 {
		t.Fatalf("列表 max_registrations=%v, want 10", got.MaxRegistrations)
	}
	if got.RegisteredCount != 3 {
		t.Fatalf("列表 registered_count=%d, want 3", got.RegisteredCount)
	}
	if got.AcceptedCount != 7 {
		t.Fatalf("列表 accepted_count=%d, want 7", got.AcceptedCount)
	}
	if !got.IsShareLink || got.InviteURL == "" {
		t.Fatalf("列表共享链接缺少 is_share_link/invite_url: %+v", got)
	}
}

// AC-7：撤销 / 过期在 lookup 与注册两处都统一为 410，不暴露具体原因。
func TestOverall_RevokedAndExpiredLinksReturnGone(t *testing.T) {
	env := newQuotaTestEnv(t)
	revoked := &types.TenantInvitation{
		TenantID:  quotaTestTenantID,
		Token:     "revoked-token",
		Role:      types.TenantRoleViewer,
		Status:    types.TenantInvitationStatusRevoked,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	expired := &types.TenantInvitation{
		TenantID:  quotaTestTenantID,
		Token:     "expired-token",
		Role:      types.TenantRoleViewer,
		Status:    types.TenantInvitationStatusPending,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	for _, inv := range []*types.TenantInvitation{revoked, expired} {
		if err := env.db.Create(inv).Error; err != nil {
			t.Fatalf("seed %s: %v", inv.Token, err)
		}
	}
	for _, token := range []string{"revoked-token", "expired-token"} {
		lookup := env.post(t, "/auth/invitations/lookup", "",
			fmt.Sprintf(`{"token":%q}`, token))
		if lookup.Code != http.StatusGone {
			t.Fatalf("%s lookup status=%d, want 410 (body=%s)",
				token, lookup.Code, lookup.Body.String())
		}
		reg := env.post(t, "/auth/register-by-invite", "",
			quotaRegisterBody(token, token+"@example.com"))
		if reg.Code != http.StatusGone {
			t.Fatalf("%s 注册 status=%d, want 410 (body=%s)",
				token, reg.Code, reg.Body.String())
		}
	}
	if got := env.users.count(); got != 0 {
		t.Fatalf("失效链接不应建号, got %d 个账号", got)
	}
}
