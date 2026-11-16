package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// T41 TDD 红阶段（repository 层）：注销与消息写入并发围栏。
//
// 钉死的契约（03-tickets/T41.md + 队长派单）：
//   - MessageRepository 新增
//     CreateMessageInSessionTx(ctx, message, tenantID, ownerID)
//       (*types.Message, error)；
//   - 单事务内先锁目标 sessions 行（PG/MySQL FOR UPDATE，SQLite 用空更新
//     或等价写锁），再复核：session 在 tenantID/ownerID 作用域内仍存在；
//     会话 owner（规范 UUID 的 web 账号）未被墓碑化（deleted_at）或封禁
//     （is_active=false）；复核通过后插入 message 与 artifacts；
//   - 复核失败返回 session 不存在的同义错误（apperrors.ErrSessionNotFound）
//     且零插入；
//   - 非规范 UUID 的 owner（API/IM/embed 会话）跳过 users 状态复核，保持
//     既有会话可见性语义（user_id 为空或匹配时才可见）。
//
// 红阶段做法：新方法尚不存在时，t41RequireCreateTxRepo 用运行时类型断言
// 给出明确的契约缺失失败，而不是让整包编译失败——这样服务层的孤儿消息
// 运行时红证据也能在同一轮采集到。

const t41RepoTestTenant uint64 = 10

// t41CreateMessageTxRepo 是 T41 新仓储契约的最小形状。
type t41CreateMessageTxRepo interface {
	CreateMessageInSessionTx(
		ctx context.Context, message *types.Message, tenantID uint64, ownerID string,
	) (*types.Message, error)
}

func t41RequireCreateTxRepo(
	t *testing.T, repo interfaces.MessageRepository,
) t41CreateMessageTxRepo {
	t.Helper()
	txRepo, ok := repo.(t41CreateMessageTxRepo)
	require.True(t, ok,
		"MessageRepository 必须实现 CreateMessageInSessionTx(ctx, message, tenantID, ownerID)；"+
			"消息创建需与 session 存在性检查同事务并锁定/复核目标会话")
	return txRepo
}

func newT41RepoEnv(t *testing.T) (*gorm.DB, interfaces.MessageRepository) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&types.Session{}, &types.Message{}, &types.MessageArtifactRecord{},
	))
	migrateUsers(t, db)
	return db, NewMessageRepository(db)
}

func t41SeedRepoUser(t *testing.T, db *gorm.DB, id string, active bool) {
	t.Helper()
	require.NoError(t, db.Create(&types.User{
		ID:           id,
		Username:     "user-" + id,
		Email:        id + "@example.com",
		PasswordHash: "hash",
		IsActive:     true,
	}).Error)
	if !active {
		// users.is_active 带 default:true，必须创建后显式关闭。
		require.NoError(t, db.Model(&types.User{}).
			Where("id = ?", id).Update("is_active", false).Error)
	}
}

func t41SeedRepoTombstonedUser(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	t41SeedRepoUser(t, db, id, true)
	require.NoError(t, db.Unscoped().Model(&types.User{}).
		Where("id = ?", id).Update("deleted_at", time.Now()).Error)
}

func t41SeedRepoSession(t *testing.T, db *gorm.DB, id, owner string, tenantID uint64) {
	t.Helper()
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{
		ID: id, TenantID: tenantID, UserID: owner, Title: id,
	}).Error)
}

func t41RepoMessageCount(t *testing.T, db *gorm.DB, sessionID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Unscoped().Model(&types.Message{}).
		Where("session_id = ?", sessionID).Count(&n).Error)
	return n
}

// TestT41CreateMessageInSessionTxInsertsMessageAndArtifacts：
// 存活 session + 存活 owner 下，message 与 artifacts 在同一次调用落库。
func TestT41CreateMessageInSessionTxInsertsMessageAndArtifacts(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoUser(t, db, owner, true)
	t41SeedRepoSession(t, db, "s-live", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	created, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-live",
		Role:      "assistant",
		Content:   "answer",
		Artifacts: types.MessageArtifacts{{
			URL:      "local://artifacts/a.md",
			FileName: "a.md",
			FileType: ".md",
			FileSize: 4,
		}},
	}, t41RepoTestTenant, owner)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.NotEmpty(t, created.ID)

	var row types.Message
	require.NoError(t, db.Where("id = ?", created.ID).Take(&row).Error)
	require.Equal(t, "s-live", row.SessionID)
	require.Equal(t, "answer", row.Content)

	var artifacts int64
	require.NoError(t, db.Model(&types.MessageArtifactRecord{}).
		Where("message_id = ?", created.ID).Count(&artifacts).Error)
	require.EqualValues(t, 1, artifacts, "artifacts 必须与消息同事务写入")
}

// TestT41CreateMessageInSessionTxRejectsMissingSession：
// 事务内复核发现 session 已不存在（purge 已删）时返回 session 不存在错误且
// 零插入。
func TestT41CreateMessageInSessionTxRejectsMissingSession(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoUser(t, db, owner, true)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-missing",
		Role:      "user",
		Content:   "late",
	}, t41RepoTestTenant, owner)
	require.ErrorIs(t, err, apperrors.ErrSessionNotFound)
	require.Zero(t, t41RepoMessageCount(t, db, "s-missing"))
}

// TestT41CreateMessageInSessionTxRejectsForeignOwner：
// 会话属于其他账号时，owner 作用域复核必须拒绝，零插入。
func TestT41CreateMessageInSessionTxRejectsForeignOwner(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	other := uuid.NewString()
	t41SeedRepoUser(t, db, owner, true)
	t41SeedRepoUser(t, db, other, true)
	t41SeedRepoSession(t, db, "s-foreign", other, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-foreign",
		Role:      "user",
		Content:   "cross-owner write",
	}, t41RepoTestTenant, owner)
	require.ErrorIs(t, err, apperrors.ErrSessionNotFound)
	require.Zero(t, t41RepoMessageCount(t, db, "s-foreign"))
}

// TestT41CreateMessageInSessionTxRejectsTenantMismatch：
// 租户作用域不匹配时同样拒绝，零插入。
func TestT41CreateMessageInSessionTxRejectsTenantMismatch(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoUser(t, db, owner, true)
	t41SeedRepoSession(t, db, "s-tenant", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-tenant",
		Role:      "user",
		Content:   "wrong tenant",
	}, t41RepoTestTenant+1, owner)
	require.ErrorIs(t, err, apperrors.ErrSessionNotFound)
	require.Zero(t, t41RepoMessageCount(t, db, "s-tenant"))
}

// TestT41CreateMessageInSessionTxRejectsTombstonedOwner：
// 会话 owner 已墓碑化（deleted_at 非空）时拒绝写入。即使会话行因故还在，
// 也不得把消息写进已注销账号的会话。
func TestT41CreateMessageInSessionTxRejectsTombstonedOwner(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoTombstonedUser(t, db, owner)
	t41SeedRepoSession(t, db, "s-tombstone", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-tombstone",
		Role:      "user",
		Content:   "write after tombstone",
	}, t41RepoTestTenant, owner)
	require.Error(t, err, "墓碑化账号的会话不得接受消息写入")
	require.Zero(t, t41RepoMessageCount(t, db, "s-tombstone"))
}

// TestT41CreateMessageInSessionTxRejectsBannedOwner：
// 会话 owner 已被封禁（users.is_active=false，封禁不删会话）时拒绝写入。
func TestT41CreateMessageInSessionTxRejectsBannedOwner(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoUser(t, db, owner, false)
	t41SeedRepoSession(t, db, "s-banned", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-banned",
		Role:      "user",
		Content:   "write after ban",
	}, t41RepoTestTenant, owner)
	require.Error(t, err, "封禁账号的会话不得接受消息写入")
	require.Zero(t, t41RepoMessageCount(t, db, "s-banned"))
}

// TestT41CreateMessageInSessionTxChecksSessionOwnerNotCaller：
// 共享智能体路径会以空 ownerID 调用（会话租户优先、不按调用者过滤），
// 但用户状态复核必须看 session.user_id：会话 owner 已墓碑化时仍拒绝。
func TestT41CreateMessageInSessionTxChecksSessionOwnerNotCaller(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := uuid.NewString()
	t41SeedRepoTombstonedUser(t, db, owner)
	t41SeedRepoSession(t, db, "s-shared-owner", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	_, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-shared-owner",
		Role:      "assistant",
		Content:   "shared agent write",
	}, t41RepoTestTenant, "")
	require.Error(t, err, "空 ownerID 的共享路径也必须复核会话 owner 的存活状态")
	require.Zero(t, t41RepoMessageCount(t, db, "s-shared-owner"))
}

// TestT41CreateMessageInSessionTxAllowsNonCanonicalOwner：
// API/IM/embed 会话的 owner 不是规范 UUID、没有 users 行；用户状态复核必须
// 跳过，消息照常写入（保护既有非 web 会话路径）。
func TestT41CreateMessageInSessionTxAllowsNonCanonicalOwner(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	owner := types.SessionOwnerAPITenantKeyPrefix + "10:1"
	t41SeedRepoSession(t, db, "s-api", owner, t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	created, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-api",
		Role:      "user",
		Content:   "api session message",
	}, t41RepoTestTenant, "")
	require.NoError(t, err)
	require.NotNil(t, created)
	require.EqualValues(t, 1, t41RepoMessageCount(t, db, "s-api"))
}

// TestT41CreateMessageInSessionTxMissingOwnerIDStillVisibleWhenSessionHasNoOwner：
// user_id 为空的历史租户级会话在空 ownerID 下必须保持可见。
func TestT41CreateMessageInSessionTxMissingOwnerIDStillVisibleWhenSessionHasNoOwner(t *testing.T) {
	db, repo := newT41RepoEnv(t)
	t41SeedRepoSession(t, db, "s-legacy", "", t41RepoTestTenant)
	txRepo := t41RequireCreateTxRepo(t, repo)

	created, err := txRepo.CreateMessageInSessionTx(context.Background(), &types.Message{
		SessionID: "s-legacy",
		Role:      "user",
		Content:   "legacy tenant-level session",
	}, t41RepoTestTenant, "")
	require.NoError(t, err)
	require.NotNil(t, created)
	require.EqualValues(t, 1, t41RepoMessageCount(t, db, "s-legacy"))
}
