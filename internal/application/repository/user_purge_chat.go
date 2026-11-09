package repository

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// purgeUserChatDataTx 清理目标账号所有会话里的聊天数据：
//
//  1. 按 sessions.user_id 收集会话 ID（Unscoped，含软删历史）；
//  2. 按 session_id 收集消息的 knowledge_id 与 attachments[].id（含软删消息）；
//  3. 物理删除这些消息；
//  4. 删除没有任何未删除消息再引用的 temporary_documents 记录
//     （引用判定跨会话、跨账号，同租户；共享附件保留）；
//  5. 物理删除消息 knowledge_id 指向的聊天历史 knowledges。
//
// 不删除 knowledge_bases / custom_agents / 普通知识库文档，也不在事务内
// 删除对象存储 blob：temporary_documents 记录删除后，blob 交给既有的
// 过期清理流程回收（见 T33 notes 的 carryover）。
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

	var messages []types.Message
	if err := tx.Unscoped().
		Select("id", "knowledge_id", "attachments").
		Where("session_id IN ?", sessionIDs).
		Find(&messages).Error; err != nil {
		return err
	}
	knowledgeIDs, attachmentIDs := collectPurgeMessageRefs(messages)

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

	if len(attachmentIDs) > 0 && migrator.HasTable("temporary_documents") {
		if err := deleteUnreferencedTemporaryDocumentsTx(tx, attachmentIDs); err != nil {
			return err
		}
	}
	if len(knowledgeIDs) > 0 && migrator.HasTable("knowledges") {
		// tenant 过滤保证只删本空间消息指向的聊天历史条目，不越过租户边界。
		if err := tx.Unscoped().
			Where("id IN ? AND tenant_id IN ?", knowledgeIDs, tenantIDs).
			Delete(&types.Knowledge{}).Error; err != nil {
			return err
		}
	}
	return nil
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

// deleteUnreferencedTemporaryDocumentsTx 只物理删除“独占”的聊天附件记录：
// 只要同租户下还有任一未删除消息（任意会话、任意账号）的 attachments
// JSON 引用同 id，就保留该行及其 resource_ref。
//
// 各数据库的 JSON 成员判定与 temporary_document.expiredQuery 保持一致：
// SQLite 用 json_each + 标量键 NOT IN，MySQL 用 JSON_CONTAINS，
// PostgreSQL 用 jsonb 包含运算符。
func deleteUnreferencedTemporaryDocumentsTx(tx *gorm.DB, ids []string) error {
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
				referenced).
			Delete(&types.TemporaryDocument{}).Error
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
		Where("NOT EXISTS (?)", referenced).
		Delete(&types.TemporaryDocument{}).Error
}
