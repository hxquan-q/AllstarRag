package service

import (
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// T33 注销清理消息/聊天历史/附件红阶段用例（F-PURGE-MSG、Q43、AC-19/AC-24）。
//
// 假定契约（T33 票 + 队长派单）：
//   - PurgeUserTx 在既有墓碑事务内，除会话外还要清掉该账号所有会话里的
//     messages（含软删历史，物理删除）；
//   - messages.knowledge_id 指向的聊天历史 KB 条目要一并物理删除；
//   - messages.attachments 里引用的 temporary_documents 记录按“独占才删”
//     判定：仍被任一未删除消息（含其他账号/其他会话）引用时保留；
//   - 知识库、知识库文档、智能体等资产保留；审计保留；
//   - 事务任一步失败整体回滚，不留半清理状态。
//
// 数据模型事实（本文件依据）：
//   - 会话 owner 在 sessions.user_id；消息只按 session_id 归属；
//   - 聊天历史 KB 条目是 knowledges 行，messages.knowledge_id 指向它；
//   - 聊天附件是 temporary_documents 行，消息正文只存 attachments JSON 的
//     {id,...} 引用；同一 document id 可被多个会话的消息引用（fork/共享）。
//
// 只在 service 层断言业务结果，不测 handler/前端。

// newT33PurgeEnv 在 T02 基础库（users/tenant_members/auth_tokens/audit_logs）
// 上补齐 T33 涉及的表；资产表用最小 DDL 建，避免把真实大表搬进 SQLite。
func newT33PurgeEnv(t *testing.T) *umTestEnv {
	t.Helper()
	env := newUMTestEnv(t)
	// PurgeUserTx 事务会碰到的全部个人数据表，避免缺表导致事务提前失败。
	require.NoError(t, env.db.AutoMigrate(
		&types.TenantInvitation{},
		&types.UserResourceFavorite{},
		&types.MCPOAuthToken{},
		&types.TenantUserEnvVar{},
		&types.Session{},
		&types.Notification{},
		&types.Message{},
		&types.TemporaryDocument{},
		&types.Knowledge{},
		// F37：会话派生的追问缓存与消息产物也要随会话清理。
		&types.MessageSuggestionSet{},
		&types.MessageArtifactRecord{},
	))
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS user_kb_pins (
			tenant_id integer NOT NULL,
			user_id varchar(36) NOT NULL,
			kb_id varchar(64) NOT NULL,
			pinned_at datetime
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id varchar(36) PRIMARY KEY,
			tenant_id integer,
			creator_id varchar(36)
		)`,
		`CREATE TABLE IF NOT EXISTS custom_agents (
			id varchar(36) PRIMARY KEY,
			tenant_id integer,
			creator_id varchar(36)
		)`,
	} {
		require.NoError(t, env.db.Exec(ddl).Error)
	}
	return env
}

// t33SeedSession 建一条账号会话。Session.BeforeCreate 会覆盖 ID，测试要按
// 固定 ID 断言，必须 SkipHooks 保留种子 ID。
func t33SeedSession(t *testing.T, env *umTestEnv, id, owner string) {
	t.Helper()
	require.NoError(t, env.db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{
		ID: id, TenantID: umTenantA, UserID: owner, Title: id,
	}).Error)
}

// t33SeedMessage 建一条消息；attachments 可为空。Message.BeforeCreate 会
// 覆盖 ID，测试要按固定 ID 断言，必须 SkipHooks 保留种子 ID。
func t33SeedMessage(
	t *testing.T, env *umTestEnv, id, sessionID, role, content string,
	knowledgeID string, attachments types.MessageAttachments,
) {
	t.Helper()
	require.NoError(t, env.db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Message{
		ID:          id,
		SessionID:   sessionID,
		Role:        role,
		Content:     content,
		KnowledgeID: knowledgeID,
		Attachments: attachments,
	}).Error)
}

// t33SeedChatKnowledge 建一条聊天历史 KB 条目（或普通 KB 文档，取决于 kbID）。
func t33SeedChatKnowledge(t *testing.T, env *umTestEnv, id, kbID string) {
	t.Helper()
	require.NoError(t, env.db.Create(&types.Knowledge{
		ID:              id,
		TenantID:        umTenantA,
		KnowledgeBaseID: kbID,
		Title:           id,
		CustomMetadata:  types.JSON(`{}`),
	}).Error)
}

// t33SeedTemporaryDocument 建一条聊天附件记录。resourceRef 是对象存储句柄。
func t33SeedTemporaryDocument(t *testing.T, env *umTestEnv, id, sessionID, resourceRef string) {
	t.Helper()
	require.NoError(t, env.db.Create(&types.TemporaryDocument{
		ID:          id,
		TenantID:    umTenantA,
		SessionID:   sessionID,
		ResourceRef: resourceRef,
		FileName:    id + ".pdf",
		FileType:    ".pdf",
		FileSize:    16,
		Status:      types.TemporaryDocumentStatusReady,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}).Error)
}

// t33SeedAssets 种知识库/智能体资产，断言 purge 后仍在。
func t33SeedAssets(t *testing.T, env *umTestEnv, userID string) {
	t.Helper()
	for _, table := range []string{"knowledge_bases", "custom_agents"} {
		require.NoError(t, env.db.Exec(
			"INSERT INTO "+table+" (id, tenant_id, creator_id) VALUES (?, ?, ?)",
			table+"-"+userID, umTenantA, userID,
		).Error)
	}
}

// t33SeedBase 建 actor/target/other 三个同空间账号。actor 是 owner。
func t33SeedBase(t *testing.T, env *umTestEnv) {
	t.Helper()
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "other", nil)
	umSeedMembership(t, env, "other", umTenantA, types.TenantRoleViewer)
}

// t33RequirePhysicalGone 断言某行按原 ID 已物理删除（连 Unscoped 都查不到）。
func t33RequirePhysicalGone(t *testing.T, env *umTestEnv, model any, id string) {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Unscoped().Model(model).Where("id = ?", id).Count(&n).Error)
	require.Zero(t, n, "%T %s must be physically deleted, not merely soft-deleted", model, id)
}

// TestT33PurgeRemovesMessagesAndChatHistory 覆盖 AC-1/2/3/4：
// 两个会话、多条消息与聊天历史 KB 条目在 purge 后按原 session/message ID
// 都查不到；普通 KB 文档与知识库/智能体资产保留；旧审计保留。
func TestT33PurgeRemovesMessagesAndChatHistory(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-2", "target")
	// 已软删的历史会话也要清（AC-1 “含软删历史”）。
	t33SeedSession(t, env, "s-3", "target")
	require.NoError(t, env.db.Exec(
		"UPDATE sessions SET deleted_at = ? WHERE id = ?", time.Now(), "s-3").Error)
	t33SeedSession(t, env, "s-other", "other")

	t33SeedMessage(t, env, "m-1", "s-1", "user", "private question one", "k-1", nil)
	t33SeedMessage(t, env, "m-2", "s-1", "assistant", "private answer one", "k-1", nil)
	t33SeedMessage(t, env, "m-3", "s-2", "user", "private question two", "k-2", nil)
	t33SeedMessage(t, env, "m-4", "s-3", "user", "history from a deleted session", "k-3", nil)
	// 已软删的消息也要清，不能只扫未删除消息。
	t33SeedMessage(t, env, "m-5", "s-1", "user", "soft-deleted message", "k-4", nil)
	require.NoError(t, env.db.Exec(
		"UPDATE messages SET deleted_at = ? WHERE id = ?", time.Now(), "m-5").Error)
	t33SeedMessage(t, env, "m-other", "s-other", "user", "other user's message", "", nil)

	t33SeedChatKnowledge(t, env, "k-1", "kb-chat")
	t33SeedChatKnowledge(t, env, "k-2", "kb-chat")
	t33SeedChatKnowledge(t, env, "k-3", "kb-chat")
	t33SeedChatKnowledge(t, env, "k-4", "kb-chat")
	// 普通知识库文档不是聊天历史，必须保留。
	t33SeedChatKnowledge(t, env, "doc-keep", "kb-normal")

	t33SeedAssets(t, env, "target")

	// 历史审计必须保留（AC-4 / AC-25b）。
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantA,
		ActorUserID:  "actor",
		Action:       types.AuditAction("rbac.member_banned"),
		TargetUserID: "target",
		Outcome:      types.AuditOutcomeSuccess,
		CreatedAt:    time.Now(),
	}).Error)

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "purge chat data"))

	// 会话（既有行为）与消息（T33 新增）都必须物理删除。
	var remainingSessions int64
	require.NoError(t, env.db.Unscoped().Model(&types.Session{}).
		Where("user_id = ?", "target").Count(&remainingSessions).Error)
	require.Zero(t, remainingSessions, "even soft-deleted sessions must be physically gone")
	for _, id := range []string{"m-1", "m-2", "m-3", "m-4", "m-5"} {
		t33RequirePhysicalGone(t, env, &types.Message{}, id)
	}
	// 其他账号的消息不受影响。
	require.EqualValues(t, 1, umCount(t, env, &types.Message{}, "id = ?", "m-other"))

	// 聊天历史 KB 条目物理删除；普通 KB 文档保留。
	for _, id := range []string{"k-1", "k-2", "k-3", "k-4"} {
		t33RequirePhysicalGone(t, env, &types.Knowledge{}, id)
	}
	require.NoError(t, env.db.Unscoped().Where("id = ?", "doc-keep").First(&types.Knowledge{}).Error,
		"a normal knowledge-base document must survive the purge")

	// 知识库/智能体资产保留，creator_id 继续指向墓碑（AC-3）。
	for _, table := range []string{"knowledge_bases", "custom_agents"} {
		var n int64
		require.NoError(t, env.db.Table(table).Where("creator_id = ?", "target").Count(&n).Error)
		require.EqualValues(t, 1, n, "%s assets must survive the purge", table)
	}

	// 旧审计保留；新增一条 purge 审计。
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")), 1)
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")), 1)
}

// TestT33PurgeRemovesOnlyExclusiveAttachments 覆盖 AC-2：
// 只被目标账号消息引用的附件记录删除；仍被其他账号未删除消息引用的共享
// 附件记录保留（引用判定必须是租户级，不能只看被清理账号自己的会话）。
func TestT33PurgeRemovesOnlyExclusiveAttachments(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-other", "other")

	// 独占附件 att-exclusive；共享附件 att-shared 同时被 other 的消息引用。
	t33SeedTemporaryDocument(t, env, "att-exclusive", "s-1", "local://attachments/exclusive.pdf")
	t33SeedTemporaryDocument(t, env, "att-shared", "s-1", "local://attachments/shared.pdf")

	t33SeedMessage(t, env, "m-1", "s-1", "user", "see attached", "", types.MessageAttachments{
		{ID: "att-exclusive", FileName: "exclusive.pdf", FileType: ".pdf", FileSize: 16},
		{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
	})
	t33SeedMessage(t, env, "m-other", "s-other", "user", "same shared attachment", "",
		types.MessageAttachments{
			{ID: "att-shared", FileName: "shared.pdf", FileType: ".pdf", FileSize: 16},
		})

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "purge attachments"))

	// 目标消息删除后，独占附件不再有引用 → 记录物理删除。
	t33RequirePhysicalGone(t, env, &types.TemporaryDocument{}, "att-exclusive")
	// 共享附件仍被 other 的消息引用 → 记录与对象都必须保留。
	var shared types.TemporaryDocument
	require.NoError(t, env.db.Unscoped().Where("id = ?", "att-shared").First(&shared).Error,
		"an attachment still referenced by another live message must not be deleted")
	require.Equal(t, "local://attachments/shared.pdf", shared.ResourceRef,
		"a retained shared attachment must keep its storage reference")
	// other 的消息仍在，说明共享引用是真实存在的。
	require.EqualValues(t, 1, umCount(t, env, &types.Message{}, "id = ?", "m-other"))
}

// TestT33PurgeWithoutMessagesHasNoCollateralDamage 覆盖 AC-5：
// 无消息账号 purge 只能墓碑化自己，不能波及其他账号的会话/消息/附件/
// 知识条目与任何资产。
func TestT33PurgeWithoutMessagesHasNoCollateralDamage(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	// target 没有任何会话/消息/附件/知识条目。
	t33SeedSession(t, env, "s-other", "other")
	t33SeedMessage(t, env, "m-other", "s-other", "user", "other's chat", "k-other", nil)
	t33SeedChatKnowledge(t, env, "k-other", "kb-chat")
	t33SeedTemporaryDocument(t, env, "att-other", "s-other", "local://attachments/other.pdf")
	t33SeedAssets(t, env, "other")

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "purge empty account"))

	// 目标已墓碑化。
	require.True(t, umLoadUserUnscoped(t, env, "target").DeletedAt.Valid)

	// 其他账号的数据零副作用。
	require.EqualValues(t, 1, umCount(t, env, &types.Session{}, "user_id = ?", "other"))
	require.EqualValues(t, 1, umCount(t, env, &types.Message{}, "id = ?", "m-other"))
	require.EqualValues(t, 1, umCount(t, env, &types.Knowledge{}, "id = ?", "k-other"))
	require.EqualValues(t, 1, umCount(t, env, &types.TemporaryDocument{}, "id = ?", "att-other"))
	for _, table := range []string{"knowledge_bases", "custom_agents"} {
		var n int64
		require.NoError(t, env.db.Table(table).Where("creator_id = ?", "other").Count(&n).Error)
		require.EqualValues(t, 1, n, "%s assets of another account must survive", table)
	}
}

// TestT33PurgeRollbackLeavesNoHalfCleanup 覆盖 AC-5 的失败回滚：
// 在事务末尾（写审计）注入失败，确认消息/聊天历史/附件/会话/账号全部按
// 事务回滚，不出现“清了一半”的账号。
func TestT33PurgeRollbackLeavesNoHalfCleanup(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedMessage(t, env, "m-1", "s-1", "user", "private question", "k-1", types.MessageAttachments{
		{ID: "att-1", FileName: "a.pdf", FileType: ".pdf", FileSize: 16},
	})
	t33SeedChatKnowledge(t, env, "k-1", "kb-chat")
	t33SeedTemporaryDocument(t, env, "att-1", "s-1", "local://attachments/a.pdf")

	// 让审计插入成为事务内最后一步的可注入失败。
	const callbackName = "t33_fail_audit_create"
	require.NoError(t, env.db.Callback().Create().Before("gorm:create").Register(
		callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil {
				return
			}
			if tx.Statement.Table == "audit_logs" ||
				(tx.Statement.Schema != nil && tx.Statement.Schema.Table == "audit_logs") {
				_ = tx.AddError(errors.New("t33 injected audit failure"))
			}
		}))
	t.Cleanup(func() { _ = env.db.Callback().Create().Remove(callbackName) })

	err := env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "rollback check")
	require.ErrorContains(t, err, "t33 injected audit failure",
		"the injected audit failure must surface to the caller")

	// 账号未墓碑化。
	require.True(t, umLoadUser(t, env, "target").IsActive,
		"a failed purge must leave the account active")
	// 会话/消息/聊天历史/附件都还在。
	require.EqualValues(t, 1, umCount(t, env, &types.Session{}, "id = ?", "s-1"))
	require.EqualValues(t, 1, umCount(t, env, &types.Message{}, "id = ?", "m-1"))
	require.EqualValues(t, 1, umCount(t, env, &types.Knowledge{}, "id = ?", "k-1"))
	require.EqualValues(t, 1, umCount(t, env, &types.TemporaryDocument{}, "id = ?", "att-1"))
	// 失败事务不得留下 purge 审计。
	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")))
}

// TestT33PurgeRemovesSessionDerivedData 覆盖 F37：
// 彻底删除后，会话派生的追问缓存（message_suggestion_sets）与消息产物
// （message_artifacts）不得留下无法再通过 API 访问的孤儿行；其他账号的
// 同表数据不受影响。
func TestT33PurgeRemovesSessionDerivedData(t *testing.T) {
	env := newT33PurgeEnv(t)
	t33SeedBase(t, env)

	t33SeedSession(t, env, "s-1", "target")
	t33SeedSession(t, env, "s-other", "other")

	for id, sessionID := range map[string]string{
		"set-1":     "s-1",
		"set-other": "s-other",
	} {
		require.NoError(t, env.db.Create(&types.MessageSuggestionSet{
			ID:                 id,
			TenantID:           umTenantA,
			SessionID:          sessionID,
			AssistantMessageID: "m-1",
			AgentID:            "agent-1",
			Placement:          "after_answer",
			ConfigHash:         "hash",
			Status:             "ready",
			Questions:          types.SuggestionItems{},
		}).Error)
	}
	for id, sessionID := range map[string]string{
		"artifact-1":     "s-1",
		"artifact-other": "s-other",
	} {
		require.NoError(t, env.db.Create(&types.MessageArtifactRecord{
			ID:        id,
			SessionID: sessionID,
			MessageID: "m-1",
			Position:  0,
			URL:       "local://artifacts/" + id,
			FileName:  id + ".md",
			FileType:  ".md",
			FileSize:  8,
			CreatedAt: time.Now().UTC(),
		}).Error)
	}

	require.NoError(t, env.svc.PurgeUser(
		umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "target", "target@example.com", "purge session derived data"))

	t33RequirePhysicalGone(t, env, &types.MessageSuggestionSet{}, "set-1")
	t33RequirePhysicalGone(t, env, &types.MessageArtifactRecord{}, "artifact-1")
	require.EqualValues(t, 1,
		umCount(t, env, &types.MessageSuggestionSet{}, "id = ?", "set-other"),
		"another account's suggestion set must survive")
	require.EqualValues(t, 1,
		umCount(t, env, &types.MessageArtifactRecord{}, "id = ?", "artifact-other"),
		"another account's message artifact must survive")
}
