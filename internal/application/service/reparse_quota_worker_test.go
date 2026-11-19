package service

// CP3 修复轮 1 复验（F1/FS1/F2/F6）：重试额度豁免与 worker 逐文档复核的
// 服务层实证。全部用例走真实 ReparseKnowledge 写入点 + 真实 sqlite
// audit_logs 表 + 真实 ReparseQuotaGate 计数谓词（D-12：计数只依赖
// tenant_id/action/target_type/target_id/created_at 普通列与 details JSON，
// AutoMigrate 夹具与真 schema 等价）。
//
// 修复项对应：
//   - B1/B2：worker 端到端——普通成员批量照常计数；rebuild-index 来源在
//     worker 跑完后不占普通成员额度（audit 行照写但 quota_exempt 排除）。
//   - B3：文件替换路径（knowledge_replace）不扣额度——替换后计数不变。
//   - B4：系统管理员同步重试不占普通成员计数。
//   - B6：worker 复核与单文档入口共用文档级锁，并发不超卖。
//
// PG/MySQL 的 details 谓词写法只有代码核验（环境无这两库，同 D-3 口径）；
// SQLite 分支在本文件真实执行。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const workerQuotaTenant uint64 = 7

// workerQuotaRepo 是内存知识仓储：只实现 worker/重解析路径用到的读改方法，
// 其余接口方法由 nil 嵌入兜底（不会被调用）。并发用例会同时进入，内部加锁。
type workerQuotaRepo struct {
	interfaces.KnowledgeRepository
	mu   sync.Mutex
	rows map[string]*types.Knowledge
}

func newWorkerQuotaRepo(docIDs ...string) *workerQuotaRepo {
	r := &workerQuotaRepo{rows: map[string]*types.Knowledge{}}
	for _, id := range docIDs {
		row := &types.Knowledge{
			ID: id, TenantID: workerQuotaTenant, KnowledgeBaseID: "kb",
			Type: types.KnowledgeTypeManual, ParseStatus: types.ParseStatusFailed,
			EnableStatus: "enabled",
		}
		if err := row.SetManualMetadata(
			types.NewManualKnowledgeMetadata("# 内容", types.ManualKnowledgeStatusPublish, 1),
		); err != nil {
			panic(err)
		}
		r.rows[id] = row
	}
	return r
}

func (r *workerQuotaRepo) GetKnowledgeByID(
	_ context.Context, tenant uint64, id string,
) (*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[id]
	if row == nil || row.TenantID != tenant {
		return nil, gorm.ErrRecordNotFound
	}
	copyOf := *row
	return &copyOf, nil
}

func (r *workerQuotaRepo) GetKnowledgeBatch(
	_ context.Context, tenant uint64, ids []string,
) ([]*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		if row := r.rows[id]; row != nil && row.TenantID == tenant {
			copyOf := *row
			out = append(out, &copyOf)
		}
	}
	return out, nil
}

func (r *workerQuotaRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyOf := *knowledge
	r.rows[knowledge.ID] = &copyOf
	return nil
}

func (r *workerQuotaRepo) UpdateKnowledgeColumn(
	_ context.Context, id string, column string, value interface{},
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.rows[id]; row != nil && column == "metadata" {
		if metadata, ok := value.(types.JSON); ok {
			row.Metadata = metadata
		}
	}
	return nil
}

type workerQuotaEnqueuer struct {
	interfaces.TaskEnqueuer
	mu   sync.Mutex
	seen int
}

func (e *workerQuotaEnqueuer) Enqueue(
	*asynq.Task, ...asynq.Option,
) (*asynq.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen++
	return &asynq.TaskInfo{ID: "task", Queue: types.QueueDefault}, nil
}

func (e *workerQuotaEnqueuer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seen
}

type workerQuotaEnv struct {
	db        *gorm.DB
	svc       *knowledgeService
	repo      *workerQuotaRepo
	enqueuer  *workerQuotaEnqueuer
	gate      *ReparseQuotaGate
	auditRepo interfaces.AuditLogRepository
}

func newWorkerQuotaEnv(t *testing.T, docIDs ...string) *workerQuotaEnv {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(t.TempDir()+"/reparse_quota_worker.db?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))

	repo := newWorkerQuotaRepo(docIDs...)
	enqueuer := &workerQuotaEnqueuer{}
	gate := NewReparseQuotaGate(db)
	auditRepo := repository.NewAuditLogRepository(db)
	svc := &knowledgeService{
		repo: repo,
		kbService: &documentKBLookup{values: map[string]*types.KnowledgeBase{
			"kb": {ID: "kb", TenantID: workerQuotaTenant},
		}},
		tenantRepo:   &documentTenantSpy{},
		audit:        NewAuditLogService(auditRepo),
		reparseQuota: gate,
		task:         enqueuer,
	}
	return &workerQuotaEnv{db: db, svc: svc, repo: repo, enqueuer: enqueuer, gate: gate, auditRepo: auditRepo}
}

// runWorker 按生产 payload 形状跑一次 knowledge list reparse worker。
func (e *workerQuotaEnv) runWorker(t *testing.T, payload types.KnowledgeListReparsePayload) error {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return e.svc.ProcessKnowledgeListReparse(
		context.Background(), asynq.NewTask(types.TypeKnowledgeListReparse, raw),
	)
}

// seedQuotaAudit 按真实落库结构写一条 reparse_started 审计行；exempt 行
// 带 details.quota_exempt=true（rebuild/替换/超管来源的形状）。
func (e *workerQuotaEnv) seedQuotaAudit(t *testing.T, docID string, age time.Duration, exempt bool) {
	t.Helper()
	details := map[string]any{"title": "doc", "type": "file", "attempt": 1}
	if exempt {
		details["quota_exempt"] = true
	}
	raw, err := json.Marshal(details)
	require.NoError(t, err)
	row := &types.AuditLog{
		TenantID: workerQuotaTenant, Action: types.AuditActionKnowledgeReparseStarted,
		ScopeType: "knowledge_base", ScopeID: "kb", TargetType: "knowledge",
		TargetID: docID, Outcome: types.AuditOutcomeAccepted,
		Details: types.JSON(raw), CreatedAt: time.Now().Add(-age),
	}
	require.NoError(t, e.db.Create(row).Error)
}

func (e *workerQuotaEnv) quotaCount(t *testing.T, docID string) (int64, time.Time) {
	t.Helper()
	count, earliest, err := e.auditRepo.CountRecentByAction(
		context.Background(), workerQuotaTenant, types.AuditActionKnowledgeReparseStarted,
		"knowledge", docID, time.Now().Add(-24*time.Hour),
	)
	require.NoError(t, err)
	return count, earliest
}

func (e *workerQuotaEnv) rawAuditRows(t *testing.T, docID string) []types.AuditLog {
	t.Helper()
	var rows []types.AuditLog
	require.NoError(t, e.db.Where(
		"tenant_id = ? AND action = ? AND target_type = ? AND target_id = ?",
		workerQuotaTenant, types.AuditActionKnowledgeReparseStarted, "knowledge", docID,
	).Order("id ASC").Find(&rows).Error)
	return rows
}

// B1：普通成员批量 worker 照常计入——2 次历史 + 1 次 worker = 3。
func TestCP3R1_B1_WorkerBatchCountsNormalMemberRetry(t *testing.T) {
	const doc = "doc-b1"
	env := newWorkerQuotaEnv(t, doc)
	env.seedQuotaAudit(t, doc, time.Hour, false)
	env.seedQuotaAudit(t, doc, 30*time.Minute, false)

	err := env.runWorker(t, types.KnowledgeListReparsePayload{
		TenantID: workerQuotaTenant, KnowledgeBaseID: "kb", KnowledgeIDs: []string{doc},
	})
	require.NoError(t, err)
	require.Equal(t, 1, env.enqueuer.count(), "有额度时 worker 应提交一次")

	count, _ := env.quotaCount(t, doc)
	require.Equal(t, int64(3), count, "普通成员批量重试计入额度（2+1）")
	rows := env.rawAuditRows(t, doc)
	require.Len(t, rows, 3)
	require.NotContains(t, string(rows[2].Details), `"quota_exempt":true`, "普通行不得带豁免标记")
}

// B2：rebuild-index 来源的 worker 跑完后，普通成员计数仍为 0。
func TestCP3R1_B2_RebuildWorkerDoesNotConsumeNormalQuota(t *testing.T) {
	const doc = "doc-b2"
	env := newWorkerQuotaEnv(t, doc)

	err := env.runWorker(t, types.KnowledgeListReparsePayload{
		TenantID: workerQuotaTenant, KnowledgeBaseID: "kb", KnowledgeIDs: []string{doc},
		QuotaExempt: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, env.enqueuer.count(), "rebuild 来源仍应提交解析任务")

	count, _ := env.quotaCount(t, doc)
	require.Zero(t, count, "rebuild 行 quota_exempt 排除后普通成员计数为 0")
	rows := env.rawAuditRows(t, doc)
	require.Len(t, rows, 1, "审计行照写留痕")
	require.Contains(t, string(rows[0].Details), `"quota_exempt":true`)
}

// B3：文件替换不扣额度——替换完成后普通成员计数不变（仍为 0）。
func TestCP3R1_B3_FileReplacementDoesNotConsumeQuota(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open(t.TempDir()+"/reparse_quota_replace.db?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	auditRepo := repository.NewAuditLogRepository(db)

	h := newReplaceFileHarness(t)
	h.svc.audit = NewAuditLogService(auditRepo)

	got, err := h.replace(t, "# new body", "notes/sub/b.md", nil)
	require.NoError(t, err)
	require.Equal(t, h.original.ID, got.ID)
	require.Len(t, h.tasks.payloads, 1, "替换触发的重解析仍应入队")

	count, _, err := auditRepo.CountRecentByAction(
		context.Background(), 7, types.AuditActionKnowledgeReparseStarted,
		"knowledge", h.original.ID, time.Now().Add(-24*time.Hour),
	)
	require.NoError(t, err)
	require.Zero(t, count, "替换行 quota_exempt 排除后计数为 0")

	var rows []types.AuditLog
	require.NoError(t, db.Where("action = ?", types.AuditActionKnowledgeReparseStarted).Find(&rows).Error)
	require.Len(t, rows, 1, "审计行照写留痕")
	require.Contains(t, string(rows[0].Details), `"quota_exempt":true`)
}

// B4：系统管理员同步重试不占普通成员计数。
func TestCP3R1_B4_SystemAdminSyncRetryDoesNotConsumeQuota(t *testing.T) {
	const doc = "doc-b4"
	env := newWorkerQuotaEnv(t, doc)
	kb := &types.KnowledgeBase{ID: "kb", TenantID: workerQuotaTenant}

	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, workerQuotaTenant)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: workerQuotaTenant})
	ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
	ctx, err := access.WithKBTaskWrite(ctx, kb, workerQuotaTenant)
	require.NoError(t, err)

	_, err = env.svc.ReparseKnowledge(ctx, doc, nil)
	require.NoError(t, err)
	require.Equal(t, 1, env.enqueuer.count())

	count, _ := env.quotaCount(t, doc)
	require.Zero(t, count, "超管行 quota_exempt 排除后普通成员计数为 0")
	rows := env.rawAuditRows(t, doc)
	require.Len(t, rows, 1)
	require.Contains(t, string(rows[0].Details), `"quota_exempt":true`)
}

// B6：worker 复核与单文档入口共用文档级锁——count=2 起步时并发只放行 1 个，
// 最终计数 ≤3、无第 4 次 attempt。
func TestCP3R1_B6_ConcurrentWorkerAndSingleDoNotOversell(t *testing.T) {
	const doc = "doc-b6"
	env := newWorkerQuotaEnv(t, doc)
	env.seedQuotaAudit(t, doc, time.Hour, false)
	env.seedQuotaAudit(t, doc, 30*time.Minute, false)

	kb := &types.KnowledgeBase{ID: "kb", TenantID: workerQuotaTenant}
	singleCtx := context.WithValue(context.Background(), types.TenantIDContextKey, workerQuotaTenant)
	singleCtx = context.WithValue(singleCtx, types.TenantInfoContextKey, &types.Tenant{ID: workerQuotaTenant})
	singleCtx, err := access.WithKBTaskWrite(singleCtx, kb, workerQuotaTenant)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var unexpected []error
	var accepted int

	// 单文档入口：与 handler 同序（Acquire → 服务提交 → release）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		release, err := env.gate.Acquire(singleCtx, workerQuotaTenant, doc)
		mu.Lock()
		if err != nil {
			// 429 是并发下另一入口先占满额度的正常拒绝；只有非 429 才算意外。
			var appErr *apperrors.AppError
			if !errors.As(err, &appErr) || appErr.HTTPCode != 429 {
				unexpected = append(unexpected, err)
			}
			mu.Unlock()
			return
		}
		accepted++
		mu.Unlock()
		defer release()
		if _, err := env.svc.ReparseKnowledge(singleCtx, doc, nil); err != nil {
			mu.Lock()
			unexpected = append(unexpected, err)
			mu.Unlock()
		}
	}()

	// 批量 worker：逐文档 AcquireBatch 复核同文档。
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := env.runWorker(t, types.KnowledgeListReparsePayload{
			TenantID: workerQuotaTenant, KnowledgeBaseID: "kb", KnowledgeIDs: []string{doc},
		}); err != nil {
			mu.Lock()
			unexpected = append(unexpected, err)
			mu.Unlock()
		}
	}()
	wg.Wait()

	require.Empty(t, unexpected, "并发路径不得出现 5xx/内部错误")
	require.LessOrEqual(t, accepted, 1, "count=2 起步只允许 1 个单文档放行")
	count, _ := env.quotaCount(t, doc)
	require.LessOrEqual(t, count, int64(reparseQuotaLimit), "最终计数不得超 3（无第 4 次 attempt）")
	require.GreaterOrEqual(t, env.enqueuer.count(), 1, "至少一个入口成功提交")
	require.LessOrEqual(t, env.enqueuer.count(), 2, "总提交数 ≤2（count=2 → 最多补 1 次）")
}
