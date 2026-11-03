package handler

// T03 TDD 红阶段：注册配额强制（AC-7 / AC-13 / AC-14 / AC-15）。
//
// 本文件只写行为级用例，不实现业务逻辑。链路用真实 sqlite + 真实
// service/repository（与 tenant_invitation_rejoin_test.go 同思路），
// 用户目录与租户名用最小桩。另在真实仓储外包一层记录装饰器，用于
// 断言「注册失败时预占确实发生过、且预占后释放」，避免只比较计数
// 时把「根本没预占」误判为通过。
//
// 当前实现尚未接入预占/释放与 lookup 满额检查，因此除守卫用例外
// 预期失败；失败输出记录在 05-tests.md 的「TDD 红阶段（T03）」。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const quotaTestTenantID uint64 = 900

// quotaRepoMethods 是仓储配额方法的窄接口，供装饰器转发真实实现。
type quotaRepoMethods interface {
	ReserveRegistrationQuota(ctx context.Context, id uint64, now time.Time) (bool, error)
	ReleaseRegistrationQuota(ctx context.Context, id uint64) error
}

// quotaRecordingRepo 包住真实仓储，记录配额预占/释放调用。
// T03 要求「失败注册释放预占」，只比较最终计数无法区分
// 「预占又释放」与「根本没有预占」，因此用调用记录补强断言。
type quotaRecordingRepo struct {
	interfaces.TenantInvitationRepository
	quota        quotaRepoMethods
	mu           sync.Mutex
	reserveOK    int
	reserveFail  int
	releaseCalls int
}

func (r *quotaRecordingRepo) ReserveRegistrationQuota(
	ctx context.Context, id uint64, now time.Time,
) (bool, error) {
	ok, err := r.quota.ReserveRegistrationQuota(ctx, id, now)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.reserveOK++
	} else {
		r.reserveFail++
	}
	return ok, err
}

func (r *quotaRecordingRepo) ReleaseRegistrationQuota(ctx context.Context, id uint64) error {
	err := r.quota.ReleaseRegistrationQuota(ctx, id)
	if err == nil {
		r.mu.Lock()
		r.releaseCalls++
		r.mu.Unlock()
	}
	return err
}

func (r *quotaRecordingRepo) calls() (reserveOK, reserveFail, release int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserveOK, r.reserveFail, r.releaseCalls
}

// quotaTestUserService 是最小用户目录：只实现注册链路用到的方法。
// Register/UpdateUser 会被并发调用（并发用例），因此内部加锁。
type quotaTestUserService struct {
	interfaces.UserService
	mu        sync.Mutex
	users     map[string]*types.User
	byEmail   map[string]*types.User
	seq       int
	updateErr error
}

func newQuotaTestUserService() *quotaTestUserService {
	return &quotaTestUserService{
		users:   map[string]*types.User{},
		byEmail: map[string]*types.User{},
	}
}

func (s *quotaTestUserService) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byEmail[email], nil
}

func (s *quotaTestUserService) GetUserByID(_ context.Context, id string) (*types.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users[id], nil
}

func (s *quotaTestUserService) Register(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	u := &types.User{
		ID:       fmt.Sprintf("quota-user-%d", s.seq),
		Username: req.Username,
		Email:    req.Email,
		IsActive: true,
	}
	s.users[u.ID] = u
	s.byEmail[req.Email] = u
	return u, nil
}

func (s *quotaTestUserService) UpdateUser(_ context.Context, u *types.User) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
	return nil
}

func (s *quotaTestUserService) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[id]; ok {
		delete(s.byEmail, u.Email)
		delete(s.users, id)
	}
	return nil
}

func (s *quotaTestUserService) GenerateTokens(_ context.Context, u *types.User) (string, string, error) {
	return "access-" + u.ID, "refresh-" + u.ID, nil
}

func (s *quotaTestUserService) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

// quotaTestTenantService 只提供租户名，供响应展示；不查真实库。
type quotaTestTenantService struct {
	interfaces.TenantService
}

func (s *quotaTestTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: id, Name: "quota-workspace"}, nil
}

// quotaTestEnv 聚合真实链路依赖与测试用 router。
type quotaTestEnv struct {
	db     *gorm.DB
	users  *quotaTestUserService
	repo   *quotaRecordingRepo
	router *gin.Engine
	seq    int
}

func newQuotaTestEnv(t *testing.T) *quotaTestEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 并发用例需要共享同一份内存库；压到单连接后由 database/sql
	// 串行化写入，与 T01 仓储并发测试的取舍一致。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}, &types.TenantInvitation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	realRepo := apprepo.NewTenantInvitationRepository(db)
	recorder := &quotaRecordingRepo{
		TenantInvitationRepository: realRepo,
		quota:                      realRepo.(quotaRepoMethods),
	}
	memberSvc := service.NewTenantMemberService(apprepo.NewTenantMemberRepository(db), nil, nil, nil)
	invSvc := service.NewTenantInvitationService(recorder, memberSvc, nil)
	users := newQuotaTestUserService()
	tenantSvc := &quotaTestTenantService{}

	authHandler := &AuthHandler{
		configInfo: &config.Config{
			Auth: &config.AuthConfig{
				RegistrationMode: config.AuthRegistrationModeSelfServe,
			},
		},
		userService:   users,
		tenantService: tenantSvc,
		invitationSvc: invSvc,
	}
	invitationHandler := &TenantInvitationHandler{
		invitationService: invSvc,
		userService:       users,
		tenantService:     tenantSvc,
		memberService:     memberSvc,
		configInfo:        &config.Config{},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 生产 auth 中间件把调用方 user id 写入 request context；
	// 测试用请求头模拟。
	r.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Caller"); uid != "" {
			c.Request = c.Request.WithContext(
				context.WithValue(c.Request.Context(), types.UserIDContextKey, uid))
		}
		c.Next()
	}, errorCapture())
	r.POST("/auth/register-by-invite", authHandler.RegisterByInvite)
	r.POST("/auth/invitations/lookup", authHandler.LookupInvitationByToken)
	r.POST("/me/invitations/accept-by-token", invitationHandler.AcceptMyInvitationByToken)
	return &quotaTestEnv{db: db, users: users, repo: recorder, router: r}
}

// seedQuotaLink 直接写入一条共享链接行，避免用例依赖 T02 的创建接口。
func (e *quotaTestEnv) seedQuotaLink(t *testing.T, max *int, registered int) (uint64, string) {
	t.Helper()
	e.seq++
	token := fmt.Sprintf("quota-token-%d", e.seq)
	inv := &types.TenantInvitation{
		TenantID:         quotaTestTenantID,
		Token:            token,
		Role:             types.TenantRoleViewer,
		Status:           types.TenantInvitationStatusPending,
		ExpiresAt:        time.Now().Add(time.Hour),
		MaxRegistrations: max,
		RegisteredCount:  registered,
	}
	if err := e.db.Create(inv).Error; err != nil {
		t.Fatalf("seed share link: %v", err)
	}
	return inv.ID, token
}

func (e *quotaTestEnv) post(t *testing.T, path, caller, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if caller != "" {
		req.Header.Set("X-Test-Caller", caller)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e *quotaTestEnv) registeredCount(t *testing.T, invID uint64) int {
	t.Helper()
	var inv types.TenantInvitation
	if err := e.db.First(&inv, invID).Error; err != nil {
		t.Fatalf("load invitation %d: %v", invID, err)
	}
	return inv.RegisteredCount
}

func quotaRegisterBody(token, email string) string {
	return fmt.Sprintf(`{"token":%q,"email":%q,"username":"quota-user","password":"supersecret1"}`,
		token, email)
}

// TestT03_RegisterRejectedWhenQuotaFull 覆盖 TDD 红阶段第 1 条：
// 满额链接注册返回 410，且不创建账号（预占必须在建号之前）。
func TestT03_RegisterRejectedWhenQuotaFull(t *testing.T) {
	env := newQuotaTestEnv(t)
	max := 1
	invID, token := env.seedQuotaLink(t, &max, 1)

	w := env.post(t, "/auth/register-by-invite", "", quotaRegisterBody(token, "full@example.com"))
	if w.Code != http.StatusGone {
		t.Fatalf("满额注册 status=%d, want 410 (body=%s)", w.Code, w.Body.String())
	}
	if got := env.users.count(); got != 0 {
		t.Fatalf("满额注册创建了 %d 个账号, want 0", got)
	}
	if got := env.registeredCount(t, invID); got != 1 {
		t.Fatalf("满额注册后 registered_count=%d, want 1（不应再增加）", got)
	}
}

// TestT03_SuccessfulRegisterIncrementsCount 覆盖第 2 条：
// 未满链接注册成功返回 201，registered_count 从 X 变成 X+1。
func TestT03_SuccessfulRegisterIncrementsCount(t *testing.T) {
	env := newQuotaTestEnv(t)
	max := 5
	invID, token := env.seedQuotaLink(t, &max, 2)

	w := env.post(t, "/auth/register-by-invite", "", quotaRegisterBody(token, "ok@example.com"))
	if w.Code != http.StatusCreated {
		t.Fatalf("未满注册 status=%d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if got := env.registeredCount(t, invID); got != 3 {
		t.Fatalf("注册成功后 registered_count=%d, want 3", got)
	}
	if got := env.users.count(); got != 1 {
		t.Fatalf("注册成功应创建 1 个账号, got %d", got)
	}
}

// TestT03_FailedRegisterReleasesReservation 覆盖第 3 条：
// 预占后建号流程失败，计数回到初始值，且预占与释放都真实发生。
func TestT03_FailedRegisterReleasesReservation(t *testing.T) {
	env := newQuotaTestEnv(t)
	env.users.updateErr = errors.New("模拟写回用户失败")
	max := 5
	invID, token := env.seedQuotaLink(t, &max, 1)

	w := env.post(t, "/auth/register-by-invite", "", quotaRegisterBody(token, "fail@example.com"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("失败注册 status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	reserveOK, _, release := env.repo.calls()
	if reserveOK != 1 {
		t.Fatalf("应当预占 1 次, reserveOK=%d", reserveOK)
	}
	if release != 1 {
		t.Fatalf("注册失败应释放 1 次预占, release=%d", release)
	}
	if got := env.registeredCount(t, invID); got != 1 {
		t.Fatalf("失败注册后 registered_count=%d, want 1（回到初始值）", got)
	}
}

// TestT03_ExistingAccountAcceptBypassesQuota 覆盖第 4 条（守卫）：
// 满额链接下已有账号走 accept-by-token 仍成功，计数不变。
func TestT03_ExistingAccountAcceptBypassesQuota(t *testing.T) {
	env := newQuotaTestEnv(t)
	const existingUserID = "quota-existing-user"
	env.users.users[existingUserID] = &types.User{
		ID: existingUserID, Email: "existing@example.com", TenantID: quotaTestTenantID,
	}
	max := 1
	invID, token := env.seedQuotaLink(t, &max, 1)

	w := env.post(t, "/me/invitations/accept-by-token", existingUserID,
		fmt.Sprintf(`{"token":%q}`, token))
	if w.Code != http.StatusOK {
		t.Fatalf("已有账号加入 status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := env.registeredCount(t, invID); got != 1 {
		t.Fatalf("已有账号加入后 registered_count=%d, want 1（不计数）", got)
	}
}

// TestT03_LookupReturnsGoneWhenQuotaFull 覆盖第 5 条：
// lookup 在满额时返回 410，与过期/撤销统一语义。
func TestT03_LookupReturnsGoneWhenQuotaFull(t *testing.T) {
	env := newQuotaTestEnv(t)
	max := 2
	_, token := env.seedQuotaLink(t, &max, 2)

	w := env.post(t, "/auth/invitations/lookup", "", fmt.Sprintf(`{"token":%q}`, token))
	if w.Code != http.StatusGone {
		t.Fatalf("满额 lookup status=%d, want 410 (body=%s)", w.Code, w.Body.String())
	}
}

// TestT03_ConcurrentRegistrationsDoNotOversell 覆盖第 6 条：
// 上限 N，并发 N+K 个注册，恰好 N 个成功，计数不超过上限。
func TestT03_ConcurrentRegistrationsDoNotOversell(t *testing.T) {
	env := newQuotaTestEnv(t)
	const limit = 3
	const attempts = 8
	max := limit
	invID, token := env.seedQuotaLink(t, &max, 0)

	var wg sync.WaitGroup
	var mu sync.Mutex
	created, rejected := 0, 0
	var unexpected []int
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := env.post(t, "/auth/register-by-invite", "",
				quotaRegisterBody(token, fmt.Sprintf("concurrent-%d@example.com", idx)))
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusCreated:
				created++
			case http.StatusGone:
				rejected++
			default:
				unexpected = append(unexpected, w.Code)
			}
		}(i)
	}
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("并发注册出现非 201/410 状态: %v", unexpected)
	}
	if created != limit {
		t.Fatalf("并发注册成功数=%d, want %d（不超卖）", created, limit)
	}
	if rejected != attempts-limit {
		t.Fatalf("并发注册被拒数=%d, want %d", rejected, attempts-limit)
	}
	if got := env.registeredCount(t, invID); got != limit {
		t.Fatalf("并发后 registered_count=%d, want %d", got, limit)
	}
	if got := env.users.count(); got != limit {
		t.Fatalf("并发后创建账号数=%d, want %d（失败注册不建号）", got, limit)
	}
}
