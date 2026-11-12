package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// CP3-FIX3 针对性回归（Codex 四组复核，2026-11-12）
//
// C1：挂起成员重新加入恢复旧行；已属其他空间时拒绝恢复；软删行走新建；
//     active/invited 保持「已是成员」幂等错误。
// C3：ProvisionOwnerSpace 回填 home 只做列级条件更新，不覆盖并发密码修改。
// C4：解封事务内复核目标角色，预检 viewer 但事务内已升 admin → 409，
//     且 is_active 未写入。

const (
	cp3Fix3TenantA uint64 = 301
	cp3Fix3TenantB uint64 = 302
)

// cp3Fix3SeedSuspended 造一条挂起的旧成员关系：角色/加入时间/邀请人都
// 是可辨识的旧值，恢复后便于断言全部被本次请求覆盖。
func cp3Fix3SeedSuspended(
	t *testing.T,
	env *omOwnerEnv,
	userID string,
	tenantID uint64,
	role types.TenantRole,
	invitedBy *string,
) *types.TenantMember {
	t.Helper()
	member := &types.TenantMember{
		UserID:    userID,
		TenantID:  tenantID,
		Role:      role,
		Status:    types.TenantMemberStatusSuspended,
		InvitedBy: invitedBy,
		JoinedAt:  time.Now().Add(-48 * time.Hour),
	}
	require.NoError(t, env.memberRepo.Create(context.Background(), member))
	require.NotZero(t, member.ID)
	return member
}

// C1-1：挂起行 + 接受有效邀请 → 恢复同一行（active/本次角色/joined_at/
// invited_by 全部更新），邀请标 accepted，成员可正常读取。
func TestCP3Fix3SuspendedInviteeAcceptRecoversMembership(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "actor-admin", nil)
	omOwnerSeedUser(t, env, "invitee", nil)
	omOwnerSeedMembership(t, env, "actor-admin", cp3Fix3TenantA, types.TenantRoleAdmin)

	oldInviter := "former-inviter"
	seeded := cp3Fix3SeedSuspended(
		t, env, "invitee", cp3Fix3TenantA, types.TenantRoleViewer, &oldInviter)

	inviter := "actor-admin"
	inv, err := env.inviteSvc.Create(
		umCallerCtx("actor-admin", cp3Fix3TenantA, types.TenantRoleAdmin, false),
		cp3Fix3TenantA, "invitee", types.TenantRoleAdmin, &inviter, "")
	require.NoError(t, err, "挂起关系不挡邀请创建（只有 active 才算已是成员）")

	member, err := env.inviteSvc.Accept(
		umCallerCtx("invitee", cp3Fix3TenantA, "", false), inv.ID, "invitee")
	require.NoError(t, err, "接受邀请必须成功恢复挂起的旧关系，而不是假成功")

	require.EqualValues(t, seeded.ID, member.ID, "恢复必须复用旧成员行，不能新建第二行")
	require.Equal(t, types.TenantMemberStatusActive, member.Status)
	require.Equal(t, types.TenantRoleAdmin, member.Role, "角色取本次邀请的角色")
	require.NotNil(t, member.InvitedBy)
	require.Equal(t, "actor-admin", *member.InvitedBy, "invited_by 取本次邀请人")
	require.True(t, member.JoinedAt.After(seeded.JoinedAt),
		"joined_at 必须更新为本次重新加入时间：old=%s new=%s", seeded.JoinedAt, member.JoinedAt)

	stored, err := env.memberRepo.Get(context.Background(), "invitee", cp3Fix3TenantA)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, types.TenantMemberStatusActive, stored.Status)

	// 邀请被标 accepted，且成员可正常读取（不是 suspended 旧行）。
	freshInv, err := env.inviteRepo.GetByID(context.Background(), inv.ID)
	require.NoError(t, err)
	require.NotNil(t, freshInv)
	require.Equal(t, types.TenantInvitationStatusAccepted, freshInv.Status)

	accessible, err := env.memberSvc.GetMembership(context.Background(), "invitee", cp3Fix3TenantA)
	require.NoError(t, err)
	require.NotNil(t, accessible)
	require.Equal(t, types.TenantMemberStatusActive, accessible.Status,
		"接受邀请后成员必须可访问（active），不能停在 suspended")
}

// C1-2：挂起行 + 用户已 active/invited 属于其他空间 → 恢复被拒，
// 不能造出两个 active 空间（AC-15）。旧行保持 suspended。
func TestCP3Fix3SuspendedRecoveryRejectedWhenUserInAnotherSpace(t *testing.T) {
	cases := []struct {
		name       string
		otherState types.TenantMemberStatus
	}{
		{name: "active_in_other_space", otherState: types.TenantMemberStatusActive},
		{name: "invited_to_other_space", otherState: types.TenantMemberStatusInvited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			omOwnerSeedUser(t, env, "invitee", nil)
			seeded := cp3Fix3SeedSuspended(
				t, env, "invitee", cp3Fix3TenantA, types.TenantRoleViewer, nil)
			require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
				UserID:   "invitee",
				TenantID: cp3Fix3TenantB,
				Role:     types.TenantRoleViewer,
				Status:   tc.otherState,
				JoinedAt: time.Now(),
			}))

			_, err := env.memberSvc.AddMember(
				umCallerCtx("invitee", cp3Fix3TenantA, "", false),
				"invitee", cp3Fix3TenantA, types.TenantRoleAdmin, nil)
			require.ErrorIs(t, err, ErrUserAlreadyInSpace,
				"已属于其他空间时恢复挂起行必须被一号一空间守卫拒绝")

			row, getErr := env.memberRepo.Get(context.Background(), "invitee", cp3Fix3TenantA)
			require.NoError(t, getErr)
			require.NotNil(t, row)
			require.EqualValues(t, seeded.ID, row.ID)
			require.Equal(t, types.TenantMemberStatusSuspended, row.Status,
				"被拒绝的恢复不能改动旧行状态")
			require.Equal(t, types.TenantRoleViewer, row.Role)
		})
	}
}

// C1-3：软删（deleted_at）行 + 重新邀请 → 走新建路径（新行 active），
// 与「恢复旧行」区分。旧行保持软删。
func TestCP3Fix3SoftDeletedMembershipRejoinCreatesNewRow(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "rejoiner", nil)
	omOwnerSeedUser(t, env, "actor-admin", nil)
	omOwnerSeedMembership(t, env, "actor-admin", cp3Fix3TenantA, types.TenantRoleAdmin)
	oldMember := omOwnerSeedMembership(
		t, env, "rejoiner", cp3Fix3TenantA, types.TenantRoleViewer)
	require.NoError(t, env.memberRepo.SoftDelete(context.Background(), "rejoiner", cp3Fix3TenantA))

	inviter := "actor-admin"
	inv, err := env.inviteSvc.Create(
		umCallerCtx("actor-admin", cp3Fix3TenantA, types.TenantRoleAdmin, false),
		cp3Fix3TenantA, "rejoiner", types.TenantRoleAdmin, &inviter, "")
	require.NoError(t, err, "软删关系不算 active 成员，重新邀请必须发得出去")
	member, err := env.inviteSvc.Accept(
		umCallerCtx("rejoiner", cp3Fix3TenantA, "", false), inv.ID, "rejoiner")
	require.NoError(t, err)
	require.NotEqualValues(t, oldMember.ID, member.ID,
		"软删行不能被恢复，重新加入必须新建一行")
	require.Equal(t, types.TenantMemberStatusActive, member.Status)
	require.Equal(t, types.TenantRoleAdmin, member.Role)

	freshInv, err := env.inviteRepo.GetByID(context.Background(), inv.ID)
	require.NoError(t, err)
	require.NotNil(t, freshInv)
	require.Equal(t, types.TenantInvitationStatusAccepted, freshInv.Status)

	var all []types.TenantMember
	require.NoError(t, env.db.Unscoped().
		Where("user_id = ? AND tenant_id = ?", "rejoiner", cp3Fix3TenantA).
		Order("id ASC").Find(&all).Error)
	require.Len(t, all, 2, "软删旧行 + 新建 active 行应同时存在")
	require.True(t, all[0].DeletedAt.Valid, "旧行必须保持软删")
	require.False(t, all[1].DeletedAt.Valid, "新行必须是未删除的 active 行")
	require.EqualValues(t, member.ID, all[1].ID)
}

// C1-4：active / invited 旧行 → 仍返回 ErrMembershipAlreadyExists，
// 邀请侧既有幂等行为不变，不能误走恢复分支。
func TestCP3Fix3ActiveOrInvitedMembershipStillAlreadyExists(t *testing.T) {
	cases := []struct {
		name   string
		status types.TenantMemberStatus
	}{
		{name: "active", status: types.TenantMemberStatusActive},
		{name: "invited", status: types.TenantMemberStatusInvited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newOMOwnerEnv(t)
			omOwnerSeedUser(t, env, "member", nil)
			require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
				UserID:   "member",
				TenantID: cp3Fix3TenantA,
				Role:     types.TenantRoleViewer,
				Status:   tc.status,
				JoinedAt: time.Now(),
			}))

			_, err := env.memberSvc.AddMember(
				umCallerCtx("member", cp3Fix3TenantA, "", false),
				"member", cp3Fix3TenantA, types.TenantRoleAdmin, nil)
			require.ErrorIs(t, err, ErrMembershipAlreadyExists,
				"%s 行必须保持『已是成员』语义", tc.name)
		})
	}
}

// cp3Fix3PwdRaceUserRepo 在 adoptHomeTenant 真正写库前执行一次「并发密码
// 重置」，模拟 ProvisionOwnerSpace 读取用户快照之后、回填 home 之前另一个
// 写者改了 password_hash。回填若仍是整行 Save，就会把旧哈希覆盖回去。
type cp3Fix3PwdRaceUserRepo struct {
	interfaces.UserRepository
	onAdopt func(context.Context, string)
}

func (r *cp3Fix3PwdRaceUserRepo) SetTenantIDIfEmpty(
	ctx context.Context, userID string, tenantID uint64,
) (bool, error) {
	if r.onAdopt != nil {
		hook := r.onAdopt
		r.onAdopt = nil
		hook(ctx, userID)
	}
	writer, ok := r.UserRepository.(emptyHomeTenantWriter)
	if !ok {
		return false, errors.New("test: underlying user repository lacks SetTenantIDIfEmpty")
	}
	return writer.SetTenantIDIfEmpty(ctx, userID, tenantID)
}

// C3：ProvisionOwnerSpace 复用已有 owner 空间时由 adoptHomeTenant 首次回填
// home。并发密码修改必须保留，tenant_id 仍要写成功。
func TestCP3Fix3ProvisionAdoptHomeKeepsConcurrentPasswordHash(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target-user", nil)
	tenant := omOwnerSeedTenant(t, env, "Existing Space")
	omOwnerSeedMembership(t, env, "target-user", tenant.ID, types.TenantRoleOwner)

	before, err := env.userRepo.GetUserByID(context.Background(), "target-user")
	require.NoError(t, err)
	require.Zero(t, before.TenantID, "夹具要求目标初始没有 home，回填才有实际写入")

	const newHash = "hash-after-concurrent-reset"
	repo := &cp3Fix3PwdRaceUserRepo{UserRepository: env.userRepo}
	repo.onAdopt = func(ctx context.Context, userID string) {
		u, getErr := env.userRepo.GetUserByID(ctx, userID)
		require.NoError(t, getErr)
		u.PasswordHash = newHash
		require.NoError(t, env.userRepo.UpdateUser(ctx, u),
			"测试夹具：并发密码重置必须落库")
	}

	svc := NewUserManagementService(repo, env.memberRepo, env.audit)
	setter, ok := svc.(omOwnerDepsSetter)
	require.True(t, ok)
	setter.SetOwnerManagementDeps(env.tenantSvc, env.memberSvc, env.inviteSvc, env.userSvc)

	gotTenant, err := svc.ProvisionOwnerSpace(
		umCallerCtx("sysadmin", 0, "", true), "target-user", "")
	require.NoError(t, err)
	require.NotNil(t, gotTenant)
	require.Equal(t, tenant.ID, gotTenant.ID, "已有 owner 空间必须幂等复用")

	stored, err := env.userRepo.GetUserByID(context.Background(), "target-user")
	require.NoError(t, err)
	require.Equal(t, newHash, stored.PasswordHash,
		"回填 home 只能用列级条件更新，不能覆盖并发写入的密码哈希")
	require.Equal(t, tenant.ID, stored.TenantID, "home 回填仍需成功")
}

// cp3Fix3RoleFlipUnbanRepo 在真实解封事务之前把目标角色从 viewer 升为
// admin，精确构造「服务层预检 viewer（保护通过）→ 事务内角色已变」的窗口，
// 同时保留真实仓储的 ErrRoleChanged 与「不写 is_active」实现。
type cp3Fix3RoleFlipUnbanRepo struct {
	interfaces.UserRepository
	memberRepo interfaces.TenantMemberRepository
	flipped    bool
}

func (r *cp3Fix3RoleFlipUnbanRepo) UnbanUserTx(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	actorID string,
	isSystemAdmin bool,
	expectedTargetRole types.TenantRole,
) (*types.User, error) {
	if !r.flipped {
		r.flipped = true
		if err := r.memberRepo.UpdateRole(
			ctx, targetUserID, tenantID, types.TenantRoleAdmin); err != nil {
			return nil, err
		}
	}
	delegate, ok := r.UserRepository.(interface {
		UnbanUserTx(context.Context, uint64, string, string, bool, types.TenantRole) (*types.User, error)
	})
	if !ok {
		return nil, errors.New("test: underlying user repository lacks UnbanUserTx")
	}
	return delegate.UnbanUserTx(
		ctx, tenantID, targetUserID, actorID, isSystemAdmin, expectedTargetRole)
}

// C4：预检 viewer 通过保护，事务内角色已升 admin → ErrRoleChanged→409，
// 且 is_active 不得写入、不得产生成功解封审计。
func TestCP3Fix3UnbanRejectsRoleChangedInsideTransaction(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "actor-owner", nil)
	omOwnerSeedUser(t, env, "target", nil)
	// users.is_active 带 gorm:"default:true"：Create 时传 false 会被列默认值
	// 覆盖（PITFALLS D-1），必须再走仓储显式关闭。
	_, err := env.userRepo.SetUserActive(context.Background(), "target", "fixture", false)
	require.NoError(t, err)
	omOwnerSeedMembership(t, env, "actor-owner", cp3Fix3TenantA, types.TenantRoleOwner)
	omOwnerSeedMembership(t, env, "target", cp3Fix3TenantA, types.TenantRoleViewer)

	// 普通 owner 解封要求最近一次成功 ban 发生在本空间。
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     cp3Fix3TenantA,
		Action:       types.AuditActionMemberBanned,
		Outcome:      types.AuditOutcomeSuccess,
		TargetUserID: "target",
		CreatedAt:    time.Now().Add(-time.Minute),
	}).Error)

	repo := &cp3Fix3RoleFlipUnbanRepo{
		UserRepository: env.userRepo,
		memberRepo:     env.memberRepo,
	}
	svc := NewUserManagementService(repo, env.memberRepo, env.audit)

	_, err = svc.UnbanUser(
		umCallerCtx("actor-owner", cp3Fix3TenantA, types.TenantRoleOwner, false),
		cp3Fix3TenantA, "target", "role race")
	umRequireHTTPStatus(t, err, 409)

	stored, getErr := env.userRepo.GetUserByID(context.Background(), "target")
	require.NoError(t, getErr)
	require.False(t, stored.IsActive, "角色复核失败后不得写入 is_active=true")

	var successAudits int64
	require.NoError(t, env.db.Model(&types.AuditLog{}).
		Where("tenant_id = ? AND action = ? AND outcome = ?",
			cp3Fix3TenantA, types.AuditActionMemberUnbanned, types.AuditOutcomeSuccess).
		Count(&successAudits).Error)
	require.Zero(t, successAudits, "被拒绝的解封不得写成功审计")
}
