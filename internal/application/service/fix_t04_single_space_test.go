package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T04 红阶段：一号一空间守卫 + users.tenant_id 维护（AC-15/16/17/18/19/20）。
//
// 锁定以下新行为（当前实现尚未具备，因此预期这些用例失败）：
//   - 非超管同一时刻最多一条 active/invited 成员关系；
//   - is_system_admin=true 或 can_access_all_tenants=true 豁免；
//   - AddMember / EnsureOwner / Accept / AcceptByToken / ProvisionOwnerSpace
//     冲突时不写成员行（邀请接受失败时邀请也不能被标记 accepted）；
//   - 成员创建成功且 users.tenant_id=0 时写入该空间；移除 home 空间清零；
//   - 并发写同一用户恰好一个成功。
//
// 复用 user_management_owner_test.go 的 newOMOwnerEnv（真实 SQLite 仓储），
// 不改生产接口。

// t04BelongingMembers 返回「属于一个空间」的成员行：active + invited。
// suspended/deleted 不计（AC-18）。
func t04BelongingMembers(t *testing.T, env *omOwnerEnv, userID string) []*types.TenantMember {
	t.Helper()
	rows, err := env.memberRepo.ListByUser(context.Background(), userID)
	require.NoError(t, err)
	var out []*types.TenantMember
	for _, row := range rows {
		if row == nil || row.DeletedAt.Valid {
			continue
		}
		if row.Status == types.TenantMemberStatusActive ||
			row.Status == types.TenantMemberStatusInvited {
			out = append(out, row)
		}
	}
	return out
}

func t04LoadUser(t *testing.T, env *omOwnerEnv, userID string) *types.User {
	t.Helper()
	user, err := env.userRepo.GetUserByID(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, user)
	return user
}

func t04SeedInvitedMembership(
	t *testing.T, env *omOwnerEnv, userID string, tenantID uint64, role types.TenantRole,
) {
	t.Helper()
	require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusInvited,
		JoinedAt: time.Now(),
	}))
}

func t04SeedInvitation(
	t *testing.T, env *omOwnerEnv, tenantID uint64, invitee string, role types.TenantRole,
) *types.TenantInvitation {
	t.Helper()
	inv := &types.TenantInvitation{
		TenantID:      tenantID,
		InviteeUserID: invitee,
		Role:          role,
		Status:        types.TenantInvitationStatusPending,
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	require.NoError(t, env.inviteRepo.Create(context.Background(), inv))
	return inv
}

func t04InvitationStatus(
	t *testing.T, env *omOwnerEnv, invID uint64,
) types.TenantInvitationStatus {
	t.Helper()
	inv, err := env.inviteRepo.GetByID(context.Background(), invID)
	require.NoError(t, err)
	require.NotNil(t, inv)
	return inv.Status
}

// ---------------------------------------------------------------------------
// AddMember
// ---------------------------------------------------------------------------

func TestT04AddMemberSingleSpace(t *testing.T) {
	t.Run("active_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.memberSvc.AddMember(
			context.Background(), "u", 102, types.TenantRoleViewer, nil)
		require.Error(t, err,
			"a non-super-admin already active in 101 must not join 102")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102),
			"the rejected AddMember must not create a membership row")
		require.Len(t, t04BelongingMembers(t, env, "u"), 1)
	})

	t.Run("invited_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		t04SeedInvitedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.memberSvc.AddMember(
			context.Background(), "u", 102, types.TenantRoleViewer, nil)
		require.Error(t, err,
			"an invited membership already counts as belonging to a space (AC-18)")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102))
		require.Len(t, t04BelongingMembers(t, env, "u"), 1)
	})

	for _, tc := range []struct {
		name   string
		mutate func(*types.User)
	}{
		{
			name:   "system_admin_is_exempt",
			mutate: func(u *types.User) { u.IsSystemAdmin = true },
		},
		{
			name:   "can_access_all_tenants_is_exempt",
			mutate: func(u *types.User) { u.CanAccessAllTenants = true },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			mutate := tc.mutate
			omOwnerSeedUser(t, env, "u", func(u *types.User) {
				u.TenantID = 101
				mutate(u)
			})
			omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

			_, err := env.memberSvc.AddMember(
				context.Background(), "u", 102, types.TenantRoleViewer, nil)
			require.NoError(t, err,
				"super-admins are exempt from the single-space rule (AC-17)")
			require.NotNil(t, omOwnerLoadMember(t, env, "u", 102))
			require.Len(t, t04BelongingMembers(t, env, "u"), 2)
			require.EqualValues(t, 101, t04LoadUser(t, env, "u").TenantID,
				"adding a second space must not move the existing home tenant")
		})
	}

	t.Run("same_tenant_duplicate_keeps_existing_error", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.memberSvc.AddMember(
			context.Background(), "u", 101, types.TenantRoleAdmin, nil)
		require.True(t, errors.Is(err, ErrMembershipAlreadyExists),
			"duplicate membership in the same tenant must keep the existing error, got %v", err)
	})

	t.Run("writes_home_tenant_when_empty", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		require.EqualValues(t, 0, t04LoadUser(t, env, "u").TenantID)

		_, err := env.memberSvc.AddMember(
			context.Background(), "u", 101, types.TenantRoleViewer, nil)
		require.NoError(t, err)
		require.EqualValues(t, 101, t04LoadUser(t, env, "u").TenantID,
			"a successful membership write must adopt the space when users.tenant_id is 0 (AC-19)")
	})
}

// ---------------------------------------------------------------------------
// EnsureOwner
// ---------------------------------------------------------------------------

func TestT04EnsureOwnerSingleSpace(t *testing.T) {
	t.Run("active_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.memberSvc.EnsureOwner(context.Background(), "u", 102)
		require.Error(t, err,
			"EnsureOwner must not give a second space to a non-super-admin")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102))
		owners, err := env.memberRepo.CountActiveOwners(context.Background(), 102)
		require.NoError(t, err)
		require.EqualValues(t, 0, owners, "the rejected space must not gain an owner")
	})

	t.Run("invited_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		t04SeedInvitedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.memberSvc.EnsureOwner(context.Background(), "u", 102)
		require.Error(t, err,
			"an invited membership already counts as belonging to a space")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102))
	})

	for _, tc := range []struct {
		name   string
		mutate func(*types.User)
	}{
		{
			name:   "system_admin_is_exempt",
			mutate: func(u *types.User) { u.IsSystemAdmin = true },
		},
		{
			name:   "can_access_all_tenants_is_exempt",
			mutate: func(u *types.User) { u.CanAccessAllTenants = true },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			omOwnerSeedUser(t, env, "u", tc.mutate)
			omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

			member, err := env.memberSvc.EnsureOwner(context.Background(), "u", 102)
			require.NoError(t, err, "super-admins are exempt (AC-17)")
			require.NotNil(t, member)
			require.Equal(t, types.TenantRoleOwner, member.Role)
			require.EqualValues(t, 102, member.TenantID)
		})
	}

	t.Run("tenantless_user_becomes_owner_and_adopts_home", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)

		member, err := env.memberSvc.EnsureOwner(context.Background(), "u", 101)
		require.NoError(t, err)
		require.NotNil(t, member)
		require.Equal(t, types.TenantRoleOwner, member.Role)
		require.EqualValues(t, 101, t04LoadUser(t, env, "u").TenantID,
			"EnsureOwner must write users.tenant_id when it is empty (AC-19)")
	})

	t.Run("idempotent_for_existing_member_in_same_space", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleAdmin)

		member, err := env.memberSvc.EnsureOwner(context.Background(), "u", 101)
		require.NoError(t, err)
		require.NotNil(t, member)
		require.Equal(t, types.TenantRoleAdmin, member.Role,
			"an existing same-space membership is returned unchanged")
	})
}

// ---------------------------------------------------------------------------
// Create invitation（AC-16：创建时也要校验归属）
// ---------------------------------------------------------------------------

func TestT04CreateInvitationSingleSpace(t *testing.T) {
	t.Run("active_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.inviteSvc.Create(
			context.Background(), 102, "u", types.TenantRoleViewer, nil, "")
		require.Error(t, err,
			"inviting a user who already belongs to another space must be rejected (AC-16)")
		inv, err := env.inviteRepo.GetPendingByPair(context.Background(), 102, "u")
		require.NoError(t, err)
		require.Nil(t, inv, "the rejected invitation must not be persisted")
	})

	t.Run("invited_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		t04SeedInvitedMembership(t, env, "u", 101, types.TenantRoleViewer)

		_, err := env.inviteSvc.Create(
			context.Background(), 102, "u", types.TenantRoleViewer, nil, "")
		require.Error(t, err,
			"an invited membership already counts as belonging to a space (AC-16/AC-18)")
		inv, err := env.inviteRepo.GetPendingByPair(context.Background(), 102, "u")
		require.NoError(t, err)
		require.Nil(t, inv)
	})

	t.Run("system_admin_is_exempt", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", func(u *types.User) { u.IsSystemAdmin = true })
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		inv, err := env.inviteSvc.Create(
			context.Background(), 102, "u", types.TenantRoleViewer, nil, "")
		require.NoError(t, err, "super-admins are exempt (AC-17)")
		require.NotNil(t, inv)
	})

	t.Run("suspended_member_can_be_invited_elsewhere", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
			UserID:   "u",
			TenantID: 101,
			Role:     types.TenantRoleViewer,
			Status:   types.TenantMemberStatusSuspended,
			JoinedAt: time.Now(),
		}))

		inv, err := env.inviteSvc.Create(
			context.Background(), 102, "u", types.TenantRoleViewer, nil, "")
		require.NoError(t, err,
			"a suspended membership does not count as belonging to a space (AC-18)")
		require.NotNil(t, inv)
	})
}

// ---------------------------------------------------------------------------
// Accept / AcceptByToken
// ---------------------------------------------------------------------------

func TestT04AcceptInvitationSingleSpace(t *testing.T) {
	t.Run("active_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)
		inv := t04SeedInvitation(t, env, 102, "u", types.TenantRoleViewer)

		_, err := env.inviteSvc.Accept(context.Background(), inv.ID, "u")
		require.Error(t, err,
			"a user active in 101 must not accept a 102 invitation")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102),
			"the rejected accept must not write a membership row")
		require.Equal(t, types.TenantInvitationStatusPending,
			t04InvitationStatus(t, env, inv.ID),
			"the invitation must stay pending when the membership write is rejected")
		require.Len(t, t04BelongingMembers(t, env, "u"), 1)
		require.EqualValues(t, 101, t04LoadUser(t, env, "u").TenantID)
	})

	t.Run("second_pending_invitation_is_rejected_after_first", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		first := t04SeedInvitation(t, env, 101, "u", types.TenantRoleViewer)
		second := t04SeedInvitation(t, env, 102, "u", types.TenantRoleAdmin)

		_, err := env.inviteSvc.Accept(context.Background(), first.ID, "u")
		require.NoError(t, err)
		require.EqualValues(t, 101, t04LoadUser(t, env, "u").TenantID,
			"accepting the first invitation must adopt the new home tenant (AC-19)")

		_, err = env.inviteSvc.Accept(context.Background(), second.ID, "u")
		require.Error(t, err,
			"accepting a second space must be rejected once the user belongs to the first")
		require.Nil(t, omOwnerLoadMember(t, env, "u", 102))
		require.Equal(t, types.TenantInvitationStatusPending,
			t04InvitationStatus(t, env, second.ID))
		require.Len(t, t04BelongingMembers(t, env, "u"), 1)
	})

	for _, tc := range []struct {
		name   string
		mutate func(*types.User)
	}{
		{
			name:   "system_admin_accepts_both",
			mutate: func(u *types.User) { u.IsSystemAdmin = true },
		},
		{
			name:   "can_access_all_tenants_accepts_both",
			mutate: func(u *types.User) { u.CanAccessAllTenants = true },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			omOwnerSeedUser(t, env, "u", tc.mutate)
			first := t04SeedInvitation(t, env, 101, "u", types.TenantRoleViewer)
			second := t04SeedInvitation(t, env, 102, "u", types.TenantRoleAdmin)

			_, err := env.inviteSvc.Accept(context.Background(), first.ID, "u")
			require.NoError(t, err)
			_, err = env.inviteSvc.Accept(context.Background(), second.ID, "u")
			require.NoError(t, err, "super-admins are exempt (AC-17)")
			require.Len(t, t04BelongingMembers(t, env, "u"), 2)
		})
	}
}

func TestT04AcceptByTokenSingleSpace(t *testing.T) {
	t.Run("active_member_in_other_space_is_rejected", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		// 分享链接的 token 查询带 tenants EXISTS 过滤，必须先建真实空间。
		first := omOwnerSeedTenant(t, env, "空间A")
		second := omOwnerSeedTenant(t, env, "空间B")
		omOwnerSeedUser(t, env, "u", nil)
		omOwnerSeedMembership(t, env, "u", first.ID, types.TenantRoleViewer)
		_, token, err := env.inviteSvc.CreateShareLink(
			context.Background(), second.ID, types.TenantRoleViewer, nil, "")
		require.NoError(t, err)

		_, err = env.inviteSvc.AcceptByToken(context.Background(), token, "u")
		require.Error(t, err,
			"a user active in one space must not join another through a share link")
		require.Nil(t, omOwnerLoadMember(t, env, "u", second.ID))
		require.Len(t, t04BelongingMembers(t, env, "u"), 1)
	})

	t.Run("system_admin_is_exempt", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		first := omOwnerSeedTenant(t, env, "空间A")
		second := omOwnerSeedTenant(t, env, "空间B")
		omOwnerSeedUser(t, env, "u", func(u *types.User) { u.IsSystemAdmin = true })
		omOwnerSeedMembership(t, env, "u", first.ID, types.TenantRoleViewer)
		_, token, err := env.inviteSvc.CreateShareLink(
			context.Background(), second.ID, types.TenantRoleViewer, nil, "")
		require.NoError(t, err)

		_, err = env.inviteSvc.AcceptByToken(context.Background(), token, "u")
		require.NoError(t, err, "super-admins are exempt (AC-17)")
		require.Len(t, t04BelongingMembers(t, env, "u"), 2)
	})
}

// ---------------------------------------------------------------------------
// ProvisionOwnerSpace
// ---------------------------------------------------------------------------

func TestT04ProvisionOwnerSpaceSingleSpace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		role    types.TenantRole
		invited bool
	}{
		{name: "existing_admin_membership", role: types.TenantRoleAdmin},
		{name: "existing_viewer_membership", role: types.TenantRoleViewer},
		{name: "existing_invited_membership", role: types.TenantRoleViewer, invited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
			omOwnerSeedUser(t, env, "target", nil)
			existing := omOwnerSeedTenant(t, env, "已有空间")
			if tc.invited {
				t04SeedInvitedMembership(t, env, "target", existing.ID, tc.role)
			} else {
				omOwnerSeedMembership(t, env, "target", existing.ID, tc.role)
			}

			ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
			_, err := env.svc.ProvisionOwnerSpace(ctx, "target", "第二个空间")
			require.Error(t, err,
				"a non-super-admin already belonging to a space must not be provisioned a second owner space")
			require.EqualValues(t, 1, omOwnerCountTenants(t, env),
				"the owner-space quota is fixed at 1; no second tenant may remain")
			require.Len(t, t04BelongingMembers(t, env, "target"), 1)
		})
	}

	t.Run("owner_membership_reuses_existing_space", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
		omOwnerSeedUser(t, env, "target", nil)
		existing := omOwnerSeedTenant(t, env, "已有空间")
		omOwnerSeedMembership(t, env, "target", existing.ID, types.TenantRoleOwner)

		ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
		space, err := env.svc.ProvisionOwnerSpace(ctx, "target", "不应新建")
		require.NoError(t, err)
		require.NotNil(t, space)
		require.Equal(t, existing.ID, space.ID,
			"an existing owner space is reused idempotently (AC-11)")
		require.EqualValues(t, 1, omOwnerCountTenants(t, env))
		require.Len(t, t04BelongingMembers(t, env, "target"), 1)
	})

	t.Run("tenantless_user_creates_and_reuses", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
		omOwnerSeedUser(t, env, "target", nil)

		ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
		first, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
		require.NoError(t, err)
		require.NotNil(t, first)
		require.EqualValues(t, first.ID, t04LoadUser(t, env, "target").TenantID)

		second, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
		require.NoError(t, err)
		require.Equal(t, first.ID, second.ID, "repeated provisioning must reuse the same space")
		require.EqualValues(t, 1, omOwnerCountTenants(t, env))
		require.Len(t, t04BelongingMembers(t, env, "target"), 1)
	})
}

// ---------------------------------------------------------------------------
// users.tenant_id 维护
// ---------------------------------------------------------------------------

func TestT04HomeTenantMaintenance(t *testing.T) {
	t.Run("remove_home_space_clears_tenant_id", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", func(u *types.User) { u.TenantID = 101 })
		omOwnerSeedMembership(t, env, "u", 101, types.TenantRoleViewer)

		require.NoError(t, env.memberSvc.RemoveMember(context.Background(), "u", 101))
		require.EqualValues(t, 0, t04LoadUser(t, env, "u").TenantID,
			"removing the home space must clear users.tenant_id (AC-19 regression)")
	})

	t.Run("suspended_membership_does_not_count_as_belonging", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
			UserID:   "u",
			TenantID: 101,
			Role:     types.TenantRoleViewer,
			Status:   types.TenantMemberStatusSuspended,
			JoinedAt: time.Now(),
		}))

		_, err := env.memberSvc.AddMember(
			context.Background(), "u", 102, types.TenantRoleViewer, nil)
		require.NoError(t, err,
			"a suspended membership must not block joining another space (AC-18)")
		require.EqualValues(t, 102, t04LoadUser(t, env, "u").TenantID)
	})
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

func TestT04ConcurrentMembershipWrites(t *testing.T) {
	t.Run("concurrent_add_member_one_wins", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)

		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, tenantID := range []uint64{101, 102} {
			wg.Add(1)
			go func(tenantID uint64) {
				defer wg.Done()
				<-start
				_, err := env.memberSvc.AddMember(
					context.Background(), "u", tenantID, types.TenantRoleViewer, nil)
				results <- err
			}(tenantID)
		}
		close(start)
		wg.Wait()
		close(results)

		successes := 0
		for err := range results {
			if err == nil {
				successes++
			}
		}
		require.Equal(t, 1, successes,
			"concurrent AddMember into two spaces must let exactly one succeed (AC-15/AC-20)")
		rows := t04BelongingMembers(t, env, "u")
		require.Len(t, rows, 1,
			"a non-super-admin must end with exactly one active/invited membership")
		require.EqualValues(t, rows[0].TenantID, t04LoadUser(t, env, "u").TenantID,
			"users.tenant_id must point at the winning space")
	})

	t.Run("concurrent_accept_one_wins", func(t *testing.T) {
		env := newOMOwnerEnv(t)
		omOwnerSeedUser(t, env, "u", nil)
		first := t04SeedInvitation(t, env, 101, "u", types.TenantRoleViewer)
		second := t04SeedInvitation(t, env, 102, "u", types.TenantRoleAdmin)

		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, invID := range []uint64{first.ID, second.ID} {
			wg.Add(1)
			go func(invID uint64) {
				defer wg.Done()
				<-start
				_, err := env.inviteSvc.Accept(context.Background(), invID, "u")
				results <- err
			}(invID)
		}
		close(start)
		wg.Wait()
		close(results)

		successes := 0
		for err := range results {
			if err == nil {
				successes++
			}
		}
		require.Equal(t, 1, successes,
			"concurrent acceptance of two invitations must let exactly one succeed (AC-15/AC-20)")
		require.Len(t, t04BelongingMembers(t, env, "u"), 1,
			"concurrent accepts must not create a second membership")
	})
}
