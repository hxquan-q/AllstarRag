package repository

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// purge:cleanup 与 wiki 共用 task_pending_ops 表，必须用独立的三元组隔离，
// 避免被 wiki 消费者当成自己的任务取走。
const (
	purgeCleanupTaskType = "purge:cleanup"
	purgeCleanupScope    = "purge"
	purgeCleanupScopeID  = "global"

	purgeCleanupOpKnowledge  = "knowledge"
	purgeCleanupOpAttachment = "attachment"
)

// purgeCleanupKnowledgePayload 描述一批需要异步清理索引的聊天历史知识。
type purgeCleanupKnowledgePayload struct {
	TenantID        uint64   `json:"tenant_id"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	KnowledgeIDs    []string `json:"knowledge_ids"`
}

// purgeCleanupAttachmentDocument 是一个需要释放对象存储引用的独占附件。
// ImageRefs 是该附件解析产物的图片存储引用（去空、保序去重），worker 必须
// 逐个释放，语义与正常 Delete 一致。
type purgeCleanupAttachmentDocument struct {
	ID          string   `json:"id"`
	ResourceRef string   `json:"resource_ref"`
	ImageRefs   []string `json:"image_refs"`
}

// purgeCleanupAttachmentPayload 描述一个租户下需要异步释放的独占附件。
type purgeCleanupAttachmentPayload struct {
	TenantID  uint64                           `json:"tenant_id"`
	Documents []purgeCleanupAttachmentDocument `json:"documents"`
}

// purgeUserChatDataTx 清理目标账号所有会话里的聊天数据：
//
//  1. 按 sessions.user_id 收集会话 ID（Unscoped，含软删历史）；
//  2. 按 session_id 收集消息的 knowledge_id 与 attachments[].id（含软删消息）；
//  3. 物理删除这些消息；
//  4. 删除没有任何未删除消息再引用的 temporary_documents 记录
//     （引用判定跨会话、跨账号，同租户；共享附件保留）；
//  5. 物理删除消息 knowledge_id 指向的聊天历史 knowledges。
//
// 索引（chunks/向量/图谱）与独占附件的对象存储 blob 不在事务内直接删除：
// 第 3–5 步收集清理载荷，并在同一事务里写入 task_pending_ops 的
// purge:cleanup 任务，由后台 worker 异步执行、失败重试（T39）。事务回滚时
// 任务随事务一起消失，不会留下指向仍存活数据的清理任务。
//
// 不删除 knowledge_bases / custom_agents / 普通知识库文档。
func purgeUserChatDataTx(tx *gorm.DB, userID string) error {
	migrator := tx.Migrator()
	// 轻量库/部分测试夹具可能没有消息表，与沙箱表缺失时一样跳过，
	// 避免事务因缺表整体失败。生产库不存在这种情况。
	if !migrator.HasTable("messages") {
		return nil
	}

	var sessions []types.Session
	if err := tx.Unscoped().
		Select("id", "tenant_id").
		Where("user_id = ?", userID).
		Find(&sessions).Error; err != nil {
		return err
	}
	if len(sessions) == 0 {
		return nil
	}

	sessionIDs := make([]string, 0, len(sessions))
	tenantIDs := make([]uint64, 0, len(sessions))
	seenTenants := make(map[uint64]struct{}, len(sessions))
	for i := range sessions {
		sessionIDs = append(sessionIDs, sessions[i].ID)
		if _, ok := seenTenants[sessions[i].TenantID]; !ok {
			seenTenants[sessions[i].TenantID] = struct{}{}
			tenantIDs = append(tenantIDs, sessions[i].TenantID)
		}
	}

	// 与消息写入共用 sessions 行锁：必须在收集/删除 messages 之前先锁住目标
	// 会话，消息写入才会被串行化。purge 先拿到锁时旧请求复核失败；消息先
	// 提交时这里的删除会覆盖它。锁序保持 users → sessions → messages。
	if err := lockPurgeSessionsTx(tx, userID); err != nil {
		return err
	}

	var messages []types.Message
	if err := tx.Unscoped().
		Select("id", "knowledge_id", "attachments").
		Where("session_id IN ?", sessionIDs).
		Find(&messages).Error; err != nil {
		return err
	}
	knowledgeIDs, attachmentIDs := collectPurgeMessageRefs(messages)
	if migrator.HasTable("temporary_documents") {
		// 上传后未发送的附件只有 session 级记录，不会出现在消息引用里；
		// 它们同样属于该账号的会话临时数据，必须一起参与“独占”判定。
		// Unscoped：已软删的行也要回收其对象存储资源。
		sessionDocumentIDs, err := listSessionTemporaryDocumentIDsTx(tx, sessionIDs)
		if err != nil {
			return err
		}
		attachmentIDs = appendUniqueIDs(attachmentIDs, sessionDocumentIDs...)
	}

	// 先物理删除消息（含软删历史），附件引用判定看到的就是删除后的状态。
	if err := tx.Unscoped().
		Where("session_id IN ?", sessionIDs).
		Delete(&types.Message{}).Error; err != nil {
		return err
	}

	// 会话派生的追问缓存与消息产物同样按 session 清理，避免“彻底删除”
	// 后留下无法再通过 API 访问、但仍存在的孤儿行（T33 残留）。
	if migrator.HasTable("message_suggestion_sets") {
		if err := tx.Where("session_id IN ?", sessionIDs).
			Delete(&types.MessageSuggestionSet{}).Error; err != nil {
			return err
		}
	}
	if migrator.HasTable("message_artifacts") {
		if err := tx.Where("session_id IN ?", sessionIDs).
			Delete(&types.MessageArtifactRecord{}).Error; err != nil {
			return err
		}
	}

	var exclusiveAttachments []types.TemporaryDocument
	if len(attachmentIDs) > 0 && migrator.HasTable("temporary_documents") {
		documents, err := purgeUnreferencedTemporaryDocumentsTx(tx, attachmentIDs)
		if err != nil {
			return err
		}
		exclusiveAttachments = documents
	}
	if len(knowledgeIDs) > 0 && migrator.HasTable("knowledges") {
		// 先按知识行解析出 (租户, 聊天 KB) 再删行，事务外 worker 才拿得到
		// 清理向量/图谱所需的作用域。
		if err := enqueuePurgeKnowledgeCleanupTx(tx, userID, tenantIDs, knowledgeIDs); err != nil {
			return err
		}
		// tenant 过滤保证只删本空间消息指向的聊天历史条目，不越过租户边界。
		if err := tx.Unscoped().
			Where("id IN ? AND tenant_id IN ?", knowledgeIDs, tenantIDs).
			Delete(&types.Knowledge{}).Error; err != nil {
			return err
		}
	}
	if err := enqueuePurgeAttachmentCleanupTx(tx, userID, exclusiveAttachments); err != nil {
		return err
	}
	return nil
}

// lockPurgeSessionsTx 锁定目标账号的全部未删除会话行：PostgreSQL/MySQL 用
// SELECT ... FOR UPDATE，SQLite 用空更新取写锁（与 lockPurgeTarget 一致）。
// 调用点必须在删除 messages 之前，保证消息写入与注销清理使用同一把
// session 锁、不会在删除消息与删除会话之间插入孤儿消息。
func lockPurgeSessionsTx(tx *gorm.DB, userID string) error {
	if tx.Name() == "sqlite" {
		return tx.Model(&types.Session{}).
			Where("user_id = ?", userID).
			UpdateColumn("updated_at", gorm.Expr("updated_at")).Error
	}
	var sessions []types.Session
	return forUpdate(tx).Model(&types.Session{}).Select("id").
		Where("user_id = ?", userID).Find(&sessions).Error
}

// enqueuePurgeKnowledgeCleanupTx 把聊天历史 knowledge 按 (租户, KB) 分组入队。
// 一次注销可能跨多个空间，因此可能写多行；同一 dedup_key 让 ClaimBatch 把
// 它们按同一次注销成组领取。知识行缺失（已被清理）时无法确定 KB 作用域，
// 跳过该条：其索引由对应 KB 的删除路径负责。
func enqueuePurgeKnowledgeCleanupTx(
	tx *gorm.DB, userID string, tenantIDs []uint64, knowledgeIDs []string,
) error {
	type knowledgeRow struct {
		ID              string `gorm:"column:id"`
		TenantID        uint64 `gorm:"column:tenant_id"`
		KnowledgeBaseID string `gorm:"column:knowledge_base_id"`
	}
	var rows []knowledgeRow
	if err := tx.Unscoped().Model(&types.Knowledge{}).
		Select("id", "tenant_id", "knowledge_base_id").
		Where("id IN ? AND tenant_id IN ?", knowledgeIDs, tenantIDs).
		Scan(&rows).Error; err != nil {
		return err
	}
	type groupKey struct {
		TenantID        uint64
		KnowledgeBaseID string
	}
	grouped := make(map[groupKey][]string)
	for _, row := range rows {
		key := groupKey{TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID}
		grouped[key] = append(grouped[key], row.ID)
	}
	for key, ids := range grouped {
		payload, err := json.Marshal(purgeCleanupKnowledgePayload{
			TenantID:        key.TenantID,
			KnowledgeBaseID: key.KnowledgeBaseID,
			KnowledgeIDs:    ids,
		})
		if err != nil {
			return err
		}
		if err := createPurgeCleanupTaskTx(
			tx, key.TenantID, userID, purgeCleanupOpKnowledge, payload,
		); err != nil {
			return err
		}
	}
	return nil
}

// enqueuePurgeAttachmentCleanupTx 把独占附件按租户分组入队。resource_ref
// 为空的记录没有对象存储资源可释放，只删行不入队。
func enqueuePurgeAttachmentCleanupTx(
	tx *gorm.DB, userID string, documents []types.TemporaryDocument,
) error {
	grouped := make(map[uint64][]purgeCleanupAttachmentDocument)
	for _, document := range documents {
		ref := strings.TrimSpace(document.ResourceRef)
		if ref == "" {
			continue
		}
		imageRefs, err := purgeCleanupImageRefs(document.ImageRefs)
		if err != nil {
			return fmt.Errorf("decode image refs of temporary document %s: %w", document.ID, err)
		}
		grouped[document.TenantID] = append(
			grouped[document.TenantID],
			purgeCleanupAttachmentDocument{
				ID:          document.ID,
				ResourceRef: ref,
				ImageRefs:   imageRefs,
			},
		)
	}
	for tenantID, docs := range grouped {
		payload, err := json.Marshal(purgeCleanupAttachmentPayload{
			TenantID: tenantID, Documents: docs,
		})
		if err != nil {
			return err
		}
		if err := createPurgeCleanupTaskTx(
			tx, tenantID, userID, purgeCleanupOpAttachment, payload,
		); err != nil {
			return err
		}
	}
	return nil
}

// purgeCleanupImageRefs 解析 temporary_documents.image_refs 里的图片 URL，
// 跳过空白项并保序去重；解析失败让注销事务回滚（fail-closed），避免静默
// 丢掉图片引用清理。
func purgeCleanupImageRefs(raw types.JSON) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var images []types.TemporaryDocumentImage
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(images))
	refs := make([]string, 0, len(images))
	for _, image := range images {
		url := strings.TrimSpace(image.URL)
		if url == "" {
			continue
		}
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		refs = append(refs, url)
	}
	return refs, nil
}

// createPurgeCleanupTaskTx 在注销事务内写一条 purge:cleanup 任务。
// 缺表/写入失败都让整个注销事务回滚（fail-closed）：不能让账号墓碑化成功
// 却把索引和 blob 永久留在存储里。
func createPurgeCleanupTaskTx(
	tx *gorm.DB, tenantID uint64, userID, op string, payload []byte,
) error {
	return tx.Create(&types.TaskPendingOp{
		TenantID:   tenantID,
		TaskType:   purgeCleanupTaskType,
		Scope:      purgeCleanupScope,
		ScopeID:    purgeCleanupScopeID,
		Op:         op,
		DedupKey:   "purge:" + userID + ":" + op,
		Payload:    payload,
		EnqueuedAt: time.Now().UTC(),
	}).Error
}

// collectPurgeMessageRefs 汇总消息引用的聊天历史知识 ID 与附件 ID，
// 空值忽略并按出现顺序去重。
func collectPurgeMessageRefs(messages []types.Message) (knowledgeIDs, attachmentIDs []string) {
	seenKnowledge := make(map[string]struct{})
	seenAttachment := make(map[string]struct{})
	for i := range messages {
		if id := strings.TrimSpace(messages[i].KnowledgeID); id != "" {
			if _, ok := seenKnowledge[id]; !ok {
				seenKnowledge[id] = struct{}{}
				knowledgeIDs = append(knowledgeIDs, id)
			}
		}
		for _, attachment := range messages[i].Attachments {
			id := strings.TrimSpace(attachment.ID)
			if id == "" {
				continue
			}
			if _, ok := seenAttachment[id]; !ok {
				seenAttachment[id] = struct{}{}
				attachmentIDs = append(attachmentIDs, id)
			}
		}
	}
	return knowledgeIDs, attachmentIDs
}

// listSessionTemporaryDocumentIDsTx 收集目标会话下所有临时附件 ID（含软删
// 行），覆盖“上传后从未发送”的文档。是否存在共享引用仍由后续独占判定处理。
func listSessionTemporaryDocumentIDsTx(tx *gorm.DB, sessionIDs []string) ([]string, error) {
	var ids []string
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	if err := tx.Unscoped().Model(&types.TemporaryDocument{}).
		Where("session_id IN ?", sessionIDs).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// appendUniqueIDs 把 extra 中尚未出现的 ID 追加到 ids，保持既有顺序。
func appendUniqueIDs(ids []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(ids)+len(extra))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, id := range extra {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// purgeUnreferencedTemporaryDocumentsTx 只物理删除“独占”的聊天附件记录，
// 并返回被删记录（含 resource_ref）供异步 worker 释放对象存储资源：
// 只要同租户下还有任一未删除消息（任意会话、任意账号）的 attachments
// JSON 引用同 id，就保留该行及其 resource_ref（共享附件不入队）。
//
// 各数据库的 JSON 成员判定与 temporary_document.expiredQuery 保持一致：
// SQLite 用 json_each + 标量键 NOT IN，MySQL 用 JSON_CONTAINS，
// PostgreSQL 用 jsonb 包含运算符。
func purgeUnreferencedTemporaryDocumentsTx(
	tx *gorm.DB, ids []string,
) ([]types.TemporaryDocument, error) {
	var documents []types.TemporaryDocument
	if err := unreferencedTemporaryDocumentsTx(tx, ids).
		Select("id", "tenant_id", "resource_ref", "image_refs").
		Find(&documents).Error; err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, nil
	}
	deleteIDs := make([]string, 0, len(documents))
	for _, document := range documents {
		deleteIDs = append(deleteIDs, document.ID)
	}
	if err := tx.Unscoped().
		Where("id IN ?", deleteIDs).
		Delete(&types.TemporaryDocument{}).Error; err != nil {
		return nil, err
	}
	return documents, nil
}

// unreferencedTemporaryDocumentsTx 构造“没有任何未删除消息再引用”的
// temporary_documents 查询。选择与删除共用同一判定，避免两边漂移。
func unreferencedTemporaryDocumentsTx(tx *gorm.DB, ids []string) *gorm.DB {
	query := tx.Unscoped().Model(&types.TemporaryDocument{}).Where("id IN ?", ids)
	if tx.Name() == "sqlite" {
		referenced := tx.Table("messages m").
			Select("CAST(s.tenant_id AS TEXT) || ':' || json_extract(a.value, '$.id')").
			Joins("JOIN sessions s ON s.id = m.session_id").
			Joins("JOIN json_each(CAST(m.attachments AS TEXT)) a").
			Where("m.deleted_at IS NULL").
			Where("json_extract(a.value, '$.id') IS NOT NULL")
		return query.
			Where("(CAST(temporary_documents.tenant_id AS TEXT) || ':' || temporary_documents.id) NOT IN (?)",
				referenced)
	}

	// PostgreSQL 走 attachments 的 GIN 索引；MySQL 走 JSON_CONTAINS。
	containsAttachment := "m.attachments @> jsonb_build_array(jsonb_build_object('id', temporary_documents.id))"
	if tx.Name() == "mysql" {
		containsAttachment = "JSON_CONTAINS(m.attachments, JSON_OBJECT('id', temporary_documents.id))"
	}
	referenced := tx.Table("messages m").Select("1").
		Joins("JOIN sessions s ON s.id = m.session_id").
		Where("s.tenant_id = temporary_documents.tenant_id").
		Where("m.deleted_at IS NULL").
		Where(containsAttachment)
	return query.
		Where("NOT EXISTS (?)", referenced)
}
