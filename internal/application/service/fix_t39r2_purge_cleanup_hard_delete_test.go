package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T39 round=2 红阶段（F49）：聊天历史 chunks 必须物理删除。
//
// 现状：worker 调用 DeleteByKnowledgeList 只做软删除，聊天历史 knowledge 没有
// 替代版本，正文永远留在库里；chunk_revisions 也没有随知识删除清理。
//
// 本轮钉死的契约（03-tickets/T39.md round=2）：
//   - ChunkRepository 新增 HardDeleteByKnowledgeList(ctx, tenantID, knowledgeIDs)
//     error，用 Unscoped 物理删除这些 knowledge 的 chunks；
//   - 同一事务内删除这些 chunks 的 chunk_revisions；
//   - 只处理传入 tenant + knowledge，其他 knowledge / 其他租户数据不动；
//   - 重复执行幂等。

// 编译期契约：接口必须提供按 knowledge 列表物理删除的方法。
var _ = interfaces.ChunkRepository.HardDeleteByKnowledgeList

type t39r2ChunkSeed struct {
	ID          string
	TenantID    uint64
	KnowledgeID string
	Deleted     bool
}

func t39r2OpenChunkDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "t39r2_chunks.db")+"?_busy_timeout=5000"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Chunk{}, &types.ChunkRevision{}))
	return db
}

// t39r2SeedChunks 建 chunks 与对应的 chunk_revisions；Deleted=true 的 chunk
// 额外走一次 GORM 软删除，模拟聊天历史清理前后的历史行。
func t39r2SeedChunks(t *testing.T, db *gorm.DB, seeds []t39r2ChunkSeed) {
	t.Helper()
	ctx := context.Background()
	repo := apprepo.NewChunkRepository(db)

	chunks := make([]*types.Chunk, 0, len(seeds))
	for _, seed := range seeds {
		chunks = append(chunks, &types.Chunk{
			ID:              seed.ID,
			TenantID:        seed.TenantID,
			KnowledgeBaseID: "kb-chat",
			KnowledgeID:     seed.KnowledgeID,
			Content:         "正文-" + seed.ID,
			ChunkType:       types.ChunkTypeText,
			IsEnabled:       true,
		})
	}
	require.NoError(t, repo.CreateChunks(ctx, chunks))
	for _, seed := range seeds {
		if seed.Deleted {
			require.NoError(t, db.Where("id = ?", seed.ID).Delete(&types.Chunk{}).Error)
		}
		require.NoError(t, repo.CreateChunkRevision(ctx, &types.ChunkRevision{
			ID:              "rev-" + seed.ID,
			TenantID:        seed.TenantID,
			KnowledgeBaseID: "kb-chat",
			KnowledgeID:     seed.KnowledgeID,
			ChunkID:         seed.ID,
			Revision:        1,
			Content:         "旧正文-" + seed.ID,
			EditedAt:        time.Now(),
		}))
	}
}

func t39r2UnscopedCount(t *testing.T, db *gorm.DB, model any, query string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Unscoped().Model(model).Where(query, args...).Count(&count).Error)
	return count
}

// t39r2WorkerWithRealChunks 用真实 chunk 仓储装配 worker；KB 不配 embedding
// 模型，避免把向量/图谱替身混进本用例的关注点。
func t39r2WorkerWithRealChunks(
	t *testing.T, db *gorm.DB,
) (*t39PendingQueue, PurgeCleanupService) {
	t.Helper()
	pending := newT39PendingQueue()
	svc := NewPurgeCleanupService(
		pending,
		apprepo.NewChunkRepository(db),
		&t39KBLookup{kb: &types.KnowledgeBase{ID: "kb-chat"}},
		&t39EmbeddingResolver{},
		&t39VectorEngine{},
		&t39GraphEngine{},
		&t39AttachmentReleaser{},
	)
	return pending, svc
}

// TestT39R2PurgeCleanupHardDeletesChatChunksAndRevisions 覆盖 F49 主场景：
// worker 处理后目标 knowledge 的 active + 软删 chunks 在 Unscoped 下都为 0，
// chunk_revisions 也为 0；另一 knowledge 与其他租户的数据保持原样。
func TestT39R2PurgeCleanupHardDeletesChatChunksAndRevisions(t *testing.T) {
	db := t39r2OpenChunkDB(t)
	const tenantID uint64 = 7
	t39r2SeedChunks(t, db, []t39r2ChunkSeed{
		{ID: "c-a-active", TenantID: tenantID, KnowledgeID: "k-delete"},
		{ID: "c-a-soft", TenantID: tenantID, KnowledgeID: "k-delete", Deleted: true},
		{ID: "c-b-active", TenantID: tenantID, KnowledgeID: "k-keep"},
		{ID: "c-b-soft", TenantID: tenantID, KnowledgeID: "k-keep", Deleted: true},
		{ID: "c-other-tenant", TenantID: 8, KnowledgeID: "k-delete"},
	})

	pending, svc := t39r2WorkerWithRealChunks(t, db)
	t39QueueKnowledgeTask(pending, tenantID, "kb-chat", "k-delete")

	n, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, pending.rows, "a successful knowledge task must be consumed")

	require.Zero(t, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-delete"),
		"active and soft-deleted chunks of the purged knowledge must be physically gone")
	require.Zero(t, t39r2UnscopedCount(t, db, &types.ChunkRevision{},
		"tenant_id = ? AND chunk_id IN ?", tenantID,
		[]string{"c-a-active", "c-a-soft"}),
		"revisions of the purged chunks must be deleted in the same cleanup")

	require.EqualValues(t, 2, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-keep"),
		"another knowledge must keep both its active and soft-deleted rows")
	require.EqualValues(t, 2, t39r2UnscopedCount(t, db, &types.ChunkRevision{},
		"tenant_id = ? AND chunk_id IN ?", tenantID,
		[]string{"c-b-active", "c-b-soft"}),
		"another knowledge's revisions must stay untouched")

	require.EqualValues(t, 1, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", uint64(8), "k-delete"),
		"the same knowledge ID under another tenant must never be touched")
	require.EqualValues(t, 1, t39r2UnscopedCount(t, db, &types.ChunkRevision{},
		"tenant_id = ? AND chunk_id = ?", uint64(8), "c-other-tenant"),
		"another tenant's revisions must never be touched")
}

// TestT39R2HardDeleteByKnowledgeListIsTransactional 覆盖 F49 的“同事务”要求：
// 用 SQLite 触发器让 chunk_revisions 删除失败，chunks 的删除必须一起回滚；
// 移除故障后再次调用成功，两个知识的数据边界仍然正确。
func TestT39R2HardDeleteByKnowledgeListIsTransactional(t *testing.T) {
	db := t39r2OpenChunkDB(t)
	const tenantID uint64 = 7
	t39r2SeedChunks(t, db, []t39r2ChunkSeed{
		{ID: "c-a-active", TenantID: tenantID, KnowledgeID: "k-delete"},
		{ID: "c-a-soft", TenantID: tenantID, KnowledgeID: "k-delete", Deleted: true},
		{ID: "c-b-active", TenantID: tenantID, KnowledgeID: "k-keep"},
	})
	repo := apprepo.NewChunkRepository(db)
	ctx := context.Background()

	require.NoError(t, db.Exec(`CREATE TRIGGER t39r2_block_revision_delete
		BEFORE DELETE ON chunk_revisions
		BEGIN
			SELECT RAISE(ABORT, 't39r2 injected revision delete failure');
		END;`).Error)

	err := repo.HardDeleteByKnowledgeList(ctx, tenantID, []string{"k-delete"})
	require.Error(t, err, "a revision delete failure must surface to the worker")
	require.EqualValues(t, 2, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-delete"),
		"chunk deletion must roll back together with the revision failure")
	require.EqualValues(t, 2, t39r2UnscopedCount(t, db, &types.ChunkRevision{},
		"tenant_id = ? AND chunk_id IN ?", tenantID,
		[]string{"c-a-active", "c-a-soft"}),
		"failed revisions must remain after rollback")

	require.NoError(t, db.Exec(`DROP TRIGGER t39r2_block_revision_delete`).Error)
	require.NoError(t, repo.HardDeleteByKnowledgeList(ctx, tenantID, []string{"k-delete"}))
	require.Zero(t, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-delete"))
	require.Zero(t, t39r2UnscopedCount(t, db, &types.ChunkRevision{},
		"tenant_id = ? AND chunk_id IN ?", tenantID,
		[]string{"c-a-active", "c-a-soft"}))
	require.EqualValues(t, 1, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-keep"),
		"a retry after the failure must still only touch the listed knowledge")
}

// TestT39R2PurgeCleanupHardDeleteIsIdempotent 覆盖 F49 的幂等要求：
// 同一任务重复投递两次都不报错，且不会扩大删除范围。
func TestT39R2PurgeCleanupHardDeleteIsIdempotent(t *testing.T) {
	db := t39r2OpenChunkDB(t)
	const tenantID uint64 = 7
	t39r2SeedChunks(t, db, []t39r2ChunkSeed{
		{ID: "c-a-active", TenantID: tenantID, KnowledgeID: "k-delete"},
		{ID: "c-a-soft", TenantID: tenantID, KnowledgeID: "k-delete", Deleted: true},
		{ID: "c-b-active", TenantID: tenantID, KnowledgeID: "k-keep"},
	})

	pending, svc := t39r2WorkerWithRealChunks(t, db)
	pending.keepRowsOnDelete = true
	t39QueueKnowledgeTask(pending, tenantID, "kb-chat", "k-delete")

	for run := 1; run <= 2; run++ {
		pending.resetRound()
		n, err := svc.RunOnce(context.Background())
		require.NoErrorf(t, err, "repeat run %d must not fail", run)
		require.Equalf(t, 1, n, "repeat run %d must still report the task as handled", run)
		require.Zerof(t, t39r2UnscopedCount(t, db, &types.Chunk{},
			"tenant_id = ? AND knowledge_id = ?", tenantID, "k-delete"),
			"repeat run %d must leave no chunks behind", run)
	}

	require.EqualValues(t, 1, t39r2UnscopedCount(t, db, &types.Chunk{},
		"tenant_id = ? AND knowledge_id = ?", tenantID, "k-keep"),
		"repeat delivery must not widen the deletion scope")
}
