package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// R4b / F42：ResetPasswordTx 在锁定并读出目标 users 行后，若
// enforcePrivilegedTargetCheck 为真且目标持有平台特权（IsSystemAdmin /
// CanAccessAllTenants），必须在事务内拒绝，且不写密码（零副作用）。
//
// 这里用真实 SQLite 仓储直接调用 ResetPasswordTx，隔离事务外预检，
// 证明拦截点确实在仓储事务内部，而不是只靠服务层预检。

// r4bPasswordResetter 是生产仓储实现的事务化重置窄接口（与 service 包中的
// passwordResetTx 方法签名一致），用于在测试里做类型断言。
type r4bPasswordResetter interface {
	ResetPasswordTx(
		ctx context.Context,
		targetUserID string,
		callerTenantID uint64,
		enforceOtherWorkspaceOwnerCheck bool,
		enforcePrivilegedTargetCheck bool,
		passwordHash string,
	) (*types.User, error)
}

func newR4bResetRepo(t *testing.T) (r4bPasswordResetter, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "r4b-reset.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.User{}))

	repo := NewUserRepository(db)
	resetter, ok := repo.(r4bPasswordResetter)
	require.True(t, ok, "生产仓储必须实现事务化密码重置接口")
	return resetter, db
}

func seedR4bResetUser(t *testing.T, db *gorm.DB, id string, mutate func(*types.User)) {
	t.Helper()
	u := &types.User{
		ID:           id,
		Username:     id,
		Email:        id + "@example.com",
		PasswordHash: "old-hash",
		IsActive:     true,
	}
	if mutate != nil {
		mutate(u)
	}
	require.NoError(t, NewUserRepository(db).CreateUser(context.Background(), u))
}

func reloadR4bResetUser(t *testing.T, db *gorm.DB, id string) *types.User {
	t.Helper()
	var got types.User
	require.NoError(t, db.Unscoped().Where("id = ?", id).First(&got).Error)
	return &got
}

func TestR4bResetPasswordTxPrivilegedTargetCheck(t *testing.T) {
	const (
		callerTenantID = uint64(1)
		oldHash        = "old-hash"
		newHash        = "new-hash"
	)

	t.Run("IsSystemAdmin_拒绝且零副作用", func(t *testing.T) {
		resetter, db := newR4bResetRepo(t)
		seedR4bResetUser(t, db, "sys", func(u *types.User) { u.IsSystemAdmin = true })
		before := reloadR4bResetUser(t, db, "sys")

		user, err := resetter.ResetPasswordTx(
			context.Background(), "sys", callerTenantID, false, true, newHash,
		)
		require.ErrorIs(t, err, ErrResetTargetPrivileged)
		require.Nil(t, user)

		after := reloadR4bResetUser(t, db, "sys")
		require.Equal(t, oldHash, after.PasswordHash, "拒绝必须不写密码")
		require.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "拒绝必须不触碰 updated_at")
	})

	t.Run("CanAccessAllTenants_拒绝且零副作用", func(t *testing.T) {
		resetter, db := newR4bResetRepo(t)
		seedR4bResetUser(t, db, "cross", func(u *types.User) { u.CanAccessAllTenants = true })
		before := reloadR4bResetUser(t, db, "cross")

		user, err := resetter.ResetPasswordTx(
			context.Background(), "cross", callerTenantID, false, true, newHash,
		)
		require.ErrorIs(t, err, ErrResetTargetPrivileged)
		require.Nil(t, user)

		after := reloadR4bResetUser(t, db, "cross")
		require.Equal(t, oldHash, after.PasswordHash, "拒绝必须不写密码")
		require.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "拒绝必须不触碰 updated_at")
	})

	t.Run("系统管理边界_特权目标可重置", func(t *testing.T) {
		resetter, db := newR4bResetRepo(t)
		seedR4bResetUser(t, db, "sys", func(u *types.User) { u.IsSystemAdmin = true })

		user, err := resetter.ResetPasswordTx(
			context.Background(), "sys", callerTenantID, false, false, newHash,
		)
		require.NoError(t, err)
		require.NotNil(t, user)

		after := reloadR4bResetUser(t, db, "sys")
		require.Equal(t, newHash, after.PasswordHash, "系统管理边界应允许重置特权账号")
	})

	t.Run("普通目标_正常重置", func(t *testing.T) {
		resetter, db := newR4bResetRepo(t)
		seedR4bResetUser(t, db, "plain", nil)

		user, err := resetter.ResetPasswordTx(
			context.Background(), "plain", callerTenantID, false, true, newHash,
		)
		require.NoError(t, err)
		require.NotNil(t, user)

		after := reloadR4bResetUser(t, db, "plain")
		require.Equal(t, newHash, after.PasswordHash)
	})

	t.Run("目标不存在", func(t *testing.T) {
		resetter, _ := newR4bResetRepo(t)

		_, err := resetter.ResetPasswordTx(
			context.Background(), "missing", callerTenantID, false, true, newHash,
		)
		require.ErrorIs(t, err, ErrUserNotFound)
	})
}
