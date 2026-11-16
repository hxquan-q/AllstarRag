package handler

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 安全热修 fix/suspended-reset-security 的 F1 用例。
//
// F1（High）：用户迁移后 A 空间 suspended、B 空间 active。A 空间的管理员
// 仍可通过 POST /tenants/:id/members/:user_id/reset-password 改其全局密码。
// 修复分两层：handler 在 GetMembership 后拒绝非 active 目标；ResetPasswordTx
// 在事务内复核 active 成员关系（绕过 handler 直调 service 也被拦截）。
//
// 复用 tenant_member_account_test.go 的 stub 与路由助手（同包）。

// ---------- F1-red：handler 层拒绝非 active 目标 ----------

func TestSuspendedReset_F1_Handler_SuspendedTargetForbidden(t *testing.T) {
	ms := &accountTestMemberService{
		getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
			return &types.TenantMember{
				UserID:   userID,
				TenantID: tenantID,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusSuspended,
			}, nil
		},
	}
	us := &accountTestUserService{}
	h := NewTenantMemberHandler(ms, us)

	w := doAccountJSON(t, accountTestRouter(h), http.MethodPost,
		"/tenants/1/members/u-target/reset-password",
		validResetBody(), accountCtxOpts{userID: "u-owner", role: types.TenantRoleOwner})

	require.Equal(t, http.StatusForbidden, w.Code,
		"A 空间 owner 不得重置本空间 suspended 成员的全局密码, body=%s", w.Body.String())
	require.Zero(t, us.resetCalls, "拒绝路径绝不能改密码")
	require.Zero(t, ms.auditCalls, "拒绝路径不得落成功审计")
}

func TestSuspendedReset_F1_Handler_InvitedTargetForbidden(t *testing.T) {
	ms := &accountTestMemberService{
		getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
			return &types.TenantMember{
				UserID:   userID,
				TenantID: tenantID,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusInvited,
			}, nil
		},
	}
	us := &accountTestUserService{}
	h := NewTenantMemberHandler(ms, us)

	w := doAccountJSON(t, accountTestRouter(h), http.MethodPost,
		"/tenants/1/members/u-target/reset-password",
		validResetBody(), accountCtxOpts{userID: "u-owner", role: types.TenantRoleOwner})

	require.Equal(t, http.StatusForbidden, w.Code,
		"invited（尚未生效）目标同样不可重置, body=%s", w.Body.String())
	require.Zero(t, us.resetCalls)
	require.Zero(t, ms.auditCalls)
}

// ---------- F1-regression：active 成员的正常重置不受影响 ----------

func TestSuspendedReset_F1_Handler_ActiveTargetsStillReset(t *testing.T) {
	for _, targetRole := range []types.TenantRole{
		types.TenantRoleAdmin, types.TenantRoleViewer,
	} {
		t.Run(string(targetRole), func(t *testing.T) {
			ms := &accountTestMemberService{
				getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
					return &types.TenantMember{
						UserID:   userID,
						TenantID: tenantID,
						Role:     targetRole,
						Status:   types.TenantMemberStatusActive,
					}, nil
				},
			}
			us := &accountTestUserService{}
			h := NewTenantMemberHandler(ms, us)

			w := doAccountJSON(t, accountTestRouter(h), http.MethodPost,
				"/tenants/1/members/u-target/reset-password",
				validResetBody(), accountCtxOpts{userID: "u-owner", role: types.TenantRoleOwner})

			require.Equal(t, http.StatusOK, w.Code,
				"active %s 的正常重置必须继续工作, body=%s", targetRole, w.Body.String())
			require.Equal(t, 1, us.resetCalls)
			require.Equal(t, "u-target", us.resetTargetID)
			require.Equal(t, 1, ms.auditCalls, "成功重置仍要落审计")
		})
	}
}

// ---------- F1-tx：绕过 handler 直调仓储事务 / 服务层同样被拒 ----------

// fixSuspendedResetTokenRepo 记录 RevokeTokensByUserID 调用，其余方法无操作。
type fixSuspendedResetTokenRepo struct {
	revokedUserIDs []string
}

func (r *fixSuspendedResetTokenRepo) CreateToken(context.Context, *types.AuthToken) error {
	return nil
}

func (r *fixSuspendedResetTokenRepo) GetTokenByValue(context.Context, string) (*types.AuthToken, error) {
	return nil, apprepo.ErrTokenNotFound
}

func (r *fixSuspendedResetTokenRepo) GetTokenByID(context.Context, string) (*types.AuthToken, error) {
	return nil, apprepo.ErrTokenNotFound
}

func (r *fixSuspendedResetTokenRepo) GetTokensByUserID(context.Context, string) ([]*types.AuthToken, error) {
	return nil, nil
}

func (r *fixSuspendedResetTokenRepo) UpdateToken(context.Context, *types.AuthToken) error { return nil }

func (r *fixSuspendedResetTokenRepo) DeleteToken(context.Context, string) error { return nil }

func (r *fixSuspendedResetTokenRepo) DeleteExpiredTokens(context.Context) error { return nil }

func (r *fixSuspendedResetTokenRepo) RevokeTokensByUserID(_ context.Context, userID string) error {
	r.revokedUserIDs = append(r.revokedUserIDs, userID)
	return nil
}

// fixSuspendedResetTx 与生产仓储 ResetPasswordTx 签名一致的窄接口（与 service
// 包的 passwordResetTx 相同），用于直接调用事务入口。
type fixSuspendedResetTx interface {
	ResetPasswordTx(
		ctx context.Context,
		targetUserID string,
		callerTenantID uint64,
		enforceOtherWorkspaceOwnerCheck bool,
		enforcePrivilegedTargetCheck bool,
		passwordHash string,
	) (*types.User, error)
}

// fixSuspendedResetEnv 用真实 SQLite 仓储构造 F1-tx 用例的最小依赖图。
type fixSuspendedResetEnv struct {
	db         *gorm.DB
	userRepo   interfaces.UserRepository
	memberRepo interfaces.TenantMemberRepository
	tokenRepo  *fixSuspendedResetTokenRepo
	userSvc    interfaces.UserService
}

func newFixSuspendedResetEnv(t *testing.T) *fixSuspendedResetEnv {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "f1-suspended-reset.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.TenantMember{}))

	userRepo := apprepo.NewUserRepository(db)
	memberRepo := apprepo.NewTenantMemberRepository(db)
	tokenRepo := &fixSuspendedResetTokenRepo{}
	memberSvc := service.NewTenantMemberService(memberRepo, nil, userRepo, tokenRepo)
	userSvc := service.NewUserService(nil, userRepo, tokenRepo, nil, memberSvc, nil)
	return &fixSuspendedResetEnv{
		db:         db,
		userRepo:   userRepo,
		memberRepo: memberRepo,
		tokenRepo:  tokenRepo,
		userSvc:    userSvc,
	}
}

func (e *fixSuspendedResetEnv) seedUser(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, e.userRepo.CreateUser(context.Background(), &types.User{
		ID:           id,
		Username:     id,
		Email:        id + "@example.com",
		PasswordHash: "old-hash",
		IsActive:     true,
	}))
}

func (e *fixSuspendedResetEnv) seedMembership(
	t *testing.T, userID string, tenantID uint64, role types.TenantRole, status types.TenantMemberStatus,
) {
	t.Helper()
	require.NoError(t, e.memberRepo.Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   status,
	}))
}

func (e *fixSuspendedResetEnv) reloadHash(t *testing.T, id string) string {
	t.Helper()
	var u types.User
	require.NoError(t, e.db.Where("id = ?", id).First(&u).Error)
	return u.PasswordHash
}

// suspendedResetCallerCtx 构造「空间边界调用方」上下文：tenant 1 的 owner、
// 非系统管理员（与 service 包 t30Caller 同一口径）。
func suspendedResetCallerCtx(tenantID uint64, role types.TenantRole) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
	ctx = context.WithValue(ctx, types.SystemAdminContextKey, false)
	return ctx
}

func TestSuspendedReset_F1_Tx_RejectsNonActiveMember(t *testing.T) {
	const oldHash = "old-hash"

	t.Run("仓储事务_返回active成员哨兵且零副作用", func(t *testing.T) {
		env := newFixSuspendedResetEnv(t)
		env.seedUser(t, "target")
		// target：A（caller）空间 suspended viewer，B 空间 active admin。
		env.seedMembership(t, "target", 1, types.TenantRoleViewer, types.TenantMemberStatusSuspended)
		env.seedMembership(t, "target", 2, types.TenantRoleAdmin, types.TenantMemberStatusActive)

		resetter, ok := env.userRepo.(fixSuspendedResetTx)
		require.True(t, ok, "生产仓储必须实现 ResetPasswordTx")

		user, err := resetter.ResetPasswordTx(
			suspendedResetCallerCtx(1, types.TenantRoleOwner),
			"target", 1, true, true, "new-hash",
		)
		require.ErrorIs(t, err, apprepo.ErrResetTargetNotActiveMember)
		require.Nil(t, user)
		require.Equal(t, oldHash, env.reloadHash(t, "target"), "拒绝必须不写密码")
	})

	t.Run("绕过handler直调service_同样拒绝且零副作用", func(t *testing.T) {
		env := newFixSuspendedResetEnv(t)
		env.seedUser(t, "target")
		env.seedMembership(t, "target", 1, types.TenantRoleViewer, types.TenantMemberStatusSuspended)
		env.seedMembership(t, "target", 2, types.TenantRoleAdmin, types.TenantMemberStatusActive)

		err := env.userSvc.AdminResetPassword(
			suspendedResetCallerCtx(1, types.TenantRoleOwner), "target", "NewSecure9")
		require.ErrorIs(t, err, service.ErrCannotResetInactiveMember,
			"仓储哨兵必须映射为服务层 ErrCannotResetInactiveMember")
		require.Equal(t, oldHash, env.reloadHash(t, "target"), "拒绝必须不写密码")
		require.Empty(t, env.tokenRepo.revokedUserIDs, "拒绝不得吊销 token")
	})

	t.Run("active成员_事务与service路径仍放行", func(t *testing.T) {
		env := newFixSuspendedResetEnv(t)
		env.seedUser(t, "plain")
		env.seedMembership(t, "plain", 1, types.TenantRoleViewer, types.TenantMemberStatusActive)

		require.NoError(t, env.userSvc.AdminResetPassword(
			suspendedResetCallerCtx(1, types.TenantRoleOwner), "plain", "NewSecure9"))
		require.NotEqual(t, oldHash, env.reloadHash(t, "plain"), "active 目标必须写入新哈希")
		require.Len(t, env.tokenRepo.revokedUserIDs, 1, "成功重置仍要吊销会话")
		require.Equal(t, "plain", env.tokenRepo.revokedUserIDs[0])
	})
}
