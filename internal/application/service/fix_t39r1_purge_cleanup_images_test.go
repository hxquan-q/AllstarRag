package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T39 round=1 红阶段：注销附件清理必须覆盖解析图片（P2 遗漏）。
//
// 正常 Delete 的语义（temporary_document.go:271-287）是：
//   - 释放 document.ResourceRef；
//   - 释放 document.ImageRefs 里每个非空 URL；
//   - 同一 ref 只释放一次（map 去重）。
//
// T39 的异步清理必须等价：
//   - 入队 payload 的 documents[] 增加 image_refs（去空、去重）；
//   - worker 对 resource_ref 与每个 image_refs[].url 各调用一次 ReleaseResource；
//   - 共享图片（catalog Release 返回 remaining>0）不删文件；共享附件与他人数据
//     仍保留；重复 URL 不重复释放。
//
// 当前实现只释放 resource_ref，以下 E6/E7/E8 预期红；E9 是空 image_refs 的
// 边界保护网。

// t39r1AttachmentDoc 是新 payload 形状的测试视图；不能复用 t39AttachmentDoc，
// 因为既有用例用 require.Equal 比较整个结构，额外字段的空切片会破坏比较口径。
type t39r1AttachmentDoc struct {
	ID          string   `json:"id"`
	ResourceRef string   `json:"resource_ref"`
	ImageRefs   []string `json:"image_refs"`
}

type t39r1AttachmentPayload struct {
	TenantID  uint64               `json:"tenant_id"`
	Documents []t39r1AttachmentDoc `json:"documents"`
}

// t39r1SeedTemporaryDocumentWithImages 建一条带解析图片的聊天附件记录。
// urls 允许包含空串与重复项，用来钉死入队侧的跳过/去重语义。
func t39r1SeedTemporaryDocumentWithImages(
	t *testing.T, env *umTestEnv, id, sessionID, resourceRef string, urls ...string,
) {
	t.Helper()
	images := make([]types.TemporaryDocumentImage, 0, len(urls))
	for _, url := range urls {
		images = append(images, types.TemporaryDocumentImage{URL: url})
	}
	raw, err := json.Marshal(images)
	require.NoError(t, err)
	require.NoError(t, env.db.Create(&types.TemporaryDocument{
		ID:          id,
		TenantID:    umTenantA,
		SessionID:   sessionID,
		ResourceRef: resourceRef,
		ImageRefs:   types.JSON(raw),
		FileName:    id + ".pdf",
		FileType:    ".pdf",
		FileSize:    16,
		Status:      types.TemporaryDocumentStatusReady,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}).Error)
}

// TestT39PurgeEnqueuesAttachmentImageRefs 覆盖本轮 AC-1：独占附件的解析图片
// 必须随 attachment payload 入队；空 URL 跳过、重复 URL 去重；共享附件与他人
// 文档不入队且原行保留。
func TestT39PurgeEnqueuesAttachmentImageRefs(t *testing.T) {
	env := newT39PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-other", "other")

	// 目标消息引用独占附件 att-img。
	t33SeedMessage(t, env, "m-1", "s-1", "user", "see images", "",
		types.MessageAttachments{
			{ID: "att-img", FileName: "img.pdf", FileType: ".pdf", FileSize: 16},
		})
	// other 的未删除消息仍引用 att-shared：共享附件必须保留且不入队。
	t33SeedMessage(t, env, "m-other", "s-other", "user", "still shared", "",
		types.MessageAttachments{
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})

	// 独占附件：两个非空 URL + 一个空串 + 一个重复 URL。
	t39r1SeedTemporaryDocumentWithImages(t, env, "att-img", "s-1",
		"local://attachments/img.pdf",
		"local://images/a.png",
		"",
		"local://images/b.png",
		"local://images/a.png",
	)
	t33SeedTemporaryDocument(t, env, "att-shared", "s-1", "local://attachments/shared.pdf")
	// 他人会话里的附件：不得入队、不得删除。
	t33SeedTemporaryDocument(t, env, "att-other", "s-other", "local://attachments/other.pdf")

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t39r1 image refs"))

	rows := t39PurgeTaskRows(t, env)
	require.Len(t, rows, 1, "only the exclusive attachment deserves a cleanup task")
	attachmentTask := t39TaskByOp(t, rows, "attachment")

	var payload t39r1AttachmentPayload
	require.NoError(t, json.Unmarshal(attachmentTask.Payload, &payload))
	require.EqualValues(t, umTenantA, payload.TenantID)
	require.Len(t, payload.Documents, 1, "shared and other-user documents must stay out")

	document := payload.Documents[0]
	require.Equal(t, "att-img", document.ID)
	require.Equal(t, "local://attachments/img.pdf", document.ResourceRef)
	require.ElementsMatch(t, []string{
		"local://images/a.png",
		"local://images/b.png",
	}, document.ImageRefs,
		"payload must carry every non-empty parsed image URL")
	require.Len(t, document.ImageRefs, 2,
		"empty image URLs are skipped and duplicate URLs are collapsed")

	// 共享附件与他人文档零副作用。
	require.EqualValues(t, 1,
		umCount(t, env, &types.TemporaryDocument{}, "id = ?", "att-shared"),
		"a shared attachment must survive the purge")
	require.EqualValues(t, 1,
		umCount(t, env, &types.TemporaryDocument{}, "id = ?", "att-other"),
		"another account's attachment must survive the purge")
}

// TestT39PurgeCleanupReleasesAttachmentImageRefs 覆盖本轮 AC-2/AC-4：attachment
// 任务必须对 resource_ref 与每个去重后的 image_refs 各调用一次 releaser。
func TestT39PurgeCleanupReleasesAttachmentImageRefs(t *testing.T) {
	h := newT39WorkerHarness(t)
	t39r1QueueAttachmentTask(h.pending, 7, t39r1AttachmentDoc{
		ID:          "att-img",
		ResourceRef: "local://attachments/img.pdf",
		ImageRefs: []string{
			"local://images/a.png",
			"local://images/b.png",
			"local://images/a.png",
		},
	})

	n, err := h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.ElementsMatch(t, []t39ReleaseCall{
		{DocumentID: "att-img", ResourceRef: "local://attachments/img.pdf"},
		{DocumentID: "att-img", ResourceRef: "local://images/a.png"},
		{DocumentID: "att-img", ResourceRef: "local://images/b.png"},
	}, h.releaser.calls,
		"the source file and every parsed image must be released")
	require.Len(t, h.releaser.calls, 3,
		"duplicate image URLs must be released exactly once")
	require.Empty(t, h.pending.rows, "a successful task must be removed")
}

// t39r1Catalog 模拟资源目录：remaining 决定引用归零后是否允许删文件。
type t39r1Catalog struct {
	interfaces.ResourceCatalog
	remaining map[string]int64
	releases  []string
}

func (c *t39r1Catalog) Release(
	_ context.Context, ref, _, _ string,
) (int64, error) {
	c.releases = append(c.releases, ref)
	if remaining, ok := c.remaining[ref]; ok {
		return remaining, nil
	}
	return -1, nil
}

// t39r1Files 记录真正被删除的物理文件。
type t39r1Files struct {
	interfaces.FileService
	deleted []string
}

func (f *t39r1Files) DeleteFile(_ context.Context, filePath string) error {
	f.deleted = append(f.deleted, filePath)
	return nil
}

// TestT39PurgeCleanupKeepsSharedImageAndDedupes 覆盖本轮 AC-2/AC-6：worker 通过
// 生产同款释放语义（catalog + files）处理 attachment 任务时，共享图片
// （Release 后 remaining>0）不删文件；重复 URL 只释放一次。
func TestT39PurgeCleanupKeepsSharedImageAndDedupes(t *testing.T) {
	pending := newT39PendingQueue()
	catalog := &t39r1Catalog{remaining: map[string]int64{
		"local://attachments/a.pdf":    0,
		"local://images/exclusive.png": 0,
		"local://images/shared.png":    2,
	}}
	files := &t39r1Files{}
	svc := NewPurgeCleanupService(
		pending,
		&t39ChunkRepo{},
		&t39KBLookup{},
		&t39EmbeddingResolver{},
		&t39VectorEngine{},
		&t39GraphEngine{},
		NewTemporaryDocumentReleaser(catalog, files),
	)

	t39r1QueueAttachmentTask(pending, 7, t39r1AttachmentDoc{
		ID:          "att-1",
		ResourceRef: "local://attachments/a.pdf",
		ImageRefs: []string{
			"local://images/exclusive.png",
			"local://images/shared.png",
			"local://images/exclusive.png",
		},
	})

	n, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.ElementsMatch(t, []string{
		"local://attachments/a.pdf",
		"local://images/exclusive.png",
		"local://images/shared.png",
	}, catalog.releases,
		"every unique ref (source file + images) must be released once")
	require.Len(t, catalog.releases, 3,
		"duplicate image URLs must not be released twice")

	require.ElementsMatch(t, []string{
		"local://attachments/a.pdf",
		"local://images/exclusive.png",
	}, files.deleted,
		"only refs whose claims reach zero may delete bytes")
	require.NotContains(t, files.deleted, "local://images/shared.png",
		"an image still claimed by another owner must not be deleted")
	require.Empty(t, pending.rows)
}

// TestT39PurgeAttachmentWithoutImageRefsStillReleasesResource 覆盖边界：
// 没有解析图片的独占附件仍要正常入队并释放 resource_ref，不能因新字段为空
// 而漏掉整条文档。
func TestT39PurgeAttachmentWithoutImageRefsStillReleasesResource(t *testing.T) {
	env := newT39PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedMessage(t, env, "m-1", "s-1", "user", "plain pdf", "",
		types.MessageAttachments{
			{ID: "att-plain", FileName: "plain.pdf", FileType: ".pdf", FileSize: 16},
		})
	t33SeedTemporaryDocument(t, env, "att-plain", "s-1", "local://attachments/plain.pdf")

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t39r1 no images"))

	rows := t39PurgeTaskRows(t, env)
	require.Len(t, rows, 1)
	attachmentTask := t39TaskByOp(t, rows, "attachment")
	var payload t39r1AttachmentPayload
	require.NoError(t, json.Unmarshal(attachmentTask.Payload, &payload))
	require.Len(t, payload.Documents, 1)
	require.Equal(t, "att-plain", payload.Documents[0].ID)
	require.Equal(t, "local://attachments/plain.pdf", payload.Documents[0].ResourceRef)
	require.Empty(t, payload.Documents[0].ImageRefs)

	// worker 端同样只释放 resource_ref，不产生空 ref 调用。
	h := newT39WorkerHarness(t)
	t39r1QueueAttachmentTask(h.pending, 7, t39r1AttachmentDoc{
		ID:          "att-plain",
		ResourceRef: "local://attachments/plain.pdf",
	})
	n, err := h.runOnce(t)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []t39ReleaseCall{
		{DocumentID: "att-plain", ResourceRef: "local://attachments/plain.pdf"},
	}, h.releaser.calls,
		"an attachment without parsed images still releases its source resource")
	require.Empty(t, h.pending.rows)
}

// t39r1QueueAttachmentTask 按新 payload 形状入队一条 attachment 清理任务。
func t39r1QueueAttachmentTask(
	q *t39PendingQueue, tenantID uint64, docs ...t39r1AttachmentDoc,
) {
	q.enqueue(&types.TaskPendingOp{
		TaskType: "purge:cleanup",
		Scope:    "purge",
		ScopeID:  "global",
		Op:       "attachment",
		DedupKey: "purge:user-1:attachment",
		Payload:  mustJSONNoT(t39r1AttachmentPayload{TenantID: tenantID, Documents: docs}),
	})
}
