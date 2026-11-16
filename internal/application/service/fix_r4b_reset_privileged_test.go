package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// R4b / F42：AdminResetPassword 必须把仓储事务内抛出的
// apprepo.ErrResetTargetPrivileged 映射为既有 ErrCannotResetPrivilegedTarget，
// 并且不进入 token 吊销；系统管理边界上则传 enforcePrivilegedTargetCheck=false。
// 另含一条确定性 TOCTOU 用例：事务外预检读到普通账号，事务内读到已提权账号。

// r4bResetTxStubRepo 只实现 AdminResetPassword 会触达的两个方法；其余
// interfaces.UserRepository 方法由嵌入的 nil 接口兜底（本用例不会调用）。
type r4bResetTxStubRepo struct {
	interfaces.UserRepository

	user *types.User

	resetErr     error
	resetCalls   int
	enforcePriv  *bool
	callerTenant uint64
	passwordHash string
}

func (r *r4bResetTxStubRepo) GetUserByID(context.Context, string) (*types.User, error) {
	return r.user, nil
}

func (r *r4bResetTxStubRepo) ResetPasswordTx(
	_ context.Context,
	_ string,
	callerTenantID uint64,
	_ bool,
	enforcePrivilegedTargetCheck bool,
	passwordHash string,
) (*types.User, error) {
	r.resetCalls++
	r.enforcePriv = &enforcePrivilegedTargetCheck
	r.callerTenant = callerTenantID
	r.passwordHash = passwordHash
	if r.resetErr != nil {
		return nil, r.resetErr
	}
	return r.user, nil
}

func TestR4bAdminResetPasswordMapsPrivilegedResetError(t *testing.T) {
	// 事务外预检看到的是普通账号（无平台特权），因此会走到事务路径。
	userRepo := &r4bResetTxStubRepo{
		user:     &types.User{ID: "target", PasswordHash: "old-hash", IsActive: true},
		resetErr: apprepo.ErrResetTargetPrivileged,
	}
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := &userService{userRepo: userRepo, tokenRepo: tokenRepo}

	ctx := t30Caller(1, types.TenantRoleOwner, false)
	err := svc.AdminResetPassword(ctx, "target", "NewSecure9")

	require.ErrorIs(t, err, ErrCannotResetPrivilegedTarget)
	require.Equal(t, 1, userRepo.resetCalls)
	require.NotNil(t, userRepo.enforcePriv)
	require.True(t, *userRepo.enforcePriv, "非系统管理边界必须要求事务内复核特权目标")
	require.Equal(t, uint64(1), userRepo.callerTenant)
	require.Empty(t, tokenRepo.revokedUserIDs, "拒绝时不得吊销 token")
}

func TestR4bAdminResetPasswordSystemAdminBoundaryAllowsPrivileged(t *testing.T) {
	// 系统管理边界上服务层应传 enforcePrivilegedTargetCheck=false，
	// 让特权账号可以正常重置。
	userRepo := &r4bResetTxStubRepo{
		user: &types.User{ID: "sys", PasswordHash: "old-hash", IsActive: true, IsSystemAdmin: true},
	}
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := &userService{userRepo: userRepo, tokenRepo: tokenRepo}

	ctx := t30Caller(0, types.TenantRoleViewer, true)
	require.NoError(t, svc.AdminResetPassword(ctx, "sys", "NewSecure9"))

	require.Equal(t, 1, userRepo.resetCalls)
	require.NotNil(t, userRepo.enforcePriv)
	require.False(t, *userRepo.enforcePriv, "系统管理边界不应要求事务内拒绝特权目标")
	require.Len(t, tokenRepo.revokedUserIDs, 1)
	require.Equal(t, "sys", tokenRepo.revokedUserIDs[0])
}

// r4bStaleGetUserRepo 模拟「事务外预检读到普通账号、事务内目标已被提权」的
// 竞态窗口：GetUserByID 返回过期快照，ResetPasswordTx 委托真实 SQLite 仓储。
type r4bStaleGetUserRepo struct {
	interfaces.UserRepository

	resetter       passwordResetTx
	stale          *types.User
	enforcePrivObs bool
}

func (r *r4bStaleGetUserRepo) GetUserByID(context.Context, string) (*types.User, error) {
	return r.stale, nil
}

func (r *r4bStaleGetUserRepo) ResetPasswordTx(
	ctx context.Context,
	targetUserID string,
	callerTenantID uint64,
	enforceOtherWorkspaceOwnerCheck bool,
	enforcePrivilegedTargetCheck bool,
	passwordHash string,
) (*types.User, error) {
	r.enforcePrivObs = enforcePrivilegedTargetCheck
	return r.resetter.ResetPasswordTx(
		ctx, targetUserID, callerTenantID,
		enforceOtherWorkspaceOwnerCheck, enforcePrivilegedTargetCheck, passwordHash,
	)
}

func TestR4bAdminResetPasswordRechecksPrivilegeInsideTx(t *testing.T) {
	env := newT30Env(t)
	// 数据库中的目标已经是系统管理员。
	env.seedUser(t, "target", func(u *types.User) { u.IsSystemAdmin = true })

	realRepo, ok := env.userRepo.(interfaces.UserRepository)
	require.True(t, ok)
	resetter, ok := realRepo.(passwordResetTx)
	require.True(t, ok)

	wrapper := &r4bStaleGetUserRepo{
		UserRepository: realRepo,
		resetter:       resetter,
		// 事务外预检返回的过期快照：普通账号。
		stale: &types.User{ID: "target", PasswordHash: "old-hash", IsActive: true},
	}
	svc := &userService{
		userRepo:      wrapper,
		tokenRepo:     env.tokenRepo,
		memberService: env.svc.memberService,
	}

	ctx := t30Caller(1, types.TenantRoleOwner, false)
	err := svc.AdminResetPassword(ctx, "target", "NewSecure9")

	require.ErrorIs(t, err, ErrCannotResetPrivilegedTarget,
		"事务内复核必须拦住预检后提权的目标")
	require.True(t, wrapper.enforcePrivObs)
	require.Empty(t, env.tokenRepo.revokedUserIDs, "拒绝时不得吊销 token")

	got, getErr := env.userRepo.GetUserByID(context.Background(), "target")
	require.NoError(t, getErr)
	require.Equal(t, "old-hash", got.PasswordHash, "拒绝必须零副作用")
}
