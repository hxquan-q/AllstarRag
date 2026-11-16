package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// R6b F46 TDD 红阶段：SessionRepository 新增
//
//	CreateIdempotent(ctx, session) (*types.Session, error)
//
// 契约：事务内先 lockLiveWebUser（墓碑 web 用户返回错误且不落行），
// 再以 SkipHooks + OnConflict DoNothing 插入——必须保留调用方指定的
// 会话 ID（automation 的 persistent session 与委托成员都依赖该 ID），
// 重复调用同一 ID 不得报错、不得写第二行。
// 当前仓储没有该方法，预期红。

// r6bCreateIdempotentRepo 是 F46 的窄接口契约；用运行时断言而非包级
// 断言，保证既有 T40/T41/session 用例仍可编译运行（红点只落在本用例）。
type r6bCreateIdempotentRepo interface {
	CreateIdempotent(ctx context.Context, session *types.Session) (*types.Session, error)
}

func r6bNewCreateIdempotentDB(t *testing.T) (*gorm.DB, interfaces.SessionRepository) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(&types.Session{}))
	migrateUsers(t, db)
	return db, NewSessionRepository(db)
}

func r6bIdempotentRepo(t *testing.T, repo interfaces.SessionRepository) r6bCreateIdempotentRepo {
	t.Helper()
	typed, ok := repo.(r6bCreateIdempotentRepo)
	if !ok {
		t.Fatalf("F46: SessionRepository 必须实现 CreateIdempotent(ctx, session) (*types.Session, error)")
	}
	return typed
}

// TestR6BCreateIdempotentNormalUserSucceeds：存活 web 用户正常建会话，
// 且调用方指定的 ID 必须原样保留（不能被 BeforeCreate 钩子改成随机 UUID）。
func TestR6BCreateIdempotentNormalUserSucceeds(t *testing.T) {
	db, base := r6bNewCreateIdempotentDB(t)
	repo := r6bIdempotentRepo(t, base)
	ctx := context.Background()
	user := "11111111-1111-4111-8111-111111111111"
	t40SeedUser(t, db, user, true)

	created, err := repo.CreateIdempotent(ctx, &types.Session{
		ID: "r6b-session-1", TenantID: 46, UserID: user, Title: "normal",
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, "r6b-session-1", created.ID,
		"F46: 必须保留调用方指定的会话 ID（BeforeCreate 不得改写成随机 UUID）")

	var count int64
	require.NoError(t, db.Model(&types.Session{}).
		Where("id = ?", "r6b-session-1").Count(&count).Error)
	require.EqualValues(t, 1, count, "正常用户应落一行会话")
}

// TestR6BCreateIdempotentRejectsTombstonedUser：墓碑用户必须被
// lockLiveWebUser 挡住，返回错误且零会话行。
func TestR6BCreateIdempotentRejectsTombstonedUser(t *testing.T) {
	db, base := r6bNewCreateIdempotentDB(t)
	repo := r6bIdempotentRepo(t, base)
	ctx := context.Background()
	user := "22222222-2222-4222-8222-222222222222"
	t40SeedUser(t, db, user, true)
	require.NoError(t, db.Delete(&types.User{}, "id = ?", user).Error)

	_, err := repo.CreateIdempotent(ctx, &types.Session{
		ID: "r6b-tomb-1", TenantID: 46, UserID: user, Title: "tombstoned",
	})
	require.Error(t, err, "F46: 墓碑用户不得再创建会话")

	var count int64
	require.NoError(t, db.Model(&types.Session{}).
		Where("user_id = ?", user).Count(&count).Error)
	require.Zero(t, count, "F46: 墓碑用户被拒后不得落任何会话行")
}

// TestR6BCreateIdempotentRepeatIsNoOp：同一 ID 重复创建必须幂等
// （OnConflict DoNothing）；第二次不得报错，库中只保留首条会话。
func TestR6BCreateIdempotentRepeatIsNoOp(t *testing.T) {
	db, base := r6bNewCreateIdempotentDB(t)
	repo := r6bIdempotentRepo(t, base)
	ctx := context.Background()
	user := "33333333-3333-4333-8333-333333333333"
	t40SeedUser(t, db, user, true)

	_, err := repo.CreateIdempotent(ctx, &types.Session{
		ID: "r6b-repeat-1", TenantID: 46, UserID: user, Title: "first",
	})
	require.NoError(t, err)
	_, err = repo.CreateIdempotent(ctx, &types.Session{
		ID: "r6b-repeat-1", TenantID: 46, UserID: user, Title: "second",
	})
	require.NoError(t, err, "F46: 重复 ID + OnConflict DoNothing 不得报错")

	var rows []types.Session
	require.NoError(t, db.Where("id = ?", "r6b-repeat-1").Find(&rows).Error)
	require.Len(t, rows, 1, "F46: 重复创建不得写第二行会话")
	require.Equal(t, "first", rows[0].Title,
		"F46: OnConflict DoNothing 必须保留首条会话内容")
}
