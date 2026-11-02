package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tenant_invitation_rejoin_test.go(T02)用真实 sqlite + 真实 service/repo
// 锁定「被移除成员经合法邀请恢复」的后端链路,两条通道各一个用例:
//  1. per-user 邀请:owner POST /tenants/:id/invitations(非 auto_accept)
//     → 本人 POST /me/invitations/:inv_id/accept;
//  2. 分享链接:owner POST /tenants/:id/invite-links 取 token
//     → 本人 POST /me/invitations/accept-by-token。
//
// 「被移除」前置状态也走真实服务(EnsureOwner → AddMember → RemoveMember
// 软删),不用 db.Create 直接塞行;用户目录查询/回写用最小桩,成员关系与
// 邀请行全部落真实库。security_t09_regression_test.go 的恢复用例复用本
// 文件基建,替代旧的 db.Create 伪覆盖。

const (
	rejoinOwnerID    = "rejoin-owner"
	rejoinOwnerEmail = "rejoin-owner@example.com"
)

// rejoinUserService 只实现邀请链路用到的用户查询与回写,
// 其余方法一律不实现(误调会 panic,便于发现越界依赖)。
type rejoinUserService struct {
	interfaces.UserService
	users   map[string]*types.User
	updates int
}

func (s *rejoinUserService) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	for _, u := range s.users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, apprepo.ErrUserNotFound
}

func (s *rejoinUserService) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return nil, apprepo.ErrUserNotFound
}

func (s *rejoinUserService) UpdateUser(_ context.Context, u *types.User) error {
	s.updates++
	if _, ok := s.users[u.ID]; ok {
		s.users[u.ID] = u
	}
	return nil
}

// rejoinTenantService 只实现 accept-by-token 响应里租户名展示用的
// GetTenantByID,直接读真实库,不为此引入整套租户服务。
type rejoinTenantService struct {
	interfaces.TenantService
	db *gorm.DB
}

func (s *rejoinTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	var tenant types.Tenant
	if err := s.db.First(&tenant, id).Error; err != nil {
		return nil, err
	}
	return &tenant, nil
}

// rejoinEnv 聚合真实链路测试的全部依赖。
type rejoinEnv struct {
	db       *gorm.DB
	members  interfaces.TenantMemberService
	users    *rejoinUserService
	router   *gin.Engine
	ownerCtx context.Context
}

func newRejoinEnv(t *testing.T) *rejoinEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}, &types.TenantInvitation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	memberSvc := service.NewTenantMemberService(apprepo.NewTenantMemberRepository(db), nil, nil, nil)
	invitationSvc := service.NewTenantInvitationService(apprepo.NewTenantInvitationRepository(db), memberSvc, nil)
	users := &rejoinUserService{users: map[string]*types.User{
		rejoinOwnerID: {ID: rejoinOwnerID, Email: rejoinOwnerEmail, TenantID: 1},
	}}
	h := &TenantInvitationHandler{
		invitationService: invitationSvc,
		userService:       users,
		tenantService:     &rejoinTenantService{db: db},
		memberService:     memberSvc,
		// systemSettingSvc 留 nil:跳过 auto_accept 分支,邀请先落 pending,
		// 由被移除用户自己走 accept(对应线上「非自动接受」路径)。
		systemSettingSvc: nil,
		configInfo:       &config.Config{},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 生产 auth 中间件把调用方 user id 写进 request context;测试用
	// 请求头选择当前调用者(owner 建邀请 / 被移除用户接受)。
	r.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Caller"); uid != "" {
			c.Request = c.Request.WithContext(
				context.WithValue(c.Request.Context(), types.UserIDContextKey, uid))
		}
		c.Next()
	}, errorCapture())
	// 路径与生产 routes_auth_tenant.go 逐条对齐。
	r.POST("/tenants/:id/invitations", h.CreateInvitation)
	r.POST("/tenants/:id/invite-links", h.CreateInviteLink)
	r.POST("/me/invitations/:inv_id/accept", h.AcceptMyInvitation)
	r.POST("/me/invitations/accept-by-token", h.AcceptMyInvitationByToken)
	return &rejoinEnv{
		db:       db,
		members:  memberSvc,
		users:    users,
		router:   r,
		ownerCtx: context.WithValue(context.Background(), types.UserIDContextKey, rejoinOwnerID),
	}
}

// seedRemovedViewer 用真实服务制造「被移除」前置状态:owner 落活跃行、
// 目标用户以 viewer 加入、随后被移除(tenant_members 留下软删行)。
func (e *rejoinEnv) seedRemovedViewer(t *testing.T, tenantID uint64, userID, email string) {
	t.Helper()
	e.users.users[userID] = &types.User{ID: userID, Email: email, TenantID: tenantID}
	if err := e.db.Create(&types.Tenant{
		ID:     tenantID,
		Name:   "rejoin-workspace-" + strconv.FormatUint(tenantID, 10),
		Status: "active",
	}).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := e.members.EnsureOwner(e.ownerCtx, rejoinOwnerID, tenantID); err != nil {
		t.Fatalf("seed owner membership: %v", err)
	}
	if _, err := e.members.AddMember(e.ownerCtx, userID, tenantID, types.TenantRoleViewer, nil); err != nil {
		t.Fatalf("seed viewer membership: %v", err)
	}
	if err := e.members.RemoveMember(e.ownerCtx, userID, tenantID); err != nil {
		t.Fatalf("remove viewer membership: %v", err)
	}
}

// membershipRows 返回该 (user, tenant) 的全部成员行(含软删),按 id 升序。
func (e *rejoinEnv) membershipRows(t *testing.T, userID string, tenantID uint64) []types.TenantMember {
	t.Helper()
	var rows []types.TenantMember
	if err := e.db.Unscoped().
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load membership rows: %v", err)
	}
	return rows
}

// assertRejoined 断言恢复后的共同不变量:恰好一条软删行 + 一条新活跃行
// (活跃行角色取自邀请),且撤销判定翻转为 false。
func (e *rejoinEnv) assertRejoined(t *testing.T, userID string, tenantID uint64, wantRole types.TenantRole) {
	t.Helper()
	rows := e.membershipRows(t, userID, tenantID)
	if len(rows) != 2 {
		t.Fatalf("want 2 membership rows (soft-deleted + new active), got %d: %+v", len(rows), rows)
	}
	var soft, active int
	var activeRole types.TenantRole
	for _, row := range rows {
		switch {
		case row.DeletedAt.Valid:
			soft++
		case row.Status == types.TenantMemberStatusActive:
			active++
			activeRole = row.Role
		}
	}
	if soft != 1 || active != 1 {
		t.Fatalf("want 1 soft-deleted + 1 active row, got soft=%d active=%d: %+v", soft, active, rows)
	}
	if activeRole != wantRole {
		t.Fatalf("active row role = %q, want %q", activeRole, wantRole)
	}
	revoked, err := e.members.HasRevokedAccess(context.Background(), userID)
	if err != nil {
		t.Fatalf("HasRevokedAccess after rejoin: %v", err)
	}
	if revoked {
		t.Fatal("HasRevokedAccess must flip to false after rejoining")
	}
}

func (e *rejoinEnv) postJSON(t *testing.T, method, path, caller, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if caller != "" {
		req.Header.Set("X-Test-Caller", caller)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

// rejoinDataField 取响应 data 下的字段值。
func rejoinDataField(t *testing.T, resp map[string]any, key string) any {
	t.Helper()
	data, _ := resp["data"].(map[string]any)
	v, ok := data[key]
	if !ok {
		t.Fatalf("response data missing %q: %+v", key, resp)
	}
	return v
}

// rejoinCreatePerUserInvitation 走真实端点:owner 对 email 发 per-user 邀请,
// 返回邀请 id(路径参数用)。
func (e *rejoinEnv) rejoinCreatePerUserInvitation(t *testing.T, tenantID uint64, email string) string {
	t.Helper()
	w, resp := e.postJSON(t, http.MethodPost,
		"/tenants/"+strconv.FormatUint(tenantID, 10)+"/invitations",
		rejoinOwnerID, `{"email":"`+email+`","role":"viewer"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invitation: status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	id, _ := rejoinDataField(t, resp, "id").(float64)
	if id == 0 {
		t.Fatalf("create invitation response missing id: %+v", resp)
	}
	return strconv.FormatUint(uint64(id), 10)
}

// rejoinAcceptPerUser 走真实端点:被移除用户接受自己的邀请。
func (e *rejoinEnv) rejoinAcceptPerUser(t *testing.T, invID, userID string) {
	t.Helper()
	w, _ := e.postJSON(t, http.MethodPost, "/me/invitations/"+invID+"/accept", userID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("accept invitation: status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

// R1 per-user 邀请通道:软删成员收到 owner 指定邮箱的邀请并接受后,
// tenant_members 新增活跃行(软删行保留),撤销状态解除。
func TestRejoin_PerUserInvitation_AcceptRestoresMembership(t *testing.T) {
	const (
		tenantID  = uint64(1)
		userID    = "rejoin-removed-user"
		userEmail = "rejoin-removed@example.com"
	)
	env := newRejoinEnv(t)
	env.seedRemovedViewer(t, tenantID, userID, userEmail)

	// 前置自检:只剩一条软删行,撤销判定为 true。
	if rows := env.membershipRows(t, userID, tenantID); len(rows) != 1 || !rows[0].DeletedAt.Valid {
		t.Fatalf("seed: want exactly one soft-deleted row, got %+v", rows)
	}
	if revoked, err := env.members.HasRevokedAccess(context.Background(), userID); err != nil || !revoked {
		t.Fatalf("after removal: revoked=%v err=%v, want true", revoked, err)
	}

	invID := env.rejoinCreatePerUserInvitation(t, tenantID, userEmail)

	w, resp := env.postJSON(t, http.MethodPost, "/me/invitations/"+invID+"/accept", userID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("accept invitation: status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	membership, _ := rejoinDataField(t, resp, "membership").(map[string]any)
	if membership == nil || membership["status"] != string(types.TenantMemberStatusActive) {
		t.Fatalf("accept response membership not active: %+v", resp)
	}

	env.assertRejoined(t, userID, tenantID, types.TenantRoleViewer)

	// 邀请行翻 accepted(再次接受应被状态机拒绝)。
	var inv types.TenantInvitation
	if err := env.db.First(&inv, invID).Error; err != nil {
		t.Fatalf("load invitation: %v", err)
	}
	if inv.Status != types.TenantInvitationStatusAccepted {
		t.Fatalf("invitation status = %q, want accepted", inv.Status)
	}
}

// R2 分享链接通道:软删成员持 owner 分享链接的 token 兑换加入,
// 活跃行恢复;链接行保持 pending 可复用,accepted_count 递增。
func TestRejoin_ShareLink_AcceptByTokenRestoresMembership(t *testing.T) {
	const (
		tenantID  = uint64(2)
		userID    = "rejoin-link-user"
		userEmail = "rejoin-link@example.com"
	)
	env := newRejoinEnv(t)
	env.seedRemovedViewer(t, tenantID, userID, userEmail)

	w, resp := env.postJSON(t, http.MethodPost,
		"/tenants/"+strconv.FormatUint(tenantID, 10)+"/invite-links",
		rejoinOwnerID, `{"role":"viewer"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create share link: status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	inviteURL, _ := rejoinDataField(t, resp, "invite_url").(string)
	idx := strings.LastIndex(inviteURL, "token=")
	if idx < 0 {
		t.Fatalf("share link response missing token in invite_url: %+v", resp)
	}
	token := inviteURL[idx+len("token="):]

	w, resp = env.postJSON(t, http.MethodPost, "/me/invitations/accept-by-token", userID,
		`{"token":"`+token+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("accept by token: status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	membership, _ := rejoinDataField(t, resp, "membership").(map[string]any)
	if membership == nil || membership["status"] != string(types.TenantMemberStatusActive) {
		t.Fatalf("accept-by-token response membership not active: %+v", resp)
	}
	if membership["tenant_id"] != float64(tenantID) {
		t.Fatalf("accept-by-token membership tenant_id = %v, want %d", membership["tenant_id"], tenantID)
	}

	env.assertRejoined(t, userID, tenantID, types.TenantRoleViewer)

	// 链接行保持 pending(多次使用、非一次性),accepted_count 记 1。
	var link types.TenantInvitation
	if err := env.db.Where("token = ?", token).First(&link).Error; err != nil {
		t.Fatalf("load share-link invitation: %v", err)
	}
	if link.Status != types.TenantInvitationStatusPending {
		t.Fatalf("share-link status = %q, want pending", link.Status)
	}
	if link.AcceptedCount != 1 {
		t.Fatalf("share-link accepted_count = %d, want 1", link.AcceptedCount)
	}
}
