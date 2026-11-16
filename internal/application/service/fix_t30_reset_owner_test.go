package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// T30 红阶段：空间级 owner/admin 不能重置“在别处持有 owner 角色”的账号密码，
// 否则改掉的是全局密码并吊销全部会话，等于跨空间接管。
//
// 该测试用真实 SQLite 仓储 + 真实 TenantMemberService，覆盖 service 层的
// fail-closed 判定；handler 只是把错误映射成 403，不在这里重复。

type t30Env struct {
	svc      *userService
	userRepo interface {
		GetUserByID(context.Context, string) (*types.User, error)
		UpdateUser(context.Context, *types.User) error
		CreateUser(context.Context, *types.User) error
	}
	memberRepo interface {
		Create(context.Context, *types.TenantMember) error
	}
	tokenRepo *stubAuthTokenRepo
}

func newT30Env(t *testing.T) *t30Env {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "t30.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&types.User{},
		&types.TenantMember{},
		&types.AuthToken{},
	))

	userRepo := apprepo.NewUserRepository(db)
	memberRepo := apprepo.NewTenantMemberRepository(db)
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	memberSvc := NewTenantMemberService(memberRepo, nil, userRepo, tokenRepo)
	svc := &userService{
		userRepo:      userRepo,
		tokenRepo:     tokenRepo,
		memberService: memberSvc,
	}
	return &t30Env{
		svc:        svc,
		userRepo:   userRepo,
		memberRepo: memberRepo,
		tokenRepo:  tokenRepo,
	}
}

func (e *t30Env) seedUser(t *testing.T, id string, mutate func(*types.User)) {
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
	require.NoError(t, e.userRepo.CreateUser(context.Background(), u))
}

func (e *t30Env) seedMembership(t *testing.T, userID string, tenantID uint64, role types.TenantRole) {
	t.Helper()
	require.NoError(t, e.memberRepo.Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
	}))
}

func t30Caller(tenantID uint64, role types.TenantRole, sysadmin bool) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
	ctx = context.WithValue(ctx, types.SystemAdminContextKey, sysadmin)
	return ctx
}

func TestT30WorkspaceOwnerCannotResetElsewhereOwner(t *testing.T) {
	env := newT30Env(t)
	// target：A 空间 viewer，B 空间 owner。
	env.seedUser(t, "target", nil)
	env.seedMembership(t, "target", 1, types.TenantRoleViewer)
	env.seedMembership(t, "target", 2, types.TenantRoleOwner)

	ctx := t30Caller(1, types.TenantRoleOwner, false)
	err := env.svc.AdminResetPassword(ctx, "target", "NewSecure9")
	require.Error(t, err, "A 空间 owner 不得重置 B 空间 owner 的全局密码")

	// 拒绝必须零副作用：密码未改、token 未吊销。
	got, getErr := env.userRepo.GetUserByID(context.Background(), "target")
	require.NoError(t, getErr)
	require.Equal(t, "old-hash", got.PasswordHash)
	require.Empty(t, env.tokenRepo.revokedUserIDs)
}

func TestT30SameWorkspaceOwnerMayReset(t *testing.T) {
	env := newT30Env(t)
	env.seedUser(t, "target", nil)
	env.seedMembership(t, "target", 2, types.TenantRoleOwner)

	ctx := t30Caller(2, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.AdminResetPassword(ctx, "target", "NewSecure9"))
	require.Len(t, env.tokenRepo.revokedUserIDs, 1)
	require.Equal(t, "target", env.tokenRepo.revokedUserIDs[0])
}

func TestT30SystemAdminMayResetElsewhereOwner(t *testing.T) {
	env := newT30Env(t)
	env.seedUser(t, "target", nil)
	env.seedMembership(t, "target", 2, types.TenantRoleOwner)

	ctx := t30Caller(0, types.TenantRoleViewer, true)
	require.NoError(t, env.svc.AdminResetPassword(ctx, "target", "NewSecure9"))
}

func TestT30PlainMemberResetStillWorks(t *testing.T) {
	env := newT30Env(t)
	env.seedUser(t, "target", nil)
	env.seedMembership(t, "target", 1, types.TenantRoleViewer)

	ctx := t30Caller(1, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.AdminResetPassword(ctx, "target", "NewSecure9"))
}

func TestT30PrivilegedTargetProtectionPreserved(t *testing.T) {
	env := newT30Env(t)
	env.seedUser(t, "sys", func(u *types.User) { u.IsSystemAdmin = true })
	env.seedMembership(t, "sys", 1, types.TenantRoleViewer)
	env.seedUser(t, "cross", func(u *types.User) { u.CanAccessAllTenants = true })
	env.seedMembership(t, "cross", 1, types.TenantRoleViewer)

	ctx := t30Caller(1, types.TenantRoleOwner, false)
	require.ErrorIs(t, env.svc.AdminResetPassword(ctx, "sys", "NewSecure9"), ErrCannotResetPrivilegedTarget)
	require.ErrorIs(t, env.svc.AdminResetPassword(ctx, "cross", "NewSecure9"), ErrCannotResetPrivilegedTarget)
	require.Empty(t, env.tokenRepo.revokedUserIDs)
}
