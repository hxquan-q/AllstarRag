package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// CP3-FIX2 L1 验收用例：自助离开（POST /leave）复用 RemoveMember 时，
//   - 成员行已不存在必须幂等成功（不再 404/500）；
//   - 角色 CAS 失配时按最新成员行重试，自助离开没有目标保护需求；
//   - 持续竞争到重试上限 → ErrMembershipChanged（handler 映射 409）；
//   - 管理移除（caller != target）的 404 / 最后 owner 409 语义不变。

// cp3Fix2RoleFlipRepo 让前 N 次 Get 返回过期角色，之后的读取落到真实
// SQLite 仓储。这样能精确构造「目标保护读到 viewer，真实行已是 admin」
// 的并发窗口，同时仍走生产 SoftDeleteWithRole CAS 路径。
type cp3Fix2RoleFlipRepo struct {
	interfaces.TenantMemberRepository
	staleRole  types.TenantRole
	staleReads int
}

func (r *cp3Fix2RoleFlipRepo) Get(
	ctx context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	member, err := r.TenantMemberRepository.Get(ctx, userID, tenantID)
	if err != nil || member == nil {
		return member, err
	}
	if r.staleReads > 0 {
		r.staleReads--
		member.Role = r.staleRole
	}
	return member, nil
}

func (r *cp3Fix2RoleFlipRepo) SoftDeleteWithRole(
	ctx context.Context, userID string, tenantID uint64, expectedRole types.TenantRole,
) error {
	cas, ok := r.TenantMemberRepository.(guardedMemberRemover)
	if !ok {
		return errors.New("test: real repository must implement SoftDeleteWithRole")
	}
	return cas.SoftDeleteWithRole(ctx, userID, tenantID, expectedRole)
}

func TestCP3Fix2SelfLeaveMissingMembershipIsIdempotent(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	svc := NewTenantMemberService(env.memberRepo, env.audit, env.userRepo, nil)

	// caller == target 且成员行本就不存在：视为「已经离开」，返回 nil。
	ctx := umCallerCtx("u-leaver", tenantID, types.TenantRoleViewer, false)
	require.NoError(t, svc.RemoveMember(ctx, "u-leaver", tenantID))

	row, err := env.memberRepo.Get(context.Background(), "u-leaver", tenantID)
	require.NoError(t, err)
	require.Nil(t, row, "幂等离开不能创建成员行")
}

func TestCP3Fix2SelfLeaveRetriesAgainstFreshRole(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "u-admin", nil)
	omOwnerSeedMembership(t, env, "u-admin", tenantID, types.TenantRoleAdmin)

	// 第一次读：过期快照 viewer；重试读：真实行 admin。
	repo := &cp3Fix2RoleFlipRepo{
		TenantMemberRepository: env.memberRepo,
		staleRole:              types.TenantRoleViewer,
		staleReads:             1,
	}
	svc := NewTenantMemberService(repo, env.audit, env.userRepo, nil)
	ctx := umCallerCtx("u-admin", tenantID, types.TenantRoleAdmin, false)

	require.NoError(t, svc.RemoveMember(ctx, "u-admin", tenantID),
		"self-leave must retry the role CAS against the fresh admin row")

	row, err := env.memberRepo.Get(context.Background(), "u-admin", tenantID)
	require.NoError(t, err)
	require.Nil(t, row, "重试成功后成员行必须已软删")
}

func TestCP3Fix2SelfLeaveRetryExhaustionReturnsMembershipChanged(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "u-admin", nil)
	omOwnerSeedMembership(t, env, "u-admin", tenantID, types.TenantRoleAdmin)

	// 每次都读到过期 viewer：CAS 永远失败，3 次后放弃并把冲突交给 handler 映射 409。
	repo := &cp3Fix2RoleFlipRepo{
		TenantMemberRepository: env.memberRepo,
		staleRole:              types.TenantRoleViewer,
		staleReads:             100,
	}
	svc := NewTenantMemberService(repo, env.audit, env.userRepo, nil)
	ctx := umCallerCtx("u-admin", tenantID, types.TenantRoleAdmin, false)

	err := svc.RemoveMember(ctx, "u-admin", tenantID)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMembershipChanged),
		"retry exhaustion must surface ErrMembershipChanged (409), got %v", err)

	row, getErr := env.memberRepo.Get(context.Background(), "u-admin", tenantID)
	require.NoError(t, getErr)
	require.NotNil(t, row, "持续 CAS 失配不能删除成员行")
	require.Equal(t, types.TenantRoleAdmin, row.Role)
	require.Equal(t, types.TenantMemberStatusActive, row.Status)
}

func TestCP3Fix2AdminRemoveMissingMembershipStillNotFound(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	svc := NewTenantMemberService(env.memberRepo, env.audit, env.userRepo, nil)

	// caller != target 是管理移除：不存在仍返回 404 语义。
	ctx := umCallerCtx("u-owner", tenantID, types.TenantRoleOwner, false)
	err := svc.RemoveMember(ctx, "u-ghost", tenantID)
	require.True(t, errors.Is(err, ErrMembershipNotFound),
		"admin remove of a missing membership must stay ErrMembershipNotFound, got %v", err)
}

func TestCP3Fix2AdminRemoveLastOwnerStillProtected(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "u-only-owner", nil)
	omOwnerSeedMembership(t, env, "u-only-owner", tenantID, types.TenantRoleOwner)
	svc := NewTenantMemberService(env.memberRepo, env.audit, env.userRepo, nil)

	// 系统管理员发起管理移除，绕开同级目标保护，落到最后 owner 保护。
	ctx := umCallerCtx("u-sys", tenantID, types.TenantRoleOwner, true)
	err := svc.RemoveMember(ctx, "u-only-owner", tenantID)
	require.True(t, errors.Is(err, ErrLastOwner),
		"removing the last owner must stay ErrLastOwner (409), got %v", err)

	row, getErr := env.memberRepo.Get(context.Background(), "u-only-owner", tenantID)
	require.NoError(t, getErr)
	require.NotNil(t, row, "最后 owner 不能被删除")
	require.Equal(t, types.TenantRoleOwner, row.Role)
}
