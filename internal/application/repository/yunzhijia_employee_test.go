package repository

// T06 票级复跑 round2：员工目录 blocked/unblock 的真实 SQLite 仓储语义。
//
// 审查修复 low 项把 MarkBlocked 从「blocked + 清 user_id」改为「只置
// blocked，保留 user_id 供审计；Unblock 时再清」。这里用真实表验证，
// 不用替身。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newYZJT06EmployeeRepo(t *testing.T) interfaces.YunzhijiaEmployeeRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "yzj-employees.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.YunzhijiaEmployee{}); err != nil {
		t.Fatalf("迁移 employees: %v", err)
	}
	return NewYunzhijiaEmployeeRepository(db)
}

func TestYunzhijiaT06MarkBlockedKeepsUserIDUnblockClearsIt(t *testing.T) {
	ctx := context.Background()
	repo := newYZJT06EmployeeRepo(t)

	userID := "u-t06-audit"
	employee := &types.YunzhijiaEmployee{
		OpenID:     "open-audit-0001",
		Name:       "占位员工申",
		Department: "占位部门",
		TenantID:   10024,
		UserID:     &userID,
		Status:     types.YunzhijiaEmployeeStatusInactive,
	}
	if err := repo.Create(ctx, employee); err != nil {
		t.Fatalf("创建员工: %v", err)
	}

	if err := repo.MarkBlocked(ctx, employee.OpenID, "actor-t06"); err != nil {
		t.Fatalf("MarkBlocked: %v", err)
	}
	blocked, err := repo.GetByOpenID(ctx, employee.OpenID)
	if err != nil {
		t.Fatalf("GetByOpenID(blocked): %v", err)
	}
	if blocked == nil || !blocked.Blocked {
		t.Fatalf("MarkBlocked 后应为 blocked: %+v", blocked)
	}
	if blocked.UserID == nil || *blocked.UserID != userID {
		t.Fatalf("MarkBlocked 必须保留 user_id 供审计，实际 %v", blocked.UserID)
	}
	if blocked.BlockedBy == nil || *blocked.BlockedBy != "actor-t06" {
		t.Fatalf("MarkBlocked 必须记录操作人，实际 %v", blocked.BlockedBy)
	}
	if blocked.BlockedAt == nil {
		t.Fatal("MarkBlocked 必须写入 blocked_at")
	}

	if err := repo.Unblock(ctx, employee.OpenID); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	unblocked, err := repo.GetByOpenID(ctx, employee.OpenID)
	if err != nil {
		t.Fatalf("GetByOpenID(unblocked): %v", err)
	}
	if unblocked == nil || unblocked.Blocked {
		t.Fatalf("Unblock 后应解除 blocked: %+v", unblocked)
	}
	if unblocked.UserID != nil {
		t.Fatalf("Unblock 必须清空 user_id，实际 %v", *unblocked.UserID)
	}
	if unblocked.BlockedBy != nil || unblocked.BlockedAt != nil {
		t.Fatalf("Unblock 必须清空 blocked_by/blocked_at，实际 by=%v at=%v",
			unblocked.BlockedBy, unblocked.BlockedAt)
	}

	if err := repo.Unblock(ctx, employee.OpenID); !errors.Is(err, ErrYunzhijiaEmployeeNotFound) {
		t.Fatalf("重复 Unblock 应返回 ErrYunzhijiaEmployeeNotFound，实际 %v", err)
	}
}
