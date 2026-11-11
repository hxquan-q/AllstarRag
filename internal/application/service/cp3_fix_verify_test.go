package service

import (
	"context"
	"errors"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// CP3 修复复验（fix_round=1）：
//   - B2：目标保护读快照后角色被并发修改，删除必须 CAS 失败（409）且不落库；
//   - B1(b)：from==to 交接必须服务层 400 拒绝；
//   - B4：Accept 取得用户锁后重读邀请，撤销/过期时返回错误且不写成员行。

// staleRoleMemberRepo 模拟「目标保护读到旧角色，真实行已被并发改角色」：
// Get 返回 staleRole，其余读写都落到真实 SQLite 仓储。显式转发
// SoftDeleteWithRole，保证服务层走生产 CAS 路径而不是测试回退路径。
type staleRoleMemberRepo struct {
	interfaces.TenantMemberRepository
	staleRole types.TenantRole
}

func (r staleRoleMemberRepo) Get(
	ctx context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	member, err := r.TenantMemberRepository.Get(ctx, userID, tenantID)
	if err != nil || member == nil {
		return member, err
	}
	member.Role = r.staleRole
	return member, nil
}

func (r staleRoleMemberRepo) SoftDeleteWithRole(
	ctx context.Context, userID string, tenantID uint64, expectedRole types.TenantRole,
) error {
	cas, ok := r.TenantMemberRepository.(guardedMemberRemover)
	if !ok {
		return errors.New("test: real repository must implement SoftDeleteWithRole")
	}
	return cas.SoftDeleteWithRole(ctx, userID, tenantID, expectedRole)
}

func TestCP3B2RemoveMemberStaleRoleDoesNotDelete(t *testing.T) {
	const tenantID uint64 = 101
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "u-owner", nil)
	omOwnerSeedUser(t, env, "u-target", nil)
	omOwnerSeedMembership(t, env, "u-owner", tenantID, types.TenantRoleOwner)
	omOwnerSeedMembership(t, env, "u-target", tenantID, types.TenantRoleAdmin)

	// 快照说目标还是 viewer（旧值）；真实行已是 admin。若删除不带
	// expectedRole，就会把并发提权后的同级管理员误删。
	svc := NewTenantMemberService(
		staleRoleMemberRepo{TenantMemberRepository: env.memberRepo, staleRole: types.TenantRoleViewer},
		env.audit, env.userRepo, nil)
	ctx := umCallerCtx("u-owner", tenantID, types.TenantRoleOwner, false)

	err := svc.RemoveMember(ctx, "u-target", tenantID)
	require.Error(t, err, "expectedRole 失配时删除必须失败")
	require.True(t, errors.Is(err, ErrMembershipChanged),
		"must map repository ErrRoleChanged to ErrMembershipChanged (409), got %v", err)

	row, getErr := env.memberRepo.Get(context.Background(), "u-target", tenantID)
	require.NoError(t, getErr)
	require.NotNil(t, row, "成员行不能被删除")
	require.Equal(t, types.TenantMemberStatusActive, row.Status, "成员状态不能变")
	require.Equal(t, types.TenantRoleAdmin, row.Role, "并发修改后的角色必须保留")
}

func TestCP3B1TransferOwnerSameUserRejected400(t *testing.T) {
	env := newOMOwnerEnv(t)
	ctx := umCallerCtx("u-owner", 101, types.TenantRoleOwner, false)

	err := env.svc.TransferOwner(ctx, 101, "u-owner", "u-owner")
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "must be AppError, got %T: %v", err, err)
	require.Equal(t, 400, appErr.HTTPCode, "from==to 交接必须 400")
}

// flippingInvitationRepo 在第二次 GetByID（锁内重读）时把邀请改成
// 撤销/过期，模拟「等待用户锁期间状态被并发改变」。
type flippingInvitationRepo struct {
	interfaces.TenantInvitationRepository
	reads int
	flip  func(*types.TenantInvitation)
}

func (r *flippingInvitationRepo) GetByID(
	ctx context.Context, id uint64,
) (*types.TenantInvitation, error) {
	r.reads++
	inv, err := r.TenantInvitationRepository.GetByID(ctx, id)
	if err != nil || inv == nil {
		return inv, err
	}
	if r.reads >= 2 {
		r.flip(inv)
	}
	return inv, nil
}

func TestCP3B4AcceptRechecksInvitationUnderLock(t *testing.T) {
	cases := []struct {
		name   string
		flip   func(*types.TenantInvitation)
		assert func(*testing.T, error)
	}{
		{
			name: "revoked_while_waiting_for_lock",
			flip: func(inv *types.TenantInvitation) {
				inv.Status = types.TenantInvitationStatusRevoked
			},
			assert: func(t *testing.T, err error) {
				require.True(t, errors.Is(err, ErrInvitationNotPending),
					"锁内发现已撤销必须返回 ErrInvitationNotPending, got %v", err)
			},
		},
		{
			name: "expired_while_waiting_for_lock",
			flip: func(inv *types.TenantInvitation) {
				inv.ExpiresAt = time.Now().Add(-time.Minute)
			},
			assert: func(t *testing.T, err error) {
				require.True(t, errors.Is(err, ErrInvitationExpired),
					"锁内发现已过期必须返回 ErrInvitationExpired, got %v", err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newFakeInvitationRepo()
			flipRepo := &flippingInvitationRepo{TenantInvitationRepository: base, flip: tc.flip}
			memberSvc, _ := newServiceWithRepo()
			svc := NewTenantInvitationService(flipRepo, memberSvc, nil)
			ctx := context.Background()

			inv, err := svc.Create(ctx, 1, "u-bob", types.TenantRoleViewer, nil, "")
			require.NoError(t, err)

			_, err = svc.Accept(ctx, inv.ID, "u-bob")
			tc.assert(t, err)

			member, getErr := memberSvc.GetMembership(ctx, "u-bob", 1)
			require.NoError(t, getErr)
			require.Nil(t, member, "锁内失效时不得写成员行")

			row, getErr := base.GetByID(ctx, inv.ID)
			require.NoError(t, getErr)
			require.NotNil(t, row)
			require.Equal(t, types.TenantInvitationStatusPending, row.Status,
				"锁内失效时邀请必须保持 pending，不能被标记 accepted")
			require.Equal(t, 2, flipRepo.reads,
				"Accept 必须做锁内重读（一次锁前 + 一次锁内）")
		})
	}
}
