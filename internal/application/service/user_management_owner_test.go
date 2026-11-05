package service

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T04 owner 空间 / co-owner / 空间交接 / 超管设 owner 服务层红阶段用例。
//
// 假定契约（T04 票面 + 02-plan §4.5/§4.6 + 队长派单）：
//   - internal/types/interfaces/user_management.go 增加
//     TransferOwner / ProvisionOwnerSpace / CreateOwnerInvite /
//     CreateCoOwnerAccount，以及 OwnerInviteResult / CreateCoOwnerRequest；
//   - 保持 NewUserManagementService(userRepo, memberRepo, audit) 三参构造不变，
//     跨域依赖通过可选 setter 注入（本轮锁定方法名
//     SetOwnerManagementDeps(tenantService, memberService, invitationService, userService)）；
//   - 权限拒绝/目标保护返回 *apperrors.AppError（403/404/400）。
//
// 业务规则只在本文件断言；handler 只负责把同一份结果/错误映射成 HTTP。

// omOwnerDepsSetter 是 T04 的依赖注入契约。T02/T03 的三参构造保持不变，
// 实现方需要让具体 userManagementService 暴露这个方法。
type omOwnerDepsSetter interface {
	SetOwnerManagementDeps(
		tenantService interfaces.TenantService,
		memberService interfaces.TenantMemberService,
		invitationService interfaces.TenantInvitationService,
		userService interfaces.UserService,
	)
}

type omOwnerEnv struct {
	db         *gorm.DB
	userRepo   interfaces.UserRepository
	memberRepo interfaces.TenantMemberRepository
	tenantRepo interfaces.TenantRepository
	inviteRepo interfaces.TenantInvitationRepository
	audit      interfaces.AuditLogService

	tenantSvc interfaces.TenantService
	memberSvc interfaces.TenantMemberService
	inviteSvc interfaces.TenantInvitationService
	userSvc   interfaces.UserService

	svc interfaces.UserManagementService
}

func newOMOwnerEnv(t *testing.T) *omOwnerEnv {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "owner_management.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantMember{},
		&types.TenantInvitation{},
		&types.AuthToken{},
		&types.AuditLog{},
	))

	userRepo := apprepo.NewUserRepository(db)
	memberRepo := apprepo.NewTenantMemberRepository(db)
	tenantRepo := apprepo.NewTenantRepository(db)
	inviteRepo := apprepo.NewTenantInvitationRepository(db)
	tokenRepo := apprepo.NewAuthTokenRepository(db)
	auditSvc := NewAuditLogService(apprepo.NewAuditLogRepository(db))

	tenantSvc := NewTenantService(tenantRepo, nil, nil)
	memberSvc := NewTenantMemberService(memberRepo, auditSvc, userRepo, tokenRepo)
	inviteSvc := NewTenantInvitationService(inviteRepo, memberSvc, auditSvc)
	userSvc := NewUserService(nil, userRepo, tokenRepo, tenantSvc, memberSvc, nil)

	svc := NewUserManagementService(userRepo, memberRepo, auditSvc)
	setter, ok := svc.(omOwnerDepsSetter)
	require.Truef(t, ok,
		"UserManagementService must expose SetOwnerManagementDeps(tenantService, memberService, invitationService, userService); "+
			"T04 depends on it while keeping the T02 three-arg constructor")
	setter.SetOwnerManagementDeps(tenantSvc, memberSvc, inviteSvc, userSvc)

	return &omOwnerEnv{
		db:         db,
		userRepo:   userRepo,
		memberRepo: memberRepo,
		tenantRepo: tenantRepo,
		inviteRepo: inviteRepo,
		audit:      auditSvc,
		tenantSvc:  tenantSvc,
		memberSvc:  memberSvc,
		inviteSvc:  inviteSvc,
		userSvc:    userSvc,
		svc:        svc,
	}
}

func omOwnerSeedUser(t *testing.T, env *omOwnerEnv, id string, mutate func(*types.User)) {
	t.Helper()
	user := &types.User{
		ID:           id,
		Username:     id,
		Email:        id + "@example.com",
		PasswordHash: "hash-" + id,
		IsActive:     true,
	}
	if mutate != nil {
		mutate(user)
	}
	require.NoError(t, env.userRepo.CreateUser(context.Background(), user))
}

func omOwnerSeedTenant(t *testing.T, env *omOwnerEnv, name string) *types.Tenant {
	t.Helper()
	tenant := &types.Tenant{Name: name, Status: "active"}
	require.NoError(t, env.tenantRepo.CreateTenant(context.Background(), tenant))
	return tenant
}

func omOwnerSeedMembership(
	t *testing.T, env *omOwnerEnv, userID string, tenantID uint64, role types.TenantRole,
) *types.TenantMember {
	t.Helper()
	member := &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: time.Now(),
	}
	require.NoError(t, env.memberRepo.Create(context.Background(), member))
	return member
}

func omOwnerAuditsByAction(
	t *testing.T, env *omOwnerEnv, action types.AuditAction,
) []types.AuditLog {
	t.Helper()
	var rows []types.AuditLog
	require.NoError(t, env.db.
		Where("action = ?", string(action)).
		Order("id ASC").
		Find(&rows).Error)
	return rows
}

func omOwnerCountTenants(t *testing.T, env *omOwnerEnv) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.Tenant{}).Count(&n).Error)
	return n
}

func omOwnerCountUsers(t *testing.T, env *omOwnerEnv) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.User{}).Count(&n).Error)
	return n
}

func omOwnerRequireAppStatus(t *testing.T, err error, status int) {
	t.Helper()
	umRequireHTTPStatus(t, err, status)
}

func omOwnerLoadMember(
	t *testing.T, env *omOwnerEnv, userID string, tenantID uint64,
) *types.TenantMember {
	t.Helper()
	member, err := env.memberRepo.Get(context.Background(), userID, tenantID)
	require.NoError(t, err)
	return member
}

// ---------------------------------------------------------------------------
// ProvisionOwnerSpace
// ---------------------------------------------------------------------------

// AC-9 / AC-11 / T04-1：超管为无空间账号创建独立空间，目标成为唯一 owner，
// 超管不是该空间 owner，写 system.owner_provisioned 审计。
func TestOwnerProvisionOwnerSpaceCreatesIndependentSpace(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	space, err := env.svc.ProvisionOwnerSpace(ctx, "target", "目标独立空间")
	require.NoError(t, err)
	require.NotNil(t, space)
	require.NotZero(t, space.ID)
	require.Equal(t, "目标独立空间", space.Name)

	member := omOwnerLoadMember(t, env, "target", space.ID)
	require.NotNil(t, member, "target must become a member of the provisioned space")
	require.Equal(t, types.TenantRoleOwner, member.Role, "target must be the owner of its own space")
	require.Equal(t, types.TenantMemberStatusActive, member.Status)

	require.Nil(t, omOwnerLoadMember(t, env, "sysadmin", space.ID),
		"the system admin must NOT become an owner/member of the target's space")

	owners, err := env.memberRepo.CountActiveOwners(context.Background(), space.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, owners, "the provisioned space must have exactly one owner")

	targetUser, err := env.userRepo.GetUserByID(context.Background(), "target")
	require.NoError(t, err)
	require.NotNil(t, targetUser)
	require.Equal(t, space.ID, targetUser.TenantID,
		"a tenantless target must adopt the new space as its home tenant (02-plan §4.6)")

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("system.owner_provisioned"))
	require.Len(t, audits, 1, "provisioning must write exactly one system.owner_provisioned audit")
	require.Equal(t, space.ID, audits[0].TenantID)
	require.Equal(t, "target", audits[0].TargetUserID)
	require.Equal(t, "sysadmin", audits[0].ActorUserID)
	require.Equal(t, types.AuditOutcomeSuccess, audits[0].Outcome)

	require.EqualValues(t, 1, omOwnerCountTenants(t, env))
}

// AC-11 / T04-1：已有独立空间时复用，幂等，不重复建租户/成员。
func TestOwnerProvisionOwnerSpaceReusesExistingSpace(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", func(u *types.User) { u.TenantID = 0 })
	existing := omOwnerSeedTenant(t, env, "已有空间")
	omOwnerSeedMembership(t, env, "target", existing.ID, types.TenantRoleOwner)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	space, err := env.svc.ProvisionOwnerSpace(ctx, "target", "不应新建")
	require.NoError(t, err)
	require.NotNil(t, space)
	require.Equal(t, existing.ID, space.ID, "an existing owner space must be reused")
	require.EqualValues(t, 1, omOwnerCountTenants(t, env), "must not create a second tenant")

	members, err := env.memberRepo.ListByTenant(context.Background(), existing.ID)
	require.NoError(t, err)
	require.Len(t, members, 1, "must not duplicate the owner membership")
}

// T04-1 幂等：连续两次调用返回同一空间。
func TestOwnerProvisionOwnerSpaceIsIdempotent(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	first, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
	require.NoError(t, err)
	second, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.EqualValues(t, 1, omOwnerCountTenants(t, env))

	members, err := env.memberRepo.ListByTenant(context.Background(), first.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
}

// T04-1：未指定空间名时仍创建有名字的空间。
func TestOwnerProvisionOwnerSpaceFallsBackToDefaultName(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "target", nil)

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	space, err := env.svc.ProvisionOwnerSpace(ctx, "target", "")
	require.NoError(t, err)
	require.NotNil(t, space)
	require.NotEmpty(t, space.Name, "a default space name must be generated")
}

// T04-1：目标账号不存在 → 404。
func TestOwnerProvisionOwnerSpaceTargetNotFound(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	_, err := env.svc.ProvisionOwnerSpace(ctx, "missing", "空间")
	omOwnerRequireAppStatus(t, err, http.StatusNotFound)
}

// AC-38 服务层自证：非超管不能设 owner 空间 → 403。
func TestOwnerProvisionOwnerSpaceRejectsNonSystemAdmin(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "target", nil)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	_, err := env.svc.ProvisionOwnerSpace(ctx, "target", "空间")
	omOwnerRequireAppStatus(t, err, http.StatusForbidden)
	require.EqualValues(t, 1, omOwnerCountTenants(t, env), "rejection must not create a tenant")
}

// ---------------------------------------------------------------------------
// TransferOwner
// ---------------------------------------------------------------------------

// AC-13 / AC-20 / T04-4：owner 把空间交接给自己指定的成员；源降为 admin、
// 目标升为 owner、至少保留 1 名 owner，写 rbac.owner_transferred 审计。
func TestOwnerTransferOwnerByOwner(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "successor", nil)
	omOwnerSeedMembership(t, env, "successor", tenant.ID, types.TenantRoleAdmin)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.TransferOwner(ctx, tenant.ID, "owner1", "successor"))

	require.Equal(t, types.TenantRoleOwner, omOwnerLoadMember(t, env, "successor", tenant.ID).Role)
	require.Equal(t, types.TenantRoleAdmin, omOwnerLoadMember(t, env, "owner1", tenant.ID).Role,
		"the transferring owner must be demoted to admin")

	owners, err := env.memberRepo.CountActiveOwners(context.Background(), tenant.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, owners, int64(1), "a workspace must never be left without an owner")

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("rbac.owner_transferred"))
	require.Len(t, audits, 1)
	require.Equal(t, tenant.ID, audits[0].TenantID)
	require.Equal(t, "owner1", audits[0].ActorUserID)
	require.Equal(t, "successor", audits[0].TargetUserID)
	require.Equal(t, types.AuditOutcomeSuccess, audits[0].Outcome)
}

// T04-4：owner 只能交接自己，不能把别人的 owner 身份传出去 → 403。
func TestOwnerTransferOwnerRejectsTransferringSomeoneElse(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "owner2", nil)
	omOwnerSeedMembership(t, env, "owner2", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "target", nil)
	omOwnerSeedMembership(t, env, "target", tenant.ID, types.TenantRoleViewer)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenant.ID, "owner2", "target")
	omOwnerRequireAppStatus(t, err, http.StatusForbidden)

	require.Equal(t, types.TenantRoleOwner, omOwnerLoadMember(t, env, "owner2", tenant.ID).Role)
	require.Equal(t, types.TenantRoleViewer, omOwnerLoadMember(t, env, "target", tenant.ID).Role)
}

// T04-4：系统管理员可交接任意 owner（与工作区角色无关）。
func TestOwnerTransferOwnerBySystemAdmin(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "successor", nil)
	omOwnerSeedMembership(t, env, "successor", tenant.ID, types.TenantRoleContributor)

	ctx := umCallerCtx("sysadmin", tenant.ID, types.TenantRoleViewer, true)
	require.NoError(t, env.svc.TransferOwner(ctx, tenant.ID, "owner1", "successor"))

	require.Equal(t, types.TenantRoleOwner, omOwnerLoadMember(t, env, "successor", tenant.ID).Role)
	require.Equal(t, types.TenantRoleAdmin, omOwnerLoadMember(t, env, "owner1", tenant.ID).Role)

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("rbac.owner_transferred"))
	require.Len(t, audits, 1)
	require.Equal(t, "sysadmin", audits[0].ActorUserID)
	require.Equal(t, "successor", audits[0].TargetUserID)
}

// T04-4：目标必须是本空间 active 成员，否则 404。
func TestOwnerTransferOwnerTargetNotMember(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "outsider", nil)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenant.ID, "owner1", "outsider")
	omOwnerRequireAppStatus(t, err, http.StatusNotFound)
	require.Equal(t, types.TenantRoleOwner, omOwnerLoadMember(t, env, "owner1", tenant.ID).Role)
}

// AC-38 / T04-4：非 owner 调用者（即使自称 owner 但不是本空间成员）→ 403。
func TestOwnerTransferOwnerRejectsNonOwnerCaller(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "target", nil)
	omOwnerSeedMembership(t, env, "target", tenant.ID, types.TenantRoleAdmin)

	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin, types.TenantRoleContributor, types.TenantRoleViewer,
	} {
		t.Run(string(role), func(t *testing.T) {
			omOwnerSeedUser(t, env, "caller-"+string(role), nil)
			omOwnerSeedMembership(t, env, "caller-"+string(role), tenant.ID, role)
			ctx := umCallerCtx("caller-"+string(role), tenant.ID, role, false)
			err := env.svc.TransferOwner(ctx, tenant.ID, "owner1", "target")
			omOwnerRequireAppStatus(t, err, http.StatusForbidden)
		})
	}

	// 自称 owner 但实际不是本空间成员：同样必须拒绝。
	ctx := umCallerCtx("ghost-owner", tenant.ID, types.TenantRoleOwner, false)
	err := env.svc.TransferOwner(ctx, tenant.ID, "owner1", "target")
	omOwnerRequireAppStatus(t, err, http.StatusForbidden)
	require.Equal(t, types.TenantRoleOwner, omOwnerLoadMember(t, env, "owner1", tenant.ID).Role)
}

// ---------------------------------------------------------------------------
// CreateCoOwnerAccount
// ---------------------------------------------------------------------------

func omOwnerPasswordPtr() *string {
	pwd := "Passw0rd!123"
	return &pwd
}

// AC-9 / AC-10 / T04-3：owner 新建 co-owner 账号；账号 tenant_id=0，
// 只属于当前空间，角色 owner，写 rbac.member_added 审计。
func TestOwnerCreateCoOwnerAccountCreatesTenantlessOwner(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	user, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, &interfaces.CreateCoOwnerRequest{
		Username: "coowner",
		Email:    "coowner@example.com",
		Password: omOwnerPasswordPtr(),
	})
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NotEmpty(t, user.ID)
	require.Zero(t, user.TenantID, "AC-10: an account created by an owner must have users.tenant_id = 0")
	require.True(t, user.IsActive)

	member := omOwnerLoadMember(t, env, user.ID, tenant.ID)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleOwner, member.Role)
	require.Equal(t, types.TenantMemberStatusActive, member.Status)

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("rbac.member_added"))
	require.NotEmpty(t, audits, "adding a co-owner must write rbac.member_added")
	found := false
	for _, row := range audits {
		if row.TenantID == tenant.ID && row.TargetUserID == user.ID {
			found = true
		}
	}
	require.True(t, found, "rbac.member_added must target the new co-owner in this tenant")
}

// T04-3：重复邮箱 → 409，且不产生第二个账号/成员关系。
func TestOwnerCreateCoOwnerAccountRejectsDuplicateEmail(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "existing", func(u *types.User) { u.Email = "taken@example.com" })

	beforeUsers := omOwnerCountUsers(t, env)
	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	_, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, &interfaces.CreateCoOwnerRequest{
		Username: "another",
		Email:    "taken@example.com",
		Password: omOwnerPasswordPtr(),
	})
	omOwnerRequireAppStatus(t, err, http.StatusConflict)
	require.Equal(t, beforeUsers, omOwnerCountUsers(t, env), "duplicate identity must not create a user")

	members, err := env.memberRepo.ListByTenant(context.Background(), tenant.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
}

// T04-3：重复用户名 → 409。
func TestOwnerCreateCoOwnerAccountRejectsDuplicateUsername(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "existing", nil) // username = "existing"

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	_, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, &interfaces.CreateCoOwnerRequest{
		Username: "existing",
		Email:    "brand-new@example.com",
		Password: omOwnerPasswordPtr(),
	})
	omOwnerRequireAppStatus(t, err, http.StatusConflict)
}

// T04-3：参数缺失 → 400。
func TestOwnerCreateCoOwnerAccountValidatesRequest(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)

	for name, req := range map[string]*interfaces.CreateCoOwnerRequest{
		"missing_username": {Email: "x@example.com", Password: omOwnerPasswordPtr()},
		"missing_email":    {Username: "coowner", Password: omOwnerPasswordPtr()},
		"nil_request":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, req)
			omOwnerRequireAppStatus(t, err, http.StatusBadRequest)
		})
	}
}

// AC-38 / T04-3：非 owner（admin/contributor/viewer）不能新建 co-owner → 403。
func TestOwnerCreateCoOwnerAccountRejectsNonOwner(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")

	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin, types.TenantRoleContributor, types.TenantRoleViewer,
	} {
		t.Run(string(role), func(t *testing.T) {
			callerID := "caller-" + string(role)
			omOwnerSeedUser(t, env, callerID, nil)
			omOwnerSeedMembership(t, env, callerID, tenant.ID, role)
			ctx := umCallerCtx(callerID, tenant.ID, role, false)
			before := omOwnerCountUsers(t, env)
			_, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, &interfaces.CreateCoOwnerRequest{
				Username: "co-" + string(role),
				Email:    "co-" + string(role) + "@example.com",
				Password: omOwnerPasswordPtr(),
			})
			omOwnerRequireAppStatus(t, err, http.StatusForbidden)
			require.Equal(t, before, omOwnerCountUsers(t, env), "rejected callers must not create users")
		})
	}
}

// omFailingMemberService 让 AddMember/EnsureOwner 必然失败，用于验证回滚。
type omFailingMemberService struct {
	interfaces.TenantMemberService
}

func (s *omFailingMemberService) AddMember(
	context.Context, string, uint64, types.TenantRole, *string,
) (*types.TenantMember, error) {
	return nil, errors.New("injected membership failure")
}

func (s *omFailingMemberService) EnsureOwner(
	context.Context, string, uint64,
) (*types.TenantMember, error) {
	return nil, errors.New("injected membership failure")
}

// T04-3：入组失败必须回滚刚建的账号（硬删或软删都算），不留孤儿账号。
func TestOwnerCreateCoOwnerAccountRollsBackWhenMembershipFails(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)

	setter := env.svc.(omOwnerDepsSetter)
	setter.SetOwnerManagementDeps(
		env.tenantSvc,
		&omFailingMemberService{TenantMemberService: env.memberSvc},
		env.inviteSvc,
		env.userSvc,
	)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	_, err := env.svc.CreateCoOwnerAccount(ctx, tenant.ID, &interfaces.CreateCoOwnerRequest{
		Username: "rollback",
		Email:    "rollback@example.com",
		Password: omOwnerPasswordPtr(),
	})
	require.Error(t, err, "membership failure must surface as an error")

	// 仓库对不存在的行返回 (nil, ErrUserNotFound)，因此回滚成功时 lookupErr
	// 必然非空；这里按「查不到即已删除」断言，避免要求 NoError + Nil 的矛盾组合。
	orphan, lookupErr := env.userRepo.GetUserByEmail(context.Background(), "rollback@example.com")
	require.ErrorIs(t, lookupErr, apprepo.ErrUserNotFound,
		"a failed membership must roll back the newly created user")
	require.Nil(t, orphan)

	members, listErr := env.memberRepo.ListByTenant(context.Background(), tenant.ID)
	require.NoError(t, listErr)
	require.Len(t, members, 1, "no membership may be left behind")
}

// ---------------------------------------------------------------------------
// CreateOwnerInvite
// ---------------------------------------------------------------------------

// AC-9 / AC-11 / T04-2：创建 pending 租户 + role=owner 的 share link，
// 邀请时只建租户与 token，不预建成员关系，写 system.owner_invite_created。
func TestOwnerCreateOwnerInviteCreatesPendingTenantAndShareLink(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	before := time.Now()
	result, err := env.svc.CreateOwnerInvite(ctx, "invitee@example.com", "邀请空间", 7)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotZero(t, result.TenantID)
	require.NotZero(t, result.InvitationID)
	require.NotEmpty(t, result.Token, "the invite URL token must be returned")

	require.WithinDuration(t, before.Add(7*24*time.Hour), result.ExpiresAt, 2*time.Hour,
		"default owner invite must expire in 7 days")

	tenant, err := env.tenantRepo.GetTenantByID(context.Background(), result.TenantID)
	require.NoError(t, err)
	require.NotNil(t, tenant)
	require.Equal(t, "pending", tenant.Status, "owner invite must create a pending tenant")
	require.Equal(t, "邀请空间", tenant.Name)

	inv, err := env.inviteRepo.GetByID(context.Background(), result.InvitationID)
	require.NoError(t, err)
	require.NotNil(t, inv)
	require.Equal(t, result.TenantID, inv.TenantID)
	require.Equal(t, types.TenantRoleOwner, inv.Role, "the share link must grant the owner role")
	require.Equal(t, types.TenantInvitationStatusPending, inv.Status)
	require.Equal(t, result.Token, inv.Token)

	members, err := env.memberRepo.ListByTenant(context.Background(), result.TenantID)
	require.NoError(t, err)
	require.Empty(t, members,
		"T04-2: inviting must NOT pre-create any tenant_members row; membership is created on accept")

	audits := omOwnerAuditsByAction(t, env, types.AuditAction("system.owner_invite_created"))
	require.Len(t, audits, 1)
	require.Equal(t, result.TenantID, audits[0].TenantID)
	require.Equal(t, "sysadmin", audits[0].ActorUserID)
	require.Equal(t, types.AuditOutcomeSuccess, audits[0].Outcome)
}

// T04-2：自定义有效期（例如 3 天）生效。
func TestOwnerCreateOwnerInviteCustomExpiry(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	before := time.Now()
	result, err := env.svc.CreateOwnerInvite(ctx, "", "空间", 3)
	require.NoError(t, err)
	require.WithinDuration(t, before.Add(3*24*time.Hour), result.ExpiresAt, 2*time.Hour)
}

// T04-2：expiresDays <= 0 时回落到 7 天。
func TestOwnerCreateOwnerInviteDefaultsToSevenDays(t *testing.T) {
	env := newOMOwnerEnv(t)
	omOwnerSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })

	ctx := umCallerCtx("sysadmin", 0, types.TenantRoleViewer, true)
	before := time.Now()
	result, err := env.svc.CreateOwnerInvite(ctx, "", "空间", 0)
	require.NoError(t, err)
	require.WithinDuration(t, before.Add(7*24*time.Hour), result.ExpiresAt, 2*time.Hour)
}

// AC-38 服务层自证：非超管不能生成 owner 邀请 → 403，且不建租户。
func TestOwnerCreateOwnerInviteRejectsNonSystemAdmin(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	_, err := env.svc.CreateOwnerInvite(ctx, "x@example.com", "空间", 7)
	omOwnerRequireAppStatus(t, err, http.StatusForbidden)
	require.EqualValues(t, 1, omOwnerCountTenants(t, env), "rejection must not create a tenant")
}

// ---------------------------------------------------------------------------
// 现有邀请链路的 owner 角色回归
// ---------------------------------------------------------------------------

// AC-9 / AC-30b：现有 share link 支持 role=owner；接受前无成员关系，
// 接受时创建 owner 成员；重复接受幂等，不产生重复成员。
func TestOwnerExistingShareLinkSupportsOwnerRole(t *testing.T) {
	env := newOMOwnerEnv(t)
	tenant := omOwnerSeedTenant(t, env, "A")
	omOwnerSeedUser(t, env, "owner1", nil)
	omOwnerSeedMembership(t, env, "owner1", tenant.ID, types.TenantRoleOwner)
	omOwnerSeedUser(t, env, "invitee", nil)

	ctx := umCallerCtx("owner1", tenant.ID, types.TenantRoleOwner, false)
	inviter := "owner1"
	inv, token, err := env.inviteSvc.CreateShareLink(
		ctx, tenant.ID, types.TenantRoleOwner, &inviter, "",
	)
	require.NoError(t, err)
	require.NotNil(t, inv)
	require.NotEmpty(t, token)
	require.Equal(t, types.TenantRoleOwner, inv.Role)

	require.Nil(t, omOwnerLoadMember(t, env, "invitee", tenant.ID),
		"share links must not pre-create membership before accept")

	member, err := env.inviteSvc.AcceptByToken(ctx, token, "invitee")
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleOwner, member.Role)

	again, err := env.inviteSvc.AcceptByToken(ctx, token, "invitee")
	require.NoError(t, err)
	require.NotNil(t, again)
	require.Equal(t, member.ID, again.ID, "AC-30b: repeated accept must be idempotent")

	members, err := env.memberRepo.ListByTenant(context.Background(), tenant.ID)
	require.NoError(t, err)
	require.Len(t, members, 2, "owner1 + invitee, no duplicate rows")
}
