package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T39 TDD 红阶段：PurgeCleanupService worker 契约（P1/P2）。
//
// 假定契约（03-tickets/T39.md）：
//   - 新增 internal/application/service/purge_cleanup.go，暴露
//     PurgeCleanupService{RunOnce(ctx) (int, error)}；
//   - 构造注入队列仓储、chunk 仓储、KB 查询、embedding 解析、向量引擎、
//     图谱引擎、附件资源释放窄接口；
//   - RunOnce 用 ClaimBatch("purge:cleanup","purge","global",limit,staleBefore)
//     领取任务，成功后 DeleteByIDs，失败 IncrFailCount 保留重试；
//   - 单个任务失败不向调用方返回 error（claim 基础设施失败才返回），
//     返回值是本轮成功处理的任务数；
//   - knowledge 任务：chunk 删除 → 按 KB 解析 embedding model → 向量删除
//     （聊天历史 knowledge 类型为 "passage"）→ 图谱删除；
//   - attachment 任务：只对 payload 列出的文档释放 resource ref/文件。
//
// 本文件在新符号存在前会导致 service 包编译失败，这是本轮预期的红。

type t39ClaimCall struct {
	TaskType  string
	Scope     string
	ScopeID   string
	Limit     int
	StaleTime time.Time
}

type t39ChunkDeleteCall struct {
	TenantID     uint64
	KnowledgeIDs []string
}

type t39VectorDeleteCall struct {
	KnowledgeIDs  []string
	Dimension     int
	KnowledgeType string
}

type t39ReleaseCall struct {
	DocumentID  string
	ResourceRef string
}

// t39PendingQueue 是 TaskPendingOpsRepository 的内存假实现。它按轮次记录
// 已领取的行，避免 RunOnce 内部循环领取时重复处理同一行。
type t39PendingQueue struct {
	interfaces.TaskPendingOpsRepository

	rows             []*types.TaskPendingOp
	claimedThisRound map[int64]struct{}
	claimCalls       []t39ClaimCall
	deleted          [][]int64
	failCounts       map[int64]int
	keepRowsOnDelete bool
}

func newT39PendingQueue() *t39PendingQueue {
	return &t39PendingQueue{
		claimedThisRound: map[int64]struct{}{},
		failCounts:       map[int64]int{},
	}
}

func (q *t39PendingQueue) resetRound() {
	q.claimedThisRound = map[int64]struct{}{}
}

func (q *t39PendingQueue) enqueue(op *types.TaskPendingOp) {
	if op.ID == 0 {
		op.ID = int64(len(q.rows) + 1)
	}
	q.rows = append(q.rows, op)
}

func (q *t39PendingQueue) ClaimBatch(
	_ context.Context, taskType, scope, scopeID string, limit int, staleBefore time.Time,
) ([]*types.TaskPendingOp, error) {
	q.claimCalls = append(q.claimCalls, t39ClaimCall{
		TaskType: taskType, Scope: scope, ScopeID: scopeID,
		Limit: limit, StaleTime: staleBefore,
	})
	out := make([]*types.TaskPendingOp, 0, len(q.rows))
	for _, row := range q.rows {
		if _, ok := q.claimedThisRound[row.ID]; ok {
			continue
		}
		q.claimedThisRound[row.ID] = struct{}{}
		out = append(out, row)
	}
	return out, nil
}

func (q *t39PendingQueue) DeleteByIDs(_ context.Context, ids []int64) error {
	q.deleted = append(q.deleted, append([]int64(nil), ids...))
	if q.keepRowsOnDelete {
		return nil
	}
	keep := q.rows[:0]
	for _, row := range q.rows {
		drop := false
		for _, id := range ids {
			if row.ID == id {
				drop = true
				break
			}
		}
		if !drop {
			keep = append(keep, row)
		}
	}
	q.rows = keep
	return nil
}

func (q *t39PendingQueue) IncrFailCount(_ context.Context, id int64) (int, error) {
	q.failCounts[id]++
	return q.failCounts[id], nil
}

func (q *t39PendingQueue) ReleaseByIDs(_ context.Context, _ []int64) error {
	// 生产契约要求失败走 IncrFailCount；这里实现为空，避免 fake 因嵌入 nil
	// 接口在实现选择 Release 路径时 panic。
	return nil
}

type t39ChunkRepo struct {
	interfaces.ChunkRepository
	calls []t39ChunkDeleteCall
	err   error
}

func (f *t39ChunkRepo) DeleteByKnowledgeList(
	_ context.Context, tenantID uint64, knowledgeIDs []string,
) error {
	f.calls = append(f.calls, t39ChunkDeleteCall{
		TenantID: tenantID, KnowledgeIDs: append([]string(nil), knowledgeIDs...),
	})
	return f.err
}

type t39KBLookup struct {
	interfaces.KnowledgeBaseRepository
	kb    *types.KnowledgeBase
	err   error
	calls []string
}

func (f *t39KBLookup) GetKnowledgeBaseByID(
	_ context.Context, id string,
) (*types.KnowledgeBase, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.kb, nil
}

type t39Embedder struct {
	embedding.Embedder
	dims int
}

func (e t39Embedder) GetDimensions() int { return e.dims }

type t39EmbeddingResolver struct {
	interfaces.ModelService
	dims  int
	err   error
	calls []string
}

func (f *t39EmbeddingResolver) GetEmbeddingModel(
	_ context.Context, modelID string,
) (embedding.Embedder, error) {
	f.calls = append(f.calls, modelID)
	if f.err != nil {
		return nil, f.err
	}
	return t39Embedder{dims: f.dims}, nil
}

type t39VectorEngine struct {
	interfaces.RetrieveEngineRepository
	calls []t39VectorDeleteCall
	err   error
}

func (f *t39VectorEngine) DeleteByKnowledgeIDList(
	_ context.Context, knowledgeIDList []string, dimension int, knowledgeType string,
) error {
	f.calls = append(f.calls, t39VectorDeleteCall{
		KnowledgeIDs:  append([]string(nil), knowledgeIDList...),
		Dimension:     dimension,
		KnowledgeType: knowledgeType,
	})
	return f.err
}

type t39GraphEngine struct {
	interfaces.RetrieveGraphRepository
	calls []types.NameSpace
	err   error
}

func (f *t39GraphEngine) DelGraph(_ context.Context, namespaces []types.NameSpace) error {
	f.calls = append(f.calls, namespaces...)
	return f.err
}

// t39AttachmentReleaser 同时嵌入 TemporaryDocumentService，兼容 ② 选择
// “新增窄接口”或“在 TemporaryDocumentService 上补 ReleaseResource”两种接法。
type t39AttachmentReleaser struct {
	interfaces.TemporaryDocumentService
	calls []t39ReleaseCall
	err   error
}

func (f *t39AttachmentReleaser) ReleaseResource(
	_ context.Context, documentID, ref string,
) error {
	f.calls = append(f.calls, t39ReleaseCall{DocumentID: documentID, ResourceRef: ref})
	return f.err
}

type t39WorkerHarness struct {
	pending  *t39PendingQueue
	chunks   *t39ChunkRepo
	kbs      *t39KBLookup
	models   *t39EmbeddingResolver
	vectors  *t39VectorEngine
	graph    *t39GraphEngine
	releaser *t39AttachmentReleaser
	svc      PurgeCleanupService
}

func newT39WorkerHarness(t *testing.T) *t39WorkerHarness {
	t.Helper()
	h := &t39WorkerHarness{
		pending:  newT39PendingQueue(),
		chunks:   &t39ChunkRepo{},
		kbs:      &t39KBLookup{},
		models:   &t39EmbeddingResolver{},
		vectors:  &t39VectorEngine{},
		graph:    &t39GraphEngine{},
		releaser: &t39AttachmentReleaser{},
	}
	h.svc = NewPurgeCleanupService(
		h.pending, h.chunks, h.kbs, h.models, h.vectors, h.graph, h.releaser,
	)
	require.NotNil(t, h.svc)
	return h
}

func (h *t39WorkerHarness) runOnce(t *testing.T) (int, error) {
	t.Helper()
	h.pending.resetRound()
	return h.svc.RunOnce(context.Background())
}

func t39QueueKnowledgeTask(q *t39PendingQueue, tenantID uint64, kbID string, ids ...string) {
	q.enqueue(&types.TaskPendingOp{
		TaskType: "purge:cleanup",
		Scope:    "purge",
		ScopeID:  "global",
		Op:       "knowledge",
		DedupKey: "purge:user-1:knowledge",
		Payload: mustJSONNoT(t39KnowledgePayload{
			TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDs: ids,
		}),
	})
}

func t39QueueAttachmentTask(q *t39PendingQueue, tenantID uint64, docs ...t39AttachmentDoc) {
	q.enqueue(&types.TaskPendingOp{
		TaskType: "purge:cleanup",
		Scope:    "purge",
		ScopeID:  "global",
		Op:       "attachment",
		DedupKey: "purge:user-1:attachment",
		Payload:  mustJSONNoT(t39AttachmentPayload{TenantID: tenantID, Documents: docs}),
	})
}

// mustJSONNoT 只在种子数据（已知可序列化）里用，避免每个 helper 都传 *testing.T。
func mustJSONNoT(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestT39PurgeCleanupKnowledgeTask 覆盖 AC-2 的 knowledge 分支：
// 按 tenant/kb 删 chunks，按 KB 的 embedding model 维度删向量，删图谱，
// 成功后删除任务行。
func TestT39PurgeCleanupKnowledgeTask(t *testing.T) {
	h := newT39WorkerHarness(t)
	h.kbs.kb = &types.KnowledgeBase{ID: "kb-chat", EmbeddingModelID: "emb-1"}
	h.models.dims = 768
	t39QueueKnowledgeTask(h.pending, 7, "kb-chat", "k-1", "k-2")

	n, err := h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 1, n, "one successfully processed task")

	require.Len(t, h.pending.claimCalls, 1)
	require.Equal(t, "purge:cleanup", h.pending.claimCalls[0].TaskType)
	require.Equal(t, "purge", h.pending.claimCalls[0].Scope)
	require.Equal(t, "global", h.pending.claimCalls[0].ScopeID)
	require.Positive(t, h.pending.claimCalls[0].Limit)

	require.Equal(t, []t39ChunkDeleteCall{
		{TenantID: 7, KnowledgeIDs: []string{"k-1", "k-2"}},
	}, h.chunks.calls, "chunks must be deleted by tenant and knowledge IDs")

	require.Equal(t, []string{"emb-1"}, h.models.calls,
		"the KB's embedding model must be resolved for vector cleanup")
	require.Equal(t, []t39VectorDeleteCall{
		{KnowledgeIDs: []string{"k-1", "k-2"}, Dimension: 768, KnowledgeType: "passage"},
	}, h.vectors.calls, "vectors must be deleted with the resolved dimension and chat type")

	require.Equal(t, []types.NameSpace{
		{KnowledgeBase: "kb-chat", Knowledge: "k-1"},
		{KnowledgeBase: "kb-chat", Knowledge: "k-2"},
	}, h.graph.calls, "graph namespaces must cover every knowledge entry")

	require.Equal(t, [][]int64{{1}}, h.pending.deleted,
		"the consumed task row must be deleted after success")
	require.Empty(t, h.pending.rows, "a successful task must be removed")
	require.Empty(t, h.releaser.calls, "knowledge task must not touch attachments")
}

// TestT39PurgeCleanupAttachmentTaskOnlyListedDocuments 覆盖 AC-2 的
// attachment 分支：只对 payload 里列出的独占文档释放 resource ref；未列出的
// 共享附件一律不碰。
func TestT39PurgeCleanupAttachmentTaskOnlyListedDocuments(t *testing.T) {
	h := newT39WorkerHarness(t)
	t39QueueAttachmentTask(h.pending, 7,
		t39AttachmentDoc{ID: "att-exclusive", ResourceRef: "local://attachments/exclusive.pdf"})

	n, err := h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.Equal(t, []t39ReleaseCall{
		{DocumentID: "att-exclusive", ResourceRef: "local://attachments/exclusive.pdf"},
	}, h.releaser.calls,
		"only documents listed in the payload may be released; shared attachments stay untouched")
	require.Empty(t, h.chunks.calls)
	require.Empty(t, h.vectors.calls)
	require.Empty(t, h.graph.calls)
	require.Empty(t, h.pending.rows, "a successful task must be removed")
}

// TestT39PurgeCleanupFailureKeepsTaskAndRetries 覆盖 AC-3：
// 清理失败时任务保留、fail_count+1、不删除任务行；下一轮成功后删除。
func TestT39PurgeCleanupFailureKeepsTaskAndRetries(t *testing.T) {
	h := newT39WorkerHarness(t)
	h.kbs.kb = &types.KnowledgeBase{ID: "kb-chat", EmbeddingModelID: "emb-1"}
	h.models.dims = 768
	h.chunks.err = errors.New("t39 injected chunk delete failure")
	t39QueueKnowledgeTask(h.pending, 7, "kb-chat", "k-1")

	n, err := h.runOnce(t)
	require.NoError(t, err, "per-task failure is recorded, not returned to the ticker")
	require.Equal(t, 0, n)
	require.Empty(t, h.pending.deleted, "a failed task must not be consumed")
	require.Len(t, h.pending.rows, 1, "a failed task must stay queued")
	require.Equal(t, 1, h.pending.failCounts[1], "fail_count must increase")

	h.chunks.err = nil
	h.chunks.calls = nil
	h.vectors.calls = nil
	h.graph.calls = nil

	n, err = h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the retried task must succeed")
	require.Len(t, h.pending.deleted, 1)
	require.Empty(t, h.pending.rows, "success on retry removes the task")
	require.Len(t, h.chunks.calls, 1)
}

// TestT39PurgeCleanupRepeatIsIdempotent 覆盖 AC-4：
// 模拟 at-least-once 重复投递同一任务，重复执行不报错，且只重复同一批删除，
// 不会误删 payload 之外的数据。
func TestT39PurgeCleanupRepeatIsIdempotent(t *testing.T) {
	h := newT39WorkerHarness(t)
	h.pending.keepRowsOnDelete = true
	t39QueueAttachmentTask(h.pending, 7,
		t39AttachmentDoc{ID: "att-1", ResourceRef: "local://attachments/a.pdf"})

	for i := 0; i < 2; i++ {
		n, err := h.runOnce(t)
		require.NoErrorf(t, err, "repeat run %d must not fail", i+1)
		require.Equal(t, 1, n)
	}

	require.Equal(t, []t39ReleaseCall{
		{DocumentID: "att-1", ResourceRef: "local://attachments/a.pdf"},
		{DocumentID: "att-1", ResourceRef: "local://attachments/a.pdf"},
	}, h.releaser.calls,
		"repeat delivery must only repeat the same idempotent deletion")
	require.Len(t, h.pending.deleted, 2)
	require.Empty(t, h.chunks.calls)
	require.Empty(t, h.vectors.calls)
	require.Empty(t, h.graph.calls)
}

// TestT39PurgeCleanupEmptyQueueDoesNothing 覆盖 AC-5：
// 空队列时 RunOnce 返回 0，不调用任何外部清理。
func TestT39PurgeCleanupEmptyQueueDoesNothing(t *testing.T) {
	h := newT39WorkerHarness(t)

	n, err := h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Len(t, h.pending.claimCalls, 1)
	require.Equal(t, "purge:cleanup", h.pending.claimCalls[0].TaskType)
	require.Equal(t, "purge", h.pending.claimCalls[0].Scope)
	require.Equal(t, "global", h.pending.claimCalls[0].ScopeID)
	require.Empty(t, h.chunks.calls)
	require.Empty(t, h.vectors.calls)
	require.Empty(t, h.graph.calls)
	require.Empty(t, h.releaser.calls)
	require.Empty(t, h.pending.deleted)
}
