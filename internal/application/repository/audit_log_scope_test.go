package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuditLogRepositoryListFiltersKnowledgeBaseScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:audit-log-scope?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.AuditLog{}); err != nil {
		t.Fatalf("migrate audit log: %v", err)
	}
	rows := []*types.AuditLog{
		{TenantID: 7, Action: types.AuditActionMemberAdded},
		{TenantID: 7, Action: types.AuditActionKBUpdated, ScopeType: "knowledge_base", ScopeID: "kb-a"},
		{TenantID: 7, Action: types.AuditActionKnowledgeCreated, ScopeType: "knowledge_base", ScopeID: "kb-b"},
		{TenantID: 8, Action: types.AuditActionKBUpdated, ScopeType: "knowledge_base", ScopeID: "kb-a"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("insert audit log: %v", err)
		}
	}

	repo := NewAuditLogRepository(db)
	got, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		ScopeType: "knowledge_base",
		ScopeID:   "kb-a",
	})
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != 7 || got[0].ScopeID != "kb-a" {
		t.Fatalf("scope filter returned unexpected rows: %+v", got)
	}
	unscoped, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{UnscopedOnly: true})
	if err != nil {
		t.Fatalf("list unscoped audit logs: %v", err)
	}
	if len(unscoped) != 1 || unscoped[0].Action != types.AuditActionMemberAdded {
		t.Fatalf("unscoped filter returned unexpected rows: %+v", unscoped)
	}
}

// T02 票级验证：include_scoped=true 的仓储半——UnscopedOnly=false（且不设
// ScopeType/ScopeID，即 ListTenantAuditLog 放开后的查询形状）时，
// scope_type=knowledge_base 的行随成员治理行一并返回；tenant_id 过滤仍
// 强制（跨空间行不出现）。反向对照 UnscopedOnly=true 只剩成员治理行（旧口径）。
func TestAuditLogRepositoryListIncludesKnowledgeBaseRowsWhenNotUnscopedOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:audit-log-include-scoped?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.AuditLog{}); err != nil {
		t.Fatalf("migrate audit log: %v", err)
	}
	rows := []*types.AuditLog{
		{TenantID: 7, Action: types.AuditActionMemberAdded},
		{TenantID: 7, Action: types.AuditActionKnowledgeCreated, ScopeType: "knowledge_base", ScopeID: "kb-b"},
		{TenantID: 7, Action: types.AuditActionKBUpdated, ScopeType: "knowledge_base", ScopeID: "kb-a"},
		{TenantID: 8, Action: types.AuditActionKnowledgeCreated, ScopeType: "knowledge_base", ScopeID: "kb-c"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("insert audit log: %v", err)
		}
	}

	repo := NewAuditLogRepository(db)
	got, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{})
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows (1 member + 2 knowledge_base) for tenant 7, got %d: %+v", len(got), got)
	}
	hasKB := false
	for _, row := range got {
		if row.TenantID != 7 {
			t.Fatalf("cross-tenant row leaked into tenant feed: %+v", row)
		}
		if row.ScopeType == "knowledge_base" {
			hasKB = true
		}
	}
	if !hasKB {
		t.Fatalf("knowledge_base rows must be included when UnscopedOnly=false: %+v", got)
	}

	unscoped, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{UnscopedOnly: true})
	if err != nil {
		t.Fatalf("list unscoped audit logs: %v", err)
	}
	if len(unscoped) != 1 || unscoped[0].Action != types.AuditActionMemberAdded {
		t.Fatalf("unscoped filter returned unexpected rows: %+v", unscoped)
	}
}
