package repository

// CP3 修复轮 1 复验（FS3 后端半）：List 的 actions 多选走 `action IN (...)`
// 真实查询；空 Actions 保持不过滤；与单值 Action 叠加时两个条件同时生效；
// tenant_id 隔离不变。

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAuditActionsDB(t *testing.T) (*gorm.DB, interfaces.AuditLogRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:audit-log-actions?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.AuditLog{}); err != nil {
		t.Fatalf("migrate audit log: %v", err)
	}
	rows := []*types.AuditLog{
		{TenantID: 7, Action: types.AuditActionKBUpdated, ScopeType: "knowledge_base", ScopeID: "kb-a"},
		{TenantID: 7, Action: types.AuditActionKnowledgeCreated, ScopeType: "knowledge_base", ScopeID: "kb-b"},
		{TenantID: 7, Action: types.AuditActionMemberAdded},
		{TenantID: 8, Action: types.AuditActionKnowledgeCreated, ScopeType: "knowledge_base", ScopeID: "kb-c"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("insert audit row: %v", err)
		}
	}
	return db, NewAuditLogRepository(db)
}

func actionsOf(rows []*types.AuditLog) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, string(row.Action))
	}
	return out
}

func TestCP3R1_AuditLogRepositoryActionsInFilter(t *testing.T) {
	_, repo := newAuditActionsDB(t)

	got, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Actions: []types.AuditAction{types.AuditActionKBUpdated, types.AuditActionKnowledgeCreated},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows=%v, want 2（IN 命中 kb.updated+knowledge.created）", actionsOf(got))
	}
	for _, row := range got {
		if row.TenantID != 7 {
			t.Fatalf("跨租户行泄漏: %+v", row)
		}
	}
}

func TestCP3R1_AuditLogRepositoryActionsEmptyMeansNoFilter(t *testing.T) {
	_, repo := newAuditActionsDB(t)

	got, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows=%v, want 3（空 Actions 不过滤，tenant 7 全部）", actionsOf(got))
	}
}

func TestCP3R1_AuditLogRepositoryActionAndActionsStack(t *testing.T) {
	_, repo := newAuditActionsDB(t)

	// 单值 action=knowledge.created 与 actions 含 kb.updated 叠加 = 仅 knowledge.created。
	got, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Action:  types.AuditActionKnowledgeCreated,
		Actions: []types.AuditAction{types.AuditActionKnowledgeCreated, types.AuditActionKBUpdated},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Action != types.AuditActionKnowledgeCreated {
		t.Fatalf("rows=%v, want 仅 knowledge.created（= 与 IN 同时生效）", actionsOf(got))
	}
}
