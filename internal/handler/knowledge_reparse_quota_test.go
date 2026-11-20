package handler

// T04 TDD 红阶段：失败文档受限重试（每文档 ≤3 次/24h + 超管豁免 + 并发收口）。
// 覆盖票 T04 验收条件 2-7（AC-12 / AC-14 / AC-15）。
//
// ── 计数源事实（票面条件 1 核实结论，见 05-tests.md「T04 TDD 红阶段」）──
// knowledge.reparse_started 审计行的真实落库结构（kb_activity.go:199-210 +
// knowledge_process.go:3013-3017）：tenant_id=执行租户、
// action='knowledge.reparse_started'、scope_type='knowledge_base'（硬编码）、
// scope_id=KBID、target_type='knowledge'、target_id=文档ID、outcome='accepted'。
// 计数查询口径 = tenant_id + action + target_type + target_id + created_at > now-24h；
// knowledgeID 在 target_id 列（不在 scope_id，scope_id 是 KBID）。
// 本文件的 seed 行与桩写入都严格按此结构落库。
//
// ── 红绿分组 ──
// 红组（现状无限次逻辑，必红）：第 4 次拒绝、按文档隔离、批量剔除/全超限、
// 并发不超卖。绿组（对照组，现状即绿，实现后必须保持绿）：超管豁免、
// rebuild-index 零变化、knowledge.created 不误计、24h 窗口滚动。
//
// ── 复跑接线点（给②/③）──
// wireReparseQuotaGate 是②落地限次闸后的唯一夹具适配点：把 reparse_quota
// 判定依赖（audit 计数源 = 本夹具的 sqlite db）接到 handler 上。红阶段它
// 是空实现——现状没有任何闸，红组用例应全部如实落红。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	reparseQuotaTenantID uint64 = 7
	reparseQuotaKBID            = "kb"
)

// reparseQuotaLimit 是用户拍板的每文档 24h 上限（裁定⑤）。
const reparseQuotaLimit = 3

// newReparseQuotaDB 建真实 sqlite 审计库（audit_logs 表按模型迁移）。
// 计数只依赖普通列（tenant_id/action/target_type/target_id/created_at），
// 不依赖迁移独有的部分索引，AutoMigrate 夹具与真 schema 等价（D-12 口径）。
func newReparseQuotaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "reparse_quota.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	// 并发用例共用同一文件库；压到单连接由 database/sql 串行化写入
	// （与 auth_register_by_invite_quota_test.go 的取舍一致）。
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&types.AuditLog{}); err != nil {
		t.Fatalf("migrate audit_logs: %v", err)
	}
	return db
}

// seedReparseAudit 按 recordKBActivity 的真实落库结构写一条审计行。
// age 决定 created_at 落在 24h 窗口内还是窗外。
func seedReparseAudit(t *testing.T, db *gorm.DB, action types.AuditAction, kbID, docID string, age time.Duration) {
	t.Helper()
	row := &types.AuditLog{
		TenantID:   reparseQuotaTenantID,
		Action:     action,
		ScopeType:  "knowledge_base",
		ScopeID:    kbID,
		TargetType: "knowledge",
		TargetID:   docID,
		Outcome:    types.AuditOutcomeAccepted,
		Details:    types.JSON(`{"title":"doc","type":"file","attempt":1}`),
		CreatedAt:  time.Now().Add(-age),
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

// countReparseAudits 按核实口径数指定文档窗口内 reparse_started 行数；
// ageCutoff<=0 数全量。这是「拒绝不污染计数」断言的读数口径。
func countReparseAudits(t *testing.T, db *gorm.DB, docID string, ageCutoff time.Duration) int64 {
	t.Helper()
	q := db.Model(&types.AuditLog{}).Where(
		"tenant_id = ? AND action = ? AND target_type = ? AND target_id = ?",
		reparseQuotaTenantID, types.AuditActionKnowledgeReparseStarted, "knowledge", docID,
	)
	if ageCutoff > 0 {
		q = q.Where("created_at > ?", time.Now().Add(-ageCutoff))
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count reparse audits: %v", err)
	}
	return n
}

// earliestReparseAudit 返回窗口内最早一行的时间，用于恢复时间断言。
func earliestReparseAudit(t *testing.T, db *gorm.DB, docID string, ageCutoff time.Duration) time.Time {
	t.Helper()
	var row types.AuditLog
	q := db.Where(
		"tenant_id = ? AND action = ? AND target_type = ? AND target_id = ?",
		reparseQuotaTenantID, types.AuditActionKnowledgeReparseStarted, "knowledge", docID,
	)
	if ageCutoff > 0 {
		q = q.Where("created_at > ?", time.Now().Add(-ageCutoff))
	}
	if err := q.Order("created_at ASC").First(&row).Error; err != nil {
		t.Fatalf("load earliest reparse audit: %v", err)
	}
	return row.CreatedAt
}

// quotaKnowledgeService 是最小知识服务桩：reparse 成功路径按真实服务行为
// 写一条 reparse_started 审计行（recordReparseStarted 的等价投影），
// 使「拒绝不写行/不产生任务」断言有真实读数。并发用例会并发进入，
// 内部加锁。
type quotaKnowledgeService struct {
	interfaces.KnowledgeService
	mu    sync.Mutex
	db    *gorm.DB
	docs  []*types.Knowledge
	calls map[string]int
}

func newQuotaKnowledgeService(db *gorm.DB, docIDs ...string) *quotaKnowledgeService {
	s := &quotaKnowledgeService{db: db, calls: map[string]int{}}
	for _, id := range docIDs {
		s.docs = append(s.docs, &types.Knowledge{
			ID: id, TenantID: reparseQuotaTenantID, KnowledgeBaseID: reparseQuotaKBID,
			ParseStatus: types.ParseStatusFailed,
		})
	}
	return s
}

func (s *quotaKnowledgeService) doc(id string) *types.Knowledge {
	for _, d := range s.docs {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (s *quotaKnowledgeService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if d := s.doc(id); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("not found: %s", id)
}

func (s *quotaKnowledgeService) GetKnowledgeBatch(_ context.Context, _ uint64, ids []string) ([]*types.Knowledge, error) {
	out := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		if d := s.doc(id); d != nil {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *quotaKnowledgeService) ListKnowledgeByKnowledgeBaseID(_ context.Context, _ string) ([]*types.Knowledge, error) {
	return s.docs, nil
}

// ReparseKnowledge 模拟真实服务：提交成功即记一条 reparse_started 审计行
// （knowledge_process.go:3037/3103 只在 enqueue 成功分支记录，行为一致）。
func (s *quotaKnowledgeService) ReparseKnowledge(ctx context.Context, id string, _ *types.KnowledgeProcessOverrides) (*types.Knowledge, error) {
	d := s.doc(id)
	if d == nil {
		return nil, fmt.Errorf("not found: %s", id)
	}
	s.mu.Lock()
	s.calls[id]++
	s.mu.Unlock()
	actor, _ := types.UserIDFromContext(ctx)
	row := &types.AuditLog{
		TenantID:    reparseQuotaTenantID,
		ActorUserID: actor,
		Action:      types.AuditActionKnowledgeReparseStarted,
		ScopeType:   "knowledge_base",
		ScopeID:     reparseQuotaKBID,
		TargetType:  "knowledge",
		TargetID:    id,
		Outcome:     types.AuditOutcomeAccepted,
		Details:     types.JSON(`{"title":"doc","type":"file","attempt":1}`),
		CreatedAt:   time.Now(),
	}
	if err := s.db.Create(row).Error; err != nil {
		return nil, err
	}
	return d, nil
}

func (s *quotaKnowledgeService) reparseCalls(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[id]
}

// reparseQuotaEnv 聚合夹具：真实审计库 + 服务桩 + handler + router。
type reparseQuotaEnv struct {
	db     *gorm.DB
	svc    *quotaKnowledgeService
	queue  *documentDeleteEnqueuer
	router *gin.Engine
}

func newReparseQuotaEnv(t *testing.T, systemAdmin bool, docIDs ...string) *reparseQuotaEnv {
	t.Helper()
	db := newReparseQuotaDB(t)
	svc := newQuotaKnowledgeService(db, docIDs...)
	queue := &documentDeleteEnqueuer{}
	h := &KnowledgeHandler{
		cfg:         transferHandlerConfig(),
		kbService:   transferHandlerKB(),
		kgService:   svc,
		asynqClient: queue,
	}
	wireReparseQuotaGate(t, h, db)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{
			TenantID: reparseQuotaTenantID, UserID: "user", Role: types.TenantRoleAdmin,
		})
		if systemAdmin {
			ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		}
		ctx = types.WithExecutionTenant(ctx, reparseQuotaTenantID)
		c.Set(types.TenantIDContextKey.String(), reparseQuotaTenantID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/knowledge/:id/reparse", h.ReparseKnowledge)
	r.POST("/knowledge/batch-reparse", h.BatchReparseKnowledge)
	r.POST("/knowledge-bases/:id/rebuild-index", h.RebuildKnowledgeBaseIndex)
	return &reparseQuotaEnv{db: db, svc: svc, queue: queue, router: r}
}

// wireReparseQuotaGate 是②实现落地后的复跑接线点：把限次闸（reparse_quota
// 的判定依赖，计数源=本库 audit_logs）接到 handler。红阶段现状无闸，保持
// 空实现——红组用例必须如实落红，不在此预埋任何判定。
// ②落地后：一行接入（断言零改动），见下方赋值。
func wireReparseQuotaGate(t *testing.T, h *KnowledgeHandler, db *gorm.DB) {
	t.Helper()
	h.reparseQuota = service.NewReparseQuotaGate(db)
}

func (e *reparseQuotaEnv) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := mutationRequest(e.router, http.MethodPost, path, body)
	return w
}

func (e *reparseQuotaEnv) reparse(t *testing.T, docID string) *httptest.ResponseRecorder {
	t.Helper()
	return e.post(t, "/knowledge/"+docID+"/reparse", "")
}

// decodeAppErrorBody 解出 ErrorHandler 的错误信封
// {"success":false,"error":{"code","message","details"}}。
func decodeAppErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Success bool           `json:"success"`
		Error   map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return body.Error
}

// assertRetryLimitExhausted 断言 429 拒绝信封的完整契约：
// HTTP 429 + code=1006（ErrTooManyRequests，quota 守卫既有先例）+
// message 人话（含「重试」）+ details.error_code=RETRY_LIMIT_EXHAUSTED +
// details.recover_at=最早计数行+24h（±2min 容差）。
func assertRetryLimitExhausted(t *testing.T, w *httptest.ResponseRecorder, db *gorm.DB, docID string) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429 (body=%s)", w.Code, w.Body.String())
	}
	errBody := decodeAppErrorBody(t, w)
	if code, _ := errBody["code"].(float64); code != float64(1006) {
		t.Fatalf("error.code=%v, want 1006 (ErrTooManyRequests)", errBody["code"])
	}
	msg, _ := errBody["message"].(string)
	if !strings.Contains(msg, "重试") {
		t.Fatalf("error.message=%q 应为人话文案（含「重试」）", msg)
	}
	details, _ := errBody["details"].(map[string]any)
	if got, _ := details["error_code"].(string); got != "RETRY_LIMIT_EXHAUSTED" {
		t.Fatalf("details.error_code=%v, want RETRY_LIMIT_EXHAUSTED", details["error_code"])
	}
	recoverRaw, _ := details["recover_at"].(string)
	recoverAt, err := time.Parse(time.RFC3339, recoverRaw)
	if err != nil {
		t.Fatalf("details.recover_at=%q 不是 RFC3339 时间: %v", recoverRaw, err)
	}
	want := earliestReparseAudit(t, db, docID, 24*time.Hour).Add(24 * time.Hour)
	if diff := recoverAt.Sub(want); diff < -2*time.Minute || diff > 2*time.Minute {
		t.Fatalf("details.recover_at=%v, want≈%v（最早计数+24h）", recoverAt, want)
	}
}

// ───────────────────────── 红组：限次（票条件 2、5） ─────────────────────────

// TestT04_ReparseQuotaRejectsFourthAttemptIn24h：同文档 24h 内第 1-3 次放行
// 且各记 1 行审计；第 4 次 429 + RETRY_LIMIT_EXHAUSTED + 恢复时间；
// 被拒请求不调服务（不产生新任务）、不写审计行（计数不被拒绝污染）。
func TestT04_ReparseQuotaRejectsFourthAttemptIn24h(t *testing.T) {
	const doc = "quota-doc"
	env := newReparseQuotaEnv(t, false, doc)

	for i := 1; i <= reparseQuotaLimit; i++ {
		w := env.reparse(t, doc)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次 reparse status=%d, want 200 (body=%s)", i, w.Code, w.Body.String())
		}
		if got := countReparseAudits(t, env.db, doc, 0); got != int64(i) {
			t.Fatalf("第 %d 次放行后审计行=%d, want %d", i, got, i)
		}
	}

	w := env.reparse(t, doc)
	assertRetryLimitExhausted(t, w, env.db, doc)

	if got := env.svc.reparseCalls(doc); got != reparseQuotaLimit {
		t.Fatalf("被拒后服务调用次数=%d, want %d（第 4 次不得产生新任务）", got, reparseQuotaLimit)
	}
	if got := countReparseAudits(t, env.db, doc, 0); got != reparseQuotaLimit {
		t.Fatalf("被拒后审计行=%d, want %d（计数不被拒绝污染）", got, reparseQuotaLimit)
	}
}

// TestT04_ReparseQuotaIsPerDocument：额度按文档隔离——A 文档耗尽后，
// 同 KB 的 B 文档仍有完整 3 次额度。
func TestT04_ReparseQuotaIsPerDocument(t *testing.T) {
	const docA, docB = "exhausted-doc", "fresh-doc"
	env := newReparseQuotaEnv(t, false, docA, docB)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, docA, time.Hour)
	}

	// A 已耗尽：本次直接 429。
	if w := env.reparse(t, docA); w.Code != http.StatusTooManyRequests {
		t.Fatalf("耗尽文档 A 再试 status=%d, want 429 (body=%s)", w.Code, w.Body.String())
	}

	// B 不受 A 占用：第 1-3 次放行，第 4 次才 429。
	for i := 1; i <= reparseQuotaLimit; i++ {
		if w := env.reparse(t, docB); w.Code != http.StatusOK {
			t.Fatalf("文档 B 第 %d 次 status=%d, want 200 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	if w := env.reparse(t, docB); w.Code != http.StatusTooManyRequests {
		t.Fatalf("文档 B 第 4 次 status=%d, want 429 (body=%s)", w.Code, w.Body.String())
	}
	if got := countReparseAudits(t, env.db, docA, 0); got != reparseQuotaLimit {
		t.Fatalf("文档 A 审计行=%d, want %d（B 的重试不得写进 A）", got, reparseQuotaLimit)
	}
}

// ───────────────────── 红组：批量入口（票条件 4） ─────────────────────

// TestT04_BatchReparseSkipsQuotaExhaustedDocuments：enqueue 前逐文档预检，
// 超限文档从任务列表剔除并在响应中回报；未超限文档正常入队。
func TestT04_BatchReparseSkipsQuotaExhaustedDocuments(t *testing.T) {
	const docA, docB = "exhausted-doc", "fresh-doc"
	env := newReparseQuotaEnv(t, false, docA, docB)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, docA, time.Hour)
	}

	w := env.post(t, "/knowledge/batch-reparse",
		fmt.Sprintf(`{"kb_id":%q,"ids":[%q,%q]}`, reparseQuotaKBID, docA, docB))
	if w.Code != http.StatusOK {
		t.Fatalf("部分超限批量 status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), docA) {
		t.Fatalf("响应应回报被剔除的文档 %s (body=%s)", docA, w.Body.String())
	}
	var body struct {
		Data struct {
			ReparseCount int `json:"reparse_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode batch body: %v", err)
	}
	if body.Data.ReparseCount != 1 {
		t.Fatalf("reparse_count=%d, want 1（只有未超限文档入队）", body.Data.ReparseCount)
	}
	if env.queue.task == nil {
		t.Fatalf("未超限文档应正常入队")
	}
	var payload types.KnowledgeListReparsePayload
	if err := json.Unmarshal(env.queue.task.Payload(), &payload); err != nil {
		t.Fatalf("decode task payload: %v", err)
	}
	if fmt.Sprint(payload.KnowledgeIDs) != fmt.Sprint([]string{docB}) {
		t.Fatalf("任务文档列表=%v, want [%s]（超限文档不得入队）", payload.KnowledgeIDs, docB)
	}
	// 终审轮3 L1 对照组：普通成员批量必须 QuotaExempt=false（照常计入共享额度），
	// 与超管批量/重建路径的 true 形成双向守卫。
	if payload.QuotaExempt {
		t.Fatalf("普通成员批量任务 payload.QuotaExempt=true, want false（照常计入额度）")
	}
}

// TestT04_BatchReparseAllExhaustedReturnsClearInfo：全超限时返回明确信息
// 而非 500，且不产生任何任务。
func TestT04_BatchReparseAllExhaustedReturnsClearInfo(t *testing.T) {
	const doc = "exhausted-doc"
	env := newReparseQuotaEnv(t, false, doc)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, time.Hour)
	}

	w := env.post(t, "/knowledge/batch-reparse",
		fmt.Sprintf(`{"kb_id":%q,"ids":[%q]}`, reparseQuotaKBID, doc))
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("全超限不得返回 500 (body=%s)", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), doc) {
		t.Fatalf("响应应点名全部被拒文档 %s (body=%s)", doc, w.Body.String())
	}
	if env.queue.task != nil {
		t.Fatalf("全超限不得产生新任务")
	}
}

// ─────────────────── 红组：并发不超卖（票条件 5） ───────────────────

// TestT04_ConcurrentReparseDoesNotOversell：count=2 起步时两并发同文档请求，
// 最多放行 1 个。断言只看最终可见结果（审计行 ≤3、放行数 ≤1、无 500），
// 不锁内部时序——SQLite 夹具里锁是进程内形态，PG/MySQL 生产是跨进程形态，
// 行为契约以最终计数为准。
func TestT04_ConcurrentReparseDoesNotOversell(t *testing.T) {
	const doc = "quota-doc"
	env := newReparseQuotaEnv(t, false, doc)
	seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, time.Hour)
	seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, 30*time.Minute)

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, rejected := 0, 0
	var unexpected []int
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := env.reparse(t, doc)
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusOK:
				accepted++
			case http.StatusTooManyRequests:
				rejected++
			default:
				unexpected = append(unexpected, w.Code)
			}
		}()
	}
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("并发 reparse 出现非 200/429 状态: %v", unexpected)
	}
	if accepted > 1 {
		t.Fatalf("count=2 起步两并发放行 %d 个, want ≤1（第 4 个 attempt 不产生）", accepted)
	}
	if got := countReparseAudits(t, env.db, doc, 0); got > reparseQuotaLimit {
		t.Fatalf("并发后审计行=%d, want ≤%d", got, reparseQuotaLimit)
	}
}

// ───────────── 绿组：对照与豁免（现状即绿，实现后必须保持绿） ─────────────

// TestT04_SystemAdminExemptFromReparseQuota：系统管理员已计 3 次后继续
// 重试不受限（AC-14 豁免；IsSystemAdminFromContext）。
func TestT04_SystemAdminExemptFromReparseQuota(t *testing.T) {
	const doc = "admin-doc"
	env := newReparseQuotaEnv(t, true, doc)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, time.Hour)
	}

	for i := 1; i <= 2; i++ {
		if w := env.reparse(t, doc); w.Code != http.StatusOK {
			t.Fatalf("超管第 %d 次（已计 3）status=%d, want 200 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	if got := env.svc.reparseCalls(doc); got != 2 {
		t.Fatalf("超管服务调用=%d, want 2", got)
	}

	// 终审轮3 L1：超管批量的豁免完全靠 handler 往任务 payload 写 QuotaExempt=true
	// （worker ctx 不带超管身份，verdict 兜不住异步路径）。锁住入队 payload 的豁免
	// 标记，防止有人改掉 types.IsSystemAdminFromContext 传参而测试全绿。
	batchW := env.post(t, "/knowledge/batch-reparse",
		fmt.Sprintf(`{"kb_id":%q,"ids":[%q]}`, reparseQuotaKBID, doc))
	if batchW.Code != http.StatusOK {
		t.Fatalf("超管批量 status=%d, want 200 (body=%s)", batchW.Code, batchW.Body.String())
	}
	if env.queue.task == nil {
		t.Fatalf("超管批量应正常入队")
	}
	var payload types.KnowledgeListReparsePayload
	if err := json.Unmarshal(env.queue.task.Payload(), &payload); err != nil {
		t.Fatalf("decode task payload: %v", err)
	}
	if !payload.QuotaExempt {
		t.Fatalf("超管批量任务 payload.QuotaExempt=false, want true（worker 侧豁免只认此标记）")
	}
}

// TestT04_RebuildIndexIgnoresReparseQuota：KB 级 rebuild-index 行为零变化
// （R3 口径：不计入、不限次）——文档额度耗尽后 rebuild-index 仍全量入队。
func TestT04_RebuildIndexIgnoresReparseQuota(t *testing.T) {
	const doc = "exhausted-doc"
	env := newReparseQuotaEnv(t, false, doc)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, time.Hour)
	}

	w := env.post(t, "/knowledge-bases/"+reparseQuotaKBID+"/rebuild-index", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rebuild-index status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if env.queue.task == nil {
		t.Fatalf("rebuild-index 应照常入队")
	}
	var payload types.KnowledgeListReparsePayload
	if err := json.Unmarshal(env.queue.task.Payload(), &payload); err != nil {
		t.Fatalf("decode task payload: %v", err)
	}
	if fmt.Sprint(payload.KnowledgeIDs) != fmt.Sprint([]string{doc}) {
		t.Fatalf("rebuild-index 任务文档列表=%v, want 全量 [%s]", payload.KnowledgeIDs, doc)
	}
	// 终审轮3 L1：rebuild 的豁免同样只靠 payload.QuotaExempt=true（R3 口径：
	// 整批不占 24h 重试额度），锁住标记防回归。
	if !payload.QuotaExempt {
		t.Fatalf("rebuild-index 任务 payload.QuotaExempt=false, want true（R3：整批不占重试额度）")
	}
}

// TestT04_FirstParseCreationNotCounted：首次解析的 knowledge.created 审计行
// 是独立 action，不得误计进重试额度（票条件 7）。
func TestT04_FirstParseCreationNotCounted(t *testing.T) {
	const doc = "created-doc"
	env := newReparseQuotaEnv(t, false, doc)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeCreated, reparseQuotaKBID, doc, time.Hour)
	}

	for i := 1; i <= reparseQuotaLimit; i++ {
		if w := env.reparse(t, doc); w.Code != http.StatusOK {
			t.Fatalf("仅有 created 行时第 %d 次 reparse status=%d, want 200 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	var createdCount int64
	if err := env.db.Model(&types.AuditLog{}).Where(
		"action = ?", types.AuditActionKnowledgeCreated,
	).Count(&createdCount).Error; err != nil {
		t.Fatalf("count created rows: %v", err)
	}
	if createdCount != reparseQuotaLimit {
		t.Fatalf("created 行=%d, want %d（reparse 不得增删 created 行）", createdCount, reparseQuotaLimit)
	}
}

// TestT04_ReparseQuotaWindowRolls：24h 滚动窗口——3 次计数全部在窗外
// （30h 前）时额度已恢复，reparse 照常放行。
func TestT04_ReparseQuotaWindowRolls(t *testing.T) {
	const doc = "stale-doc"
	env := newReparseQuotaEnv(t, false, doc)
	for i := 0; i < reparseQuotaLimit; i++ {
		seedReparseAudit(t, env.db, types.AuditActionKnowledgeReparseStarted, reparseQuotaKBID, doc, 30*time.Hour)
	}

	if w := env.reparse(t, doc); w.Code != http.StatusOK {
		t.Fatalf("窗口已滚动的文档 reparse status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := countReparseAudits(t, env.db, doc, 24*time.Hour); got != 1 {
		t.Fatalf("窗口内审计行=%d, want 1（只数 24h 内）", got)
	}
}
