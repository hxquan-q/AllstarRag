package service

import (
	"context"
	"errors"
	"testing"

	files "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T39 round=2 红阶段（F48）：附件清理部分成功后重试必须幂等。
//
// 场景：第一次清理已经删掉原文件（resources.state=deleted），worker 在删除
// 解析图片前失败。重试时 resourceCatalog.Release 对已删除资源返回
// “resource not found”，当前实现把它当真实错误，任务永久卡住，后续图片
// 永远清不掉。
//
// 本轮钉死的契约（03-tickets/T39.md round=2）：
//   - 导出哨兵 ErrResourceNotFound（package service）；
//   - resourceCatalog.Resolve/Release 对不存在或已删除资源返回该哨兵；
//   - releaseTemporaryDocumentResource 对 errors.Is(err, ErrResourceNotFound)
//     视为成功并跳过该 ref，不删物理文件；
//   - worker 遇到同一哨兵继续处理后续 ref，任务成功删除；
//   - 其他存储错误仍返回并保留任务重试。

// t39r2Releaser 按 ref 返回注入的错误，用来区分“资源已删除”和真实存储故障。
type t39r2Releaser struct {
	calls  []t39ReleaseCall
	errFor map[string]error
}

func (f *t39r2Releaser) ReleaseResource(_ context.Context, documentID, ref string) error {
	f.calls = append(f.calls, t39ReleaseCall{DocumentID: documentID, ResourceRef: ref})
	return f.errFor[ref]
}

func t39r2NewWorkerWithReleaser(pending *t39PendingQueue, releaser PurgeAttachmentReleaser) PurgeCleanupService {
	return NewPurgeCleanupService(
		pending,
		&t39ChunkRepo{},
		&t39KBLookup{},
		&t39EmbeddingResolver{},
		&t39VectorEngine{},
		&t39GraphEngine{},
		releaser,
	)
}

// TestT39R2PurgeCleanupMissingResourceIsIdempotentSuccess 覆盖 F48 主场景：
// 第一个 ref（原文件）已经因上一轮清理不存在，worker 必须把它当成功跳过，
// 并继续释放后面的两张解析图片，最后删除任务行。
func TestT39R2PurgeCleanupMissingResourceIsIdempotentSuccess(t *testing.T) {
	pending := newT39PendingQueue()
	releaser := &t39r2Releaser{errFor: map[string]error{
		"resource://missing-source": ErrResourceNotFound,
	}}
	t39r1QueueAttachmentTask(pending, 7, t39r1AttachmentDoc{
		ID:          "att-1",
		ResourceRef: "resource://missing-source",
		ImageRefs:   []string{"resource://img-1", "resource://img-2"},
	})
	svc := t39r2NewWorkerWithReleaser(pending, releaser)

	n, err := svc.RunOnce(context.Background())
	require.NoError(t, err, "an already-deleted resource is idempotent success, not a task failure")
	require.Equal(t, 1, n)

	require.Equal(t, []t39ReleaseCall{
		{DocumentID: "att-1", ResourceRef: "resource://missing-source"},
		{DocumentID: "att-1", ResourceRef: "resource://img-1"},
		{DocumentID: "att-1", ResourceRef: "resource://img-2"},
	}, releaser.calls, "worker must keep releasing the following image refs")
	require.Equal(t, [][]int64{{1}}, pending.deleted,
		"the retried task must be consumed once every ref is handled")
	require.Empty(t, pending.rows)
	require.Empty(t, pending.failCounts)
}

// TestT39R2PurgeCleanupRealStorageErrorKeepsTask 覆盖 F48 的失败边界：
// 真实存储错误不是幂等成功，任务必须保留、fail_count+1；后端恢复后重试成功。
func TestT39R2PurgeCleanupRealStorageErrorKeepsTask(t *testing.T) {
	pending := newT39PendingQueue()
	storageErr := errors.New("t39r2 storage backend unavailable")
	releaser := &t39r2Releaser{errFor: map[string]error{
		"resource://img-1": storageErr,
	}}
	t39r1QueueAttachmentTask(pending, 7, t39r1AttachmentDoc{
		ID:          "att-1",
		ResourceRef: "resource://source",
		ImageRefs:   []string{"resource://img-1", "resource://img-2"},
	})
	svc := t39r2NewWorkerWithReleaser(pending, releaser)

	n, err := svc.RunOnce(context.Background())
	require.NoError(t, err, "per-task failure is recorded, not returned to the ticker")
	require.Equal(t, 0, n)
	require.Len(t, pending.rows, 1, "a real storage error must keep the task for retry")
	require.Equal(t, 1, pending.failCounts[1], "fail_count must increase")
	require.Empty(t, pending.deleted, "a failed task must not be consumed")

	releaser.errFor = nil
	pending.resetRound()
	n, err = svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "the retried task must succeed once the backend recovers")
	require.Empty(t, pending.rows)
}

// t39r2ReleaseCatalog 只实现 Release 的资源目录替身，用于直接钉死
// releaseTemporaryDocumentResource 的哨兵语义。
type t39r2ReleaseCatalog struct {
	interfaces.ResourceCatalog
	errFor    map[string]error
	remaining map[string]int64
	releases  []string
}

func (c *t39r2ReleaseCatalog) Release(
	_ context.Context, ref, _, _ string,
) (int64, error) {
	c.releases = append(c.releases, ref)
	if err := c.errFor[ref]; err != nil {
		return -1, err
	}
	if remaining, ok := c.remaining[ref]; ok {
		return remaining, nil
	}
	return 0, nil
}

// TestT39R2ReleaseTemporaryDocumentResourceSkipsMissingResource 覆盖 F48 的
// 共用释放语义：资源已删除时返回成功，且不再尝试删除物理文件。
func TestT39R2ReleaseTemporaryDocumentResourceSkipsMissingResource(t *testing.T) {
	catalog := &t39r2ReleaseCatalog{errFor: map[string]error{
		"resource://gone": ErrResourceNotFound,
	}}
	fileSvc := &t39r1Files{}

	err := releaseTemporaryDocumentResource(
		context.Background(), catalog, fileSvc, "att-1", "resource://gone")
	require.NoError(t, err, "a resource already marked deleted must be treated as released")
	require.Empty(t, fileSvc.deleted,
		"there are no bytes left to delete; physical delete must be skipped")
}

// TestT39R2ReleaseTemporaryDocumentResourcePropagatesRealError 覆盖 F48 的
// 反向边界：真实错误不能被当成“资源已删除”吞掉。
func TestT39R2ReleaseTemporaryDocumentResourcePropagatesRealError(t *testing.T) {
	storageErr := errors.New("t39r2 storage unavailable")
	catalog := &t39r2ReleaseCatalog{errFor: map[string]error{
		"resource://broken": storageErr,
	}}
	fileSvc := &t39r1Files{}

	err := releaseTemporaryDocumentResource(
		context.Background(), catalog, fileSvc, "att-1", "resource://broken")
	require.Error(t, err)
	require.ErrorIs(t, err, storageErr)
	require.False(t, errors.Is(err, ErrResourceNotFound),
		"a real storage error must not be classified as a missing resource")
	require.Empty(t, fileSvc.deleted)
}

// TestT39R2ResourceCatalogReleaseDeletedResourceReturnsSentinel 钉死 F48 根因：
// resources.state=deleted 后，Release 必须返回可 errors.Is 的 ErrResourceNotFound。
func TestT39R2ResourceCatalogReleaseDeletedResourceReturnsSentinel(t *testing.T) {
	catalog, _ := newResourceCatalogForTest(t)
	ctx := context.Background()

	ref, err := catalog.Register(ctx, 7, "local://7/chat/attachment.pdf",
		interfaces.ResourceRegistration{OriginalName: "attachment.pdf"})
	require.NoError(t, err)
	require.NoError(t, catalog.MarkDeleted(ctx, ref))

	_, err = catalog.Release(ctx, ref, types.ResourceOwnerTemporaryDocument, "att-1")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrResourceNotFound,
		"a deleted resource must surface the sentinel so cleanup can treat it as success")
}

// TestT39R2PurgeCleanupRetryAfterPartialAttachmentSuccess 用真实资源目录与
// 文件服务重现 F48 的原始事故：首次清理已删原文件，重试任务仍带着原文件与
// 两张解析图片；worker 必须成功消费任务并把两张图片的物理文件删掉。
func TestT39R2PurgeCleanupRetryAfterPartialAttachmentSuccess(t *testing.T) {
	ctx := context.Background()
	catalog, _ := newResourceCatalogForTest(t)
	fileSvc := files.NewResourceCatalogFileService(
		files.NewLocalFileService(t.TempDir(), ""), catalog)

	sourceRef, err := fileSvc.SaveBytes(ctx, []byte("source pdf bytes"), 7, "source.pdf", false)
	require.NoError(t, err)
	image1Ref, err := fileSvc.SaveBytes(ctx, []byte("first image bytes"), 7, "first.png", false)
	require.NoError(t, err)
	image2Ref, err := fileSvc.SaveBytes(ctx, []byte("second image bytes"), 7, "second.png", false)
	require.NoError(t, err)
	for _, ref := range []string{sourceRef, image1Ref, image2Ref} {
		require.NoError(t, catalog.Bind(
			ctx, ref, types.ResourceOwnerTemporaryDocument, "att-1",
			types.ResourceRelationSourceFile))
	}

	// 上一轮已成功删除原文件，worker 在删图片前中断。
	require.NoError(t, fileSvc.DeleteFile(ctx, sourceRef))

	pending := newT39PendingQueue()
	t39r1QueueAttachmentTask(pending, 7, t39r1AttachmentDoc{
		ID:          "att-1",
		ResourceRef: sourceRef,
		ImageRefs:   []string{image1Ref, image2Ref},
	})
	svc := t39r2NewWorkerWithReleaser(
		pending, NewTemporaryDocumentReleaser(catalog, fileSvc))

	n, err := svc.RunOnce(ctx)
	require.NoError(t, err, "the retry must not stall on the already-deleted source resource")
	require.Equal(t, 1, n)
	require.Empty(t, pending.rows, "the retried task must be consumed")

	for _, ref := range []string{image1Ref, image2Ref} {
		file, getErr := fileSvc.GetFile(ctx, ref)
		if getErr == nil {
			_ = file.Close()
		}
		require.Error(t, getErr, "parsed image %s must be physically cleaned up", ref)
	}
}

// TestT39R2PurgeCleanupKeepsSharedImageWithRealCatalog 是共享图片保护网：
// 图片仍被第二个附件引用时，worker 释放第一个附件的引用后不得删文件。
func TestT39R2PurgeCleanupKeepsSharedImageWithRealCatalog(t *testing.T) {
	ctx := context.Background()
	catalog, _ := newResourceCatalogForTest(t)
	fileSvc := files.NewResourceCatalogFileService(
		files.NewLocalFileService(t.TempDir(), ""), catalog)

	sharedRef, err := fileSvc.SaveBytes(ctx, []byte("shared image bytes"), 7, "shared.png", false)
	require.NoError(t, err)
	for _, documentID := range []string{"att-1", "att-2"} {
		require.NoError(t, catalog.Bind(
			ctx, sharedRef, types.ResourceOwnerTemporaryDocument, documentID,
			types.ResourceRelationSourceFile))
	}

	pending := newT39PendingQueue()
	t39r1QueueAttachmentTask(pending, 7, t39r1AttachmentDoc{
		ID:        "att-1",
		ImageRefs: []string{sharedRef},
	})
	svc := t39r2NewWorkerWithReleaser(
		pending, NewTemporaryDocumentReleaser(catalog, fileSvc))

	n, err := svc.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	file, err := fileSvc.GetFile(ctx, sharedRef)
	require.NoError(t, err, "an image still claimed by another attachment must survive")
	require.NoError(t, file.Close())

	remaining, err := catalog.Release(
		ctx, sharedRef, types.ResourceOwnerTemporaryDocument, "att-2")
	require.NoError(t, err)
	require.Zero(t, remaining, "the second owner still holds the last claim")
}
