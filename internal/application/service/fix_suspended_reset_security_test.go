package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 安全热修 fix/suspended-reset-security 的 F2 用例。
//
// F2（Medium）：cleanupRemovedMemberState 旧实现「读整个 User → 改
// tenant_id / LastActiveTenantID → UpdateUser 整行 Save」。若在读取快照与
// 写回之间发生并发密码重置，整行 Save 会把旧 password_hash 写回。修复后
// 只 UPDATE 需要改的列（tenant_id / preferences / updated_at）。
//
// 复用 user_auth_token_test.go 的 stubAuthTokenRepo（同包）。

const (
	f2TenantID  = uint64(7)
	f2OldHash   = "old-hash"
	f2RaceHash  = "hash-after-concurrent-reset"
	f2KeepInstr = "keep-me"
)

func newF2CleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "f2-cleanup.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.TenantMember{}))
	return db
}

// f2SeedRemovedUser 造一个「home=7、last-active=7、还带着无关偏好键」的用户，
// 指针清理才有实际写入，且能验证 preferences 整列写入不丢其他键。
func f2SeedRemovedUser(t *testing.T, db *gorm.DB, id string, homeTenantID uint64) {
	t.Helper()
	instr := f2KeepInstr
	lastActive := f2TenantID
	user := &types.User{
		ID:           id,
		Username:     id,
		Email:        id + "@example.com",
		PasswordHash: f2OldHash,
		IsActive:     true,
		TenantID:     homeTenantID,
		Preferences: types.UserPreferences{
			LastActiveTenantID:        &lastActive,
			BrowserSearchInstructions: &instr,
		},
	}
	require.NoError(t, apprepo.NewUserRepository(db).CreateUser(context.Background(), user))
}

func f2SeedMembership(t *testing.T, db *gorm.DB, userID string) {
	t.Helper()
	require.NoError(t, apprepo.NewTenantMemberRepository(db).Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: f2TenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
	}))
}

// f2SelfLeaveCtx 走自助离开语义（caller == target），跳过移除路径的目标
// 保护，聚焦 cleanupRemovedMemberState 的写回行为。
func f2SelfLeaveCtx(userID string) context.Context {
	return context.WithValue(context.Background(), types.UserIDContextKey, userID)
}

func f2ReloadUser(t *testing.T, db *gorm.DB, id string) *types.User {
	t.Helper()
	var u types.User
	require.NoError(t, db.Where("id = ?", id).First(&u).Error)
	return &u
}

// f2RaceUserRepo 在 cleanupRemovedMemberState 读到用户快照之后、写回之前，
// 直接改库里的 password_hash，模拟一次并发密码重置。返回给 service 的仍是
// 旧快照（old-hash）——若 cleanup 整行 Save，旧哈希就会被写回（F2 红条件）。
type f2RaceUserRepo struct {
	interfaces.UserRepository
	db     *gorm.DB
	hooked bool
}

func (r *f2RaceUserRepo) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	u, err := r.UserRepository.GetUserByID(ctx, id)
	if err != nil || u == nil || r.hooked {
		return u, err
	}
	r.hooked = true
	if err := r.db.Exec(
		"UPDATE users SET password_hash = ? WHERE id = ?", f2RaceHash, id,
	).Error; err != nil {
		return nil, err
	}
	return u, nil // 返回的 u 仍持有旧哈希
}

// UpdateUserColumns 透传给真实仓储，让 cleanup 走列级更新路径。
func (r *f2RaceUserRepo) UpdateUserColumns(
	ctx context.Context, id string, updates map[string]any,
) error {
	updater, ok := r.UserRepository.(userColumnUpdater)
	if !ok {
		return errors.New("test: underlying user repository lacks UpdateUserColumns")
	}
	return updater.UpdateUserColumns(ctx, id, updates)
}

// F2：cleanup 读到旧快照后发生并发密码重置，指针清理绝不能把旧哈希写回。
func TestCleanupRemovedMemberKeepsConcurrentPasswordHash(t *testing.T) {
	db := newF2CleanupDB(t)
	f2SeedRemovedUser(t, db, "f2-target", f2TenantID)
	f2SeedMembership(t, db, "f2-target")

	tokenRepo := &stubAuthTokenRepo{}
	userRepo := &f2RaceUserRepo{UserRepository: apprepo.NewUserRepository(db), db: db}
	svc := NewTenantMemberService(
		apprepo.NewTenantMemberRepository(db), nil, userRepo, tokenRepo)

	require.NoError(t, svc.RemoveMember(f2SelfLeaveCtx("f2-target"), "f2-target", f2TenantID))
	require.True(t, userRepo.hooked, "夹具必须先制造并发密码重置再触发写回")

	got := f2ReloadUser(t, db, "f2-target")
	require.Equal(t, f2RaceHash, got.PasswordHash,
		"指针清理只允许改列，绝不能把旧快照的 password_hash 写回")
	require.Zero(t, got.TenantID, "home 指针仍要清零")
	require.Nil(t, got.Preferences.LastActiveTenantID, "last-active 偏好仍要清除")
	require.NotNil(t, got.Preferences.BrowserSearchInstructions)
	require.Equal(t, f2KeepInstr, *got.Preferences.BrowserSearchInstructions,
		"preferences 整列写入不得丢失其他偏好键")
	require.Len(t, tokenRepo.revokedUserIDs, 1, "移除后仍要吊销会话")
}

// F2-regression：正常移除成员后 home / 偏好清零照旧，密码哈希不被触碰。
func TestCleanupRemovedMemberStillClearsHomePointers(t *testing.T) {
	db := newF2CleanupDB(t)
	f2SeedRemovedUser(t, db, "f2-target", f2TenantID)
	f2SeedMembership(t, db, "f2-target")

	tokenRepo := &stubAuthTokenRepo{}
	userRepo := apprepo.NewUserRepository(db)
	svc := NewTenantMemberService(
		apprepo.NewTenantMemberRepository(db), nil, userRepo, tokenRepo)

	require.NoError(t, svc.RemoveMember(f2SelfLeaveCtx("f2-target"), "f2-target", f2TenantID))

	got := f2ReloadUser(t, db, "f2-target")
	require.Zero(t, got.TenantID, "正常移除后 home 仍要清零")
	require.Nil(t, got.Preferences.LastActiveTenantID)
	require.Equal(t, f2OldHash, got.PasswordHash, "无并发写时密码哈希必须原样")
	require.Len(t, tokenRepo.revokedUserIDs, 1)
}

// F2.2（auto-accept home 回填原语）：SetHomeTenantIfEmpty 只写 tenant_id 列，
// 不把旧快照整行写回，并发修改的密码哈希必须保留。
func TestAutoAcceptHomeBackfillKeepsConcurrentPasswordHash(t *testing.T) {
	db := newF2CleanupDB(t)
	f2SeedRemovedUser(t, db, "f2-adoptee", 0) // 无 home，回填才有实际写入

	// 模拟并发密码重置发生在任何读快照之后。
	require.NoError(t, db.Exec(
		"UPDATE users SET password_hash = ? WHERE id = ?", f2RaceHash, "f2-adoptee",
	).Error)

	svc := &userService{userRepo: apprepo.NewUserRepository(db)}
	set, err := svc.SetHomeTenantIfEmpty(context.Background(), "f2-adoptee", f2TenantID)
	require.NoError(t, err)
	require.True(t, set, "无 home 的用户必须完成回填")

	got := f2ReloadUser(t, db, "f2-adoptee")
	require.Equal(t, f2TenantID, got.TenantID)
	require.Equal(t, f2RaceHash, got.PasswordHash,
		"home 回填只允许写 tenant_id 列，不得覆盖并发写入的密码哈希")

	setAgain, err := svc.SetHomeTenantIfEmpty(context.Background(), "f2-adoptee", 9)
	require.NoError(t, err)
	require.False(t, setAgain, "已有 home 时不得改写")
	require.Equal(t, f2TenantID, f2ReloadUser(t, db, "f2-adoptee").TenantID)
}
