package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 注销清理任务固定的队列三元组，与 wiki 共用 task_pending_ops 表但互不可见。
const (
	purgeCleanupTaskType = "purge:cleanup"
	purgeCleanupScope    = "purge"
	purgeCleanupScopeID  = "global"

	purgeCleanupOpKnowledge  = "knowledge"
	purgeCleanupOpAttachment = "attachment"

	// 聊天历史 knowledge 用 CreateKnowledgeFromPassage 写入，向量类型固定 passage。
	purgeCleanupKnowledgeType = "passage"
	// 单轮最多领取的 dedup_key 数；一轮内处理完即可，不追求吞吐。
	purgeCleanupBatchLimit = 20
	// 崩溃 worker 的认领超过该时长后可被重新领取。
	purgeCleanupClaimStaleAfter = 30 * time.Minute
)

// PurgeCleanupService 消费注销事务写入的 purge:cleanup 任务，异步回收聊天
// 历史索引与独占附件的对象存储资源。失败的任务保留在队列中重试。
type PurgeCleanupService interface {
	// RunOnce 执行一轮清理，返回本轮成功处理的任务数。单个任务失败只记
	// fail_count 并保留，不向调用方返回 error；只有领取任务失败才返回 error。
	RunOnce(ctx context.Context) (int, error)
}

// PurgeKnowledgeBaseLookup 是 worker 解析聊天 KB 配置所需的窄接口。
type PurgeKnowledgeBaseLookup interface {
	GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error)
}

// PurgeEmbeddingResolver 是 worker 解析 KB embedding 模型所需的窄接口。
type PurgeEmbeddingResolver interface {
	GetEmbeddingModel(ctx context.Context, modelID string) (embedding.Embedder, error)
}

// PurgeVectorEngine 是 worker 删除向量索引所需的窄接口。
type PurgeVectorEngine interface {
	DeleteByKnowledgeIDList(
		ctx context.Context, knowledgeIDList []string, dimension int, knowledgeType string,
	) error
}

// PurgeAttachmentReleaser 是 worker 释放附件资源引用所需的窄接口。
type PurgeAttachmentReleaser interface {
	ReleaseResource(ctx context.Context, documentID, ref string) error
}

// purgeCleanupKnowledgePayload 与 enqueue 侧保持同一 JSON 字段名。
type purgeCleanupKnowledgePayload struct {
	TenantID        uint64   `json:"tenant_id"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	KnowledgeIDs    []string `json:"knowledge_ids"`
}

type purgeCleanupAttachmentDocument struct {
	ID          string   `json:"id"`
	ResourceRef string   `json:"resource_ref"`
	ImageRefs   []string `json:"image_refs"`
}

type purgeCleanupAttachmentPayload struct {
	TenantID  uint64                           `json:"tenant_id"`
	Documents []purgeCleanupAttachmentDocument `json:"documents"`
}

type purgeCleanupService struct {
	pendingRepo        interfaces.TaskPendingOpsRepository
	chunkRepo          interfaces.ChunkRepository
	kbLookup           PurgeKnowledgeBaseLookup
	embeddingResolver  PurgeEmbeddingResolver
	vectorEngine       PurgeVectorEngine
	graphEngine        interfaces.RetrieveGraphRepository
	attachmentReleaser PurgeAttachmentReleaser
}

// NewPurgeCleanupService 构造注销清理 worker。所有外部依赖都是窄接口，
// 生产环境的 KB 绑定/ownership 解析由适配器承担。
func NewPurgeCleanupService(
	pendingRepo interfaces.TaskPendingOpsRepository,
	chunkRepo interfaces.ChunkRepository,
	kbLookup PurgeKnowledgeBaseLookup,
	embeddingResolver PurgeEmbeddingResolver,
	vectorEngine PurgeVectorEngine,
	graphEngine interfaces.RetrieveGraphRepository,
	attachmentReleaser PurgeAttachmentReleaser,
) PurgeCleanupService {
	return &purgeCleanupService{
		pendingRepo:        pendingRepo,
		chunkRepo:          chunkRepo,
		kbLookup:           kbLookup,
		embeddingResolver:  embeddingResolver,
		vectorEngine:       vectorEngine,
		graphEngine:        graphEngine,
		attachmentReleaser: attachmentReleaser,
	}
}

// RunOnce 领取一轮任务并逐条处理。事务外删除失败可以安全重试：chunk/向量/
// 图谱删除与资源释放都是幂等的。
func (s *purgeCleanupService) RunOnce(ctx context.Context) (int, error) {
	rows, err := s.pendingRepo.ClaimBatch(
		ctx, purgeCleanupTaskType, purgeCleanupScope, purgeCleanupScopeID,
		purgeCleanupBatchLimit, time.Now().Add(-purgeCleanupClaimStaleAfter),
	)
	if err != nil {
		return 0, err
	}
	successIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		if err := s.process(ctx, row); err != nil {
			logger.Warnf(ctx, "[PurgeCleanup] task %d (op=%s) failed, will retry: %v",
				row.ID, row.Op, err)
			s.recordFailure(ctx, row)
			continue
		}
		successIDs = append(successIDs, row.ID)
	}
	if len(successIDs) == 0 {
		return 0, nil
	}
	if err := s.pendingRepo.DeleteByIDs(ctx, successIDs); err != nil {
		return len(successIDs), fmt.Errorf("delete consumed purge cleanup tasks: %w", err)
	}
	return len(successIDs), nil
}

// recordFailure 增加失败计数并释放认领，让任务在下一轮立即重试而不是等
// 认领过期。行本身保留。
func (s *purgeCleanupService) recordFailure(ctx context.Context, row *types.TaskPendingOp) {
	if _, err := s.pendingRepo.IncrFailCount(ctx, row.ID); err != nil {
		logger.Warnf(ctx, "[PurgeCleanup] task %d fail_count increment failed: %v", row.ID, err)
	}
	if err := s.pendingRepo.ReleaseByIDs(ctx, []int64{row.ID}); err != nil {
		logger.Warnf(ctx, "[PurgeCleanup] task %d claim release failed: %v", row.ID, err)
	}
}

func (s *purgeCleanupService) process(ctx context.Context, row *types.TaskPendingOp) error {
	switch row.Op {
	case purgeCleanupOpKnowledge:
		return s.cleanupKnowledge(ctx, row.Payload)
	case purgeCleanupOpAttachment:
		return s.cleanupAttachments(ctx, row.Payload)
	default:
		// 未知 op 不猜语义：保留任务并计入失败，交运维排查。
		return fmt.Errorf("unknown purge cleanup op %q", row.Op)
	}
}

// cleanupKnowledge 删除聊天历史 knowledge 的 chunks、向量与图谱。
// KB 行必须在（worker 需要它解析 embedding 模型与向量库绑定）；缺失时
// 任务失败重试，避免悄悄跳过向量清理。
func (s *purgeCleanupService) cleanupKnowledge(ctx context.Context, raw json.RawMessage) error {
	var payload purgeCleanupKnowledgePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode knowledge payload: %w", err)
	}
	knowledgeIDs := compactPurgeIDs(payload.KnowledgeIDs)
	if payload.TenantID == 0 || strings.TrimSpace(payload.KnowledgeBaseID) == "" ||
		len(knowledgeIDs) == 0 {
		return fmt.Errorf("invalid knowledge payload: tenant=%d kb=%q ids=%d",
			payload.TenantID, payload.KnowledgeBaseID, len(knowledgeIDs))
	}
	ctx = types.WithExecutionTenant(ctx, payload.TenantID)

	if err := s.chunkRepo.DeleteByKnowledgeList(ctx, payload.TenantID, knowledgeIDs); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	// 聊天历史 knowledge 没有替代版本：软删的正文如果只留在库里会无限
	// 累积，所以在软删后立即物理删除 chunks 与 chunk_revisions（F49）。
	if err := s.chunkRepo.HardDeleteByKnowledgeList(ctx, payload.TenantID, knowledgeIDs); err != nil {
		return fmt.Errorf("hard delete chunks: %w", err)
	}

	kb, err := s.kbLookup.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil {
		return fmt.Errorf("resolve knowledge base %s: %w", payload.KnowledgeBaseID, err)
	}
	if kb == nil {
		return fmt.Errorf("resolve knowledge base %s: not found", payload.KnowledgeBaseID)
	}
	if kb.TenantID != 0 && kb.TenantID != payload.TenantID {
		return fmt.Errorf("knowledge base %s does not belong to tenant %d",
			payload.KnowledgeBaseID, payload.TenantID)
	}

	if strings.TrimSpace(kb.EmbeddingModelID) != "" {
		embedder, err := s.embeddingResolver.GetEmbeddingModel(ctx, kb.EmbeddingModelID)
		if err != nil {
			return fmt.Errorf("resolve embedding model %s: %w", kb.EmbeddingModelID, err)
		}
		if embedder == nil {
			return fmt.Errorf("resolve embedding model %s: nil embedder", kb.EmbeddingModelID)
		}
		vectorCtx := withPurgeVectorTarget(
			ctx, payload.TenantID, payload.KnowledgeBaseID, kb.VectorStoreID)
		if err := s.vectorEngine.DeleteByKnowledgeIDList(
			vectorCtx, knowledgeIDs, embedder.GetDimensions(), purgeCleanupKnowledgeType,
		); err != nil {
			return fmt.Errorf("delete vectors: %w", err)
		}
	}

	namespaces := make([]types.NameSpace, 0, len(knowledgeIDs))
	for _, knowledgeID := range knowledgeIDs {
		namespaces = append(namespaces, types.NameSpace{
			KnowledgeBase: payload.KnowledgeBaseID,
			Knowledge:     knowledgeID,
		})
	}
	if err := s.graphEngine.DelGraph(ctx, namespaces); err != nil {
		return fmt.Errorf("delete knowledge graph: %w", err)
	}
	return nil
}

// cleanupAttachments 只释放 payload 明确列出的独占附件；共享附件从未进入
// payload，因此不会被误删。重复投递时释放同一批引用不会产生副作用。
func (s *purgeCleanupService) cleanupAttachments(ctx context.Context, raw json.RawMessage) error {
	var payload purgeCleanupAttachmentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode attachment payload: %w", err)
	}
	if payload.TenantID == 0 || len(payload.Documents) == 0 {
		return fmt.Errorf("invalid attachment payload: tenant=%d documents=%d",
			payload.TenantID, len(payload.Documents))
	}
	ctx = types.WithExecutionTenant(ctx, payload.TenantID)
	for _, document := range payload.Documents {
		documentID := strings.TrimSpace(document.ID)
		if documentID == "" {
			return fmt.Errorf("invalid attachment payload: empty document id")
		}
		// 源文件在前，解析图片在后；compactPurgeIDs 去空白并保序去重，
		// 与正常 Delete 的“同一 ref 只释放一次”语义一致。
		refs := compactPurgeIDs(append([]string{document.ResourceRef}, document.ImageRefs...))
		for _, ref := range refs {
			if err := s.attachmentReleaser.ReleaseResource(ctx, documentID, ref); err != nil {
				// 资源已删除是上一轮清理的幂等重放：跳过这个 ref 继续，
				// 不能让部分成功后的重试永久卡住（T39 round=2，F48）。
				if errors.Is(err, ErrResourceNotFound) {
					continue
				}
				return fmt.Errorf("release attachment %s ref %s: %w", documentID, ref, err)
			}
		}
	}
	return nil
}

// compactPurgeIDs 去空白并保序去重，避免同一 knowledge 被重复删除。
func compactPurgeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
