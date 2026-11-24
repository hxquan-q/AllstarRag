package repository

// T05 TDD 红阶段：同步运行记录仓储的 Latest 契约。
//
// 真实仓储必须让 Latest 排除 trigger=preview 的记录，避免预检覆盖
// 管理端看到的“最近一次真实同步”状态。这里用 SQLite 建真实表验证，
// 不用替身。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newYZJT05SyncRunRepo(t *testing.T) interfaces.YunzhijiaSyncRunRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "yzj-sync-runs.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.YunzhijiaSyncRun{}); err != nil {
		t.Fatalf("迁移 sync_runs: %v", err)
	}
	return NewYunzhijiaSyncRunRepository(db)
}

func yzjT05FinishRun(
	t *testing.T,
	repo interfaces.YunzhijiaSyncRunRepository,
	trigger types.YunzhijiaSyncTrigger,
	startedAt time.Time,
	status types.YunzhijiaSyncStatus,
) {
	t.Helper()
	run := &types.YunzhijiaSyncRun{Trigger: trigger, StartedAt: startedAt}
	if err := repo.CreateRunning(context.Background(), run); err != nil {
		t.Fatalf("创建 running 记录(%s): %v", trigger, err)
	}
	run.Status = status
	if err := repo.Finish(context.Background(), run); err != nil {
		t.Fatalf("结束记录(%s): %v", trigger, err)
	}
}

func TestYunzhijiaT05SyncRunLatestExcludesPreview(t *testing.T) {
	ctx := context.Background()
	repo := newYZJT05SyncRunRepo(t)

	manualAt := time.Now().Add(-time.Hour)
	yzjT05FinishRun(t, repo, types.YunzhijiaSyncTriggerManual, manualAt,
		types.YunzhijiaSyncStatusSuccess)
	// 预检的触发器常量由 T05 新增；此处用存储值字面量，避免与 service 包一起编译红。
	yzjT05FinishRun(t, repo, types.YunzhijiaSyncTrigger("preview"), time.Now(),
		types.YunzhijiaSyncStatusSuccess)

	latest, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("Latest 返回 nil：已有真实同步记录")
	}
	if latest.Trigger != types.YunzhijiaSyncTriggerManual {
		t.Fatalf("Latest 返回 trigger=%q，预检不得覆盖真实同步状态", latest.Trigger)
	}
}

func TestYunzhijiaT05SyncRunLatestNilWhenOnlyPreview(t *testing.T) {
	ctx := context.Background()
	repo := newYZJT05SyncRunRepo(t)

	yzjT05FinishRun(t, repo, types.YunzhijiaSyncTrigger("preview"), time.Now(),
		types.YunzhijiaSyncStatusSuccess)

	latest, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest != nil {
		t.Fatalf("只有预检记录时 Latest 应返回 nil，实际 trigger=%q", latest.Trigger)
	}
}
