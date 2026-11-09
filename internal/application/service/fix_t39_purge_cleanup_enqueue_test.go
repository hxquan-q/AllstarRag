package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// T39 TDD 红阶段：注销事务内的异步清理入队契约（P1/P2）。
//
// 假定契约（03-tickets/T39.md + 02-plan §15）：
//   - PurgeUserTx 的聊天清理步骤在删除行之前收集清理载荷，并在同一事务内
//     写入 task_pending_ops；
//   - 队列元组：task_type="purge:cleanup"、scope="purge"、scope_id="global"；
//   - op="knowledge"：payload {tenant_id, knowledge_base_id, knowledge_ids}；
//   - op="attachment"：payload {tenant_id, documents:[{id, resource_ref}]}；
//   - dedup_key 采用票面建议 purge:<user_id>:<kind>；
//   - 共享附件（同租户尚有未删除消息引用）不进 payload；
//   - 无清理对象不写空任务；事务回滚不留任务。
//
// 注意：本文件在 ② 实现前应断言失败（表中没有 purge:cleanup 行）；worker
// 测试文件 purge_cleanup_test.go 引用的新符号尚不存在，整包会先编译失败，
// 因此红证据分两步采集（见 05-tests §61.3）。

// t39TaskPendingOpsDDL 与 repository 包 task_queue_test.go 的 SQLite 建表
// 保持一致，避免把 jsonb 等 PG 类型搬进 SQLite。
const t39TaskPendingOpsDDL = `
CREATE TABLE IF NOT EXISTS task_pending_ops (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id   INTEGER NOT NULL,
    task_type   VARCHAR(64) NOT NULL,
    scope       VARCHAR(32) NOT NULL,
    scope_id    VARCHAR(64) NOT NULL,
    op          VARCHAR(32) NOT NULL,
    dedup_key   VARCHAR(128) NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT '{}',
    fail_count  INTEGER NOT NULL DEFAULT 0,
    enqueued_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    claimed_at  DATETIME
);
`

type t39KnowledgePayload struct {
	TenantID        uint64   `json:"tenant_id"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	KnowledgeIDs    []string `json:"knowledge_ids"`
}

type t39AttachmentDoc struct {
	ID          string `json:"id"`
	ResourceRef string `json:"resource_ref"`
}

type t39AttachmentPayload struct {
	TenantID  uint64             `json:"tenant_id"`
	Documents []t39AttachmentDoc `json:"documents"`
}

func newT39PurgeEnv(t *testing.T) *umTestEnv {
	t.Helper()
	// T33 环境已补齐注销事务会碰到的 sessions/messages/temporary_documents/
	// knowledges/会话派生表，T39 只需再加任务队列表。
	env := newT33PurgeEnv(t)
	require.NoError(t, env.db.Exec(t39TaskPendingOpsDDL).Error)
	return env
}

func t39PurgeTaskRows(t *testing.T, env *umTestEnv) []types.TaskPendingOp {
	t.Helper()
	var rows []types.TaskPendingOp
	require.NoError(t, env.db.
		Where("task_type = ?", "purge:cleanup").
		Order("id ASC").
		Find(&rows).Error)
	return rows
}

func t39TaskByOp(t *testing.T, rows []types.TaskPendingOp, op string) types.TaskPendingOp {
	t.Helper()
	for _, row := range rows {
		if row.Op == op {
			return row
		}
	}
	require.Failf(t, "purge:cleanup task missing", "no row with op=%q; rows=%+v", op, rows)
	return types.TaskPendingOp{}
}

// TestT39PurgeEnqueuesCleanupTasks 覆盖 AC-1：
// 注销提交后，聊天历史 knowledge 与独占附件各自入队一条 purge:cleanup 任务；
// 共享附件仍被 other 的未删除消息引用，不得进入 attachment payload。
func TestT39PurgeEnqueuesCleanupTasks(t *testing.T) {
	env := newT39PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-other", "other")

	t33SeedMessage(t, env, "m-1", "s-1", "user", "see attached", "k-1",
		types.MessageAttachments{
			{ID: "att-exclusive", FileName: "exclusive.pdf", FileType: ".pdf", FileSize: 16},
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})
	t33SeedMessage(t, env, "m-2", "s-1", "assistant", "answer", "k-2", nil)
	t33SeedMessage(t, env, "m-other", "s-other", "user", "same shared attachment", "",
		types.MessageAttachments{
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})

	t33SeedChatKnowledge(t, env, "k-1", "kb-chat")
	t33SeedChatKnowledge(t, env, "k-2", "kb-chat")
	t33SeedTemporaryDocument(t, env, "att-exclusive", "s-1", "local://attachments/exclusive.pdf")
	t33SeedTemporaryDocument(t, env, "att-shared", "s-1", "local://attachments/shared.pdf")

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t39 enqueue"))

	rows := t39PurgeTaskRows(t, env)
	require.Len(t, rows, 2, "exactly one knowledge task and one attachment task expected")

	for _, row := range rows {
		require.Equal(t, "purge", row.Scope, "scope must isolate the purge lane from wiki")
		require.Equal(t, "global", row.ScopeID, "purge lane is global")
	}

	knowledgeTask := t39TaskByOp(t, rows, "knowledge")
	require.Equal(t, "purge:target:knowledge", knowledgeTask.DedupKey,
		"dedup key follows the ticket suggestion purge:<user_id>:<kind>")
	var kp t39KnowledgePayload
	require.NoError(t, json.Unmarshal(knowledgeTask.Payload, &kp))
	require.EqualValues(t, umTenantA, kp.TenantID)
	require.Equal(t, "kb-chat", kp.KnowledgeBaseID)
	require.ElementsMatch(t, []string{"k-1", "k-2"}, kp.KnowledgeIDs,
		"all chat-history knowledge IDs of the purged account must be enqueued")

	attachmentTask := t39TaskByOp(t, rows, "attachment")
	require.Equal(t, "purge:target:attachment", attachmentTask.DedupKey)
	var ap t39AttachmentPayload
	require.NoError(t, json.Unmarshal(attachmentTask.Payload, &ap))
	require.EqualValues(t, umTenantA, ap.TenantID)
	require.Equal(t, []t39AttachmentDoc{
		{ID: "att-exclusive", ResourceRef: "local://attachments/exclusive.pdf"},
	}, ap.Documents,
		"only the exclusive attachment may enter the payload; shared attachments stay")
}

// TestT39PurgeSkipsEmptyCleanupTasks 覆盖 AC-5：
// 没有聊天历史索引、也没有独占附件时，不写空任务。
func TestT39PurgeSkipsEmptyCleanupTasks(t *testing.T) {
	env := newT39PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-other", "other")
	// 目标消息只引用一个仍被 other 未删除消息使用的共享附件，且没有 knowledge_id。
	t33SeedTemporaryDocument(t, env, "att-shared", "s-1", "local://attachments/shared.pdf")
	t33SeedMessage(t, env, "m-1", "s-1", "user", "shared only", "",
		types.MessageAttachments{
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})
	t33SeedMessage(t, env, "m-other", "s-other", "user", "still using it", "",
		types.MessageAttachments{
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t39 no empty task"))

	require.Empty(t, t39PurgeTaskRows(t, env),
		"a purge with nothing exclusive to clean must not enqueue empty tasks")
}

// TestT39PurgeRollbackLeavesNoCleanupTasks 覆盖 AC-1 的回滚分支：
// 事务末尾（审计写入）失败时，已构造的清理任务必须随事务回滚消失。
func TestT39PurgeRollbackLeavesNoCleanupTasks(t *testing.T) {
	env := newT39PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedMessage(t, env, "m-1", "s-1", "user", "private", "k-1",
		types.MessageAttachments{
			{ID: "att-1", FileName: "a.pdf", FileType: ".pdf", FileSize: 16},
		})
	t33SeedChatKnowledge(t, env, "k-1", "kb-chat")
	t33SeedTemporaryDocument(t, env, "att-1", "s-1", "local://attachments/a.pdf")

	const callbackName = "t39_fail_audit_create"
	require.NoError(t, env.db.Callback().Create().Before("gorm:create").Register(
		callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil {
				return
			}
			if tx.Statement.Table == "audit_logs" ||
				(tx.Statement.Schema != nil && tx.Statement.Schema.Table == "audit_logs") {
				_ = tx.AddError(errors.New("t39 injected audit failure"))
			}
		}))
	t.Cleanup(func() { _ = env.db.Callback().Create().Remove(callbackName) })

	err := env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "t39 rollback")
	require.ErrorContains(t, err, "t39 injected audit failure")

	require.Empty(t, t39PurgeTaskRows(t, env),
		"a rolled-back purge must not leave purge:cleanup tasks behind")
	// 账号仍活跃，证明事务整体回滚而不是只回滚任务表。
	require.True(t, umLoadUser(t, env, "target").IsActive)
}
