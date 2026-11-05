package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T02 封禁/解封服务层红阶段用例（AC-3..7、AC-14..18、AC-33）。
//
// 假定契约（02-plan §4.1–4.3 + 队长派单）：
//   - internal/types/interfaces/user_management.go 定义
//     UserManagementService{BanUser,UnbanUser}；
//   - internal/application/service/user_management.go 提供
//     NewUserManagementService(userRepo, memberRepo, audit)；
//   - 权限拒绝/目标保护/跨租户解封返回 *apperrors.AppError（403），
//     目标不属于本空间返回 404，参数问题返回 400；
//   - 封禁写 rbac.member_banned，解封写 rbac.member_unbanned。
//
// 业务规则只在本文件断言；handler 只负责把同一份错误映射成 HTTP 状态码。

const (
	umTenantA uint64 = 10
	umTenantB uint64 = 20
)

type umTestEnv struct {
	db         *gorm.DB
	userRepo   interfaces.UserRepository
	memberRepo interfaces.TenantMemberRepository
	audit      interfaces.AuditLogService
	svc        interfaces.UserManagementService
}

func newUMTestEnv(t *testing.T) *umTestEnv {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "user_management.db")+"?_busy_timeout=5000"),
		&gorm.Config{DisableForeignKeyConstraintWhenMigrating: true},
	)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(
		&types.User{},
		&types.TenantMember{},
		&types.AuthToken{},
		&types.AuditLog{},
	))

	userRepo := apprepo.NewUserRepository(db)
	memberRepo := apprepo.NewTenantMemberRepository(db)
	auditSvc := NewAuditLogService(apprepo.NewAuditLogRepository(db))
	return &umTestEnv{
		db:         db,
		userRepo:   userRepo,
		memberRepo: memberRepo,
		audit:      auditSvc,
		svc:        NewUserManagementService(userRepo, memberRepo, auditSvc),
	}
}

func umSeedUser(t *testing.T, env *umTestEnv, id string, mutate func(*types.User)) {
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
	wantActive := user.IsActive
	// users.is_active 带 gorm:"default:true"：Create 时字段为 false（零值）
	// 会被 GORM 写成列默认值 true。要真正落库 is_active=false，必须先按
	// 默认创建，再走仓储 SetUserActive 显式关闭。
	user.IsActive = true
	require.NoError(t, env.userRepo.CreateUser(context.Background(), user))
	if !wantActive {
		_, err := env.userRepo.SetUserActive(context.Background(), id, "fixture", false)
		require.NoError(t, err)
	}
}

func umSeedMembership(t *testing.T, env *umTestEnv, userID string, tenantID uint64, role types.TenantRole) {
	t.Helper()
	require.NoError(t, env.memberRepo.Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: time.Now(),
	}))
}

func umSeedTokens(t *testing.T, env *umTestEnv, userID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, env.db.Create(&types.AuthToken{
			ID:        fmt.Sprintf("%s-token-%d", userID, i),
			UserID:    userID,
			Token:     fmt.Sprintf("secret-token-%s-%d", userID, i),
			TokenType: "access_token",
			ExpiresAt: time.Now().Add(time.Hour),
		}).Error)
	}
}

// umCallerCtx 复刻 middleware/auth.go 种进请求上下文的三件套：
// user id / 活动租户 / 工作区角色 / 系统管理员标志。
func umCallerCtx(userID string, tenantID uint64, role types.TenantRole, systemAdmin bool) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, types.UserIDContextKey, userID)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
	ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
	return types.WithCaller(ctx, types.Caller{TenantID: tenantID, UserID: userID, Role: role})
}

func umRequireHTTPStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.Truef(t, errors.As(err, &appErr),
		"service rejection must be *apperrors.AppError so the handler can map HTTP status; got %T: %v", err, err)
	require.Equal(t, status, appErr.HTTPCode, "unexpected status for message: %s", appErr.Message)
}

func umLoadUser(t *testing.T, env *umTestEnv, userID string) *types.User {
	t.Helper()
	user, err := env.userRepo.GetUserByID(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, user)
	return user
}

func umRevokedTokenCount(t *testing.T, env *umTestEnv, userID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(&types.AuthToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, true).
		Count(&n).Error)
	return n
}

func umAuditRows(
	t *testing.T, env *umTestEnv, tenantID uint64, action types.AuditAction,
) []types.AuditLog {
	t.Helper()
	var rows []types.AuditLog
	require.NoError(t, env.db.
		Where("tenant_id = ? AND action = ?", tenantID, string(action)).
		Order("id ASC").
		Find(&rows).Error)
	return rows
}

// TestUserManagementBanLowerRoles：owner 可封禁本空间 viewer/contributor/admin。
// 断言 AC-14（is_active=false + token 全吊销 + 成员关系保留）、AC-18、AC-33（审计）。
func TestUserManagementBanLowerRoles(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleViewer,
		types.TenantRoleContributor,
		types.TenantRoleAdmin,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			env := newUMTestEnv(t)
			umSeedUser(t, env, "actor", nil)
			umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
			umSeedUser(t, env, "target", nil)
			umSeedMembership(t, env, "target", umTenantA, role)
			umSeedTokens(t, env, "target", 2)

			ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
			user, err := env.svc.BanUser(ctx, umTenantA, "target", "policy violation")
			require.NoError(t, err)
			require.NotNil(t, user)
			require.False(t, user.IsActive)

			require.False(t, umLoadUser(t, env, "target").IsActive, "ban must persist is_active=false")
			require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"),
				"ban must revoke every outstanding token")

			member, err := env.memberRepo.Get(context.Background(), "target", umTenantA)
			require.NoError(t, err)
			require.NotNil(t, member, "ban must keep the tenant_members row (AC-18)")
			require.Equal(t, types.TenantMemberStatusActive, member.Status)
			require.Equal(t, role, member.Role, "ban must not change the member role")

			rows := umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned"))
			require.Len(t, rows, 1)
			require.Equal(t, "actor", rows[0].ActorUserID)
			require.Equal(t, "target", rows[0].TargetUserID)
			require.Equal(t, types.AuditOutcomeSuccess, rows[0].Outcome)
			require.False(t, rows[0].CreatedAt.IsZero(), "audit row must carry a timestamp (AC-33)")

			details := string(rows[0].Details)
			require.NotContains(t, details, "hash-target", "audit must not carry password hashes")
			require.NotContains(t, details, "secret-token", "audit must not carry token values")
			require.NotContains(t, details, "target@example.com", "audit must not carry full PII")
		})
	}
}

// TestUserManagementBanIsAccountWide：封禁是账号级（users.is_active），
// 目标同时属于其他空间时一并生效；两边的成员关系都保留（AC-15/AC-18）。
func TestUserManagementBanIsAccountWide(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleContributor)
	umSeedTokens(t, env, "target", 2)

	ctx := umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target", "account-wide")
	require.NoError(t, err)

	require.False(t, umLoadUser(t, env, "target").IsActive, "ban is account-level, not membership-level")
	for _, tenantID := range []uint64{umTenantA, umTenantB} {
		member, err := env.memberRepo.Get(context.Background(), "target", tenantID)
		require.NoError(t, err)
		require.NotNil(t, member, "membership in tenant %d must survive the ban", tenantID)
		require.Equal(t, types.TenantMemberStatusActive, member.Status)
	}
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"))
}

// TestUserManagementBanProtectsPrivilegedTargets：owner 不能操作 owner（含自己）、
// 系统管理员、跨空间超管、在别处持有 owner 的账号（AC-4/5/6/7）。
func TestUserManagementBanProtectsPrivilegedTargets(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "co-owner", nil)
	umSeedMembership(t, env, "co-owner", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "system-admin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "system-admin", umTenantA, types.TenantRoleAdmin)
	umSeedUser(t, env, "cross-superuser", func(u *types.User) { u.CanAccessAllTenants = true })
	umSeedMembership(t, env, "cross-superuser", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "elsewhere-owner", nil)
	umSeedMembership(t, env, "elsewhere-owner", umTenantA, types.TenantRoleAdmin)
	umSeedMembership(t, env, "elsewhere-owner", umTenantB, types.TenantRoleOwner)
	umSeedUser(t, env, "outsider", nil)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)

	cases := []struct {
		name   string
		target string
		status int
	}{
		{name: "self", target: "actor", status: 403},
		{name: "another_owner", target: "co-owner", status: 403},
		{name: "system_admin", target: "system-admin", status: 403},
		{name: "cross_tenant_superuser", target: "cross-superuser", status: 403},
		{name: "owner_of_another_tenant", target: "elsewhere-owner", status: 403},
		{name: "not_a_member_of_this_tenant", target: "outsider", status: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.BanUser(ctx, umTenantA, tc.target, "denied")
			umRequireHTTPStatus(t, err, tc.status)
			// AC-4：owner 对 owner/超管目标的解封同样 403（不是"按钮不可用"而已）。
			_, err = env.svc.UnbanUser(ctx, umTenantA, tc.target, "denied")
			umRequireHTTPStatus(t, err, tc.status)
			require.True(t, umLoadUser(t, env, tc.target).IsActive,
				"a rejected ban/unban must leave the target active")
		})
	}

	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")),
		"rejected bans must not write a success audit row")
}

// TestUserManagementSystemAdminCanBanOwner：系统管理员即使在本空间只是 viewer，
// 也能封禁 owner，且不要求在目标空间持有 owner 角色（AC-3 的路由守卫口径）。
func TestUserManagementSystemAdminCanBanOwner(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "owner-target", nil)
	umSeedMembership(t, env, "owner-target", umTenantA, types.TenantRoleOwner)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	user, err := env.svc.BanUser(ctx, umTenantA, "owner-target", "handover pending")
	require.NoError(t, err)
	require.False(t, user.IsActive)
	require.False(t, umLoadUser(t, env, "owner-target").IsActive)
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")), 1)
}

// TestUserManagementSystemAdminCannotBanSelf：AC-7「任何人都不能封禁自己」。
func TestUserManagementSystemAdminCannotBanSelf(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleOwner)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleOwner, true)
	_, err := env.svc.BanUser(ctx, umTenantA, "sysadmin", "oops")
	umRequireHTTPStatus(t, err, 403)
	require.True(t, umLoadUser(t, env, "sysadmin").IsActive)
}

// TestUserManagementNonOwnerRejected：服务层自证防御，非 owner/超管直接拒绝。
func TestUserManagementNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleAdmin,
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			env := newUMTestEnv(t)
			umSeedUser(t, env, "actor", nil)
			umSeedMembership(t, env, "actor", umTenantA, role)
			umSeedUser(t, env, "target", nil)
			umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)

			ctx := umCallerCtx("actor", umTenantA, role, false)
			_, err := env.svc.BanUser(ctx, umTenantA, "target", "nope")
			umRequireHTTPStatus(t, err, 403)
			_, err = env.svc.UnbanUser(ctx, umTenantA, "target", "nope")
			umRequireHTTPStatus(t, err, 403)
			require.True(t, umLoadUser(t, env, "target").IsActive)
		})
	}
}

// TestUserManagementBanUnbanRoundTrip：解封恢复 is_active=true，但不恢复 token；
// 成员关系保留；写 rbac.member_unbanned（AC-14/16/17/18/33）。
func TestUserManagementBanUnbanRoundTrip(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleContributor)
	umSeedTokens(t, env, "target", 2)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target", "spam")
	require.NoError(t, err)

	user, err := env.svc.UnbanUser(ctx, umTenantA, "target", "appeal accepted")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.True(t, user.IsActive)
	require.True(t, umLoadUser(t, env, "target").IsActive)
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"),
		"unban must not restore old tokens; the user has to sign in again")

	member, err := env.memberRepo.Get(context.Background(), "target", umTenantA)
	require.NoError(t, err)
	require.NotNil(t, member, "unban must keep the tenant_members row")
	require.Equal(t, types.TenantRoleContributor, member.Role)

	rows := umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_unbanned"))
	require.Len(t, rows, 1)
	require.Equal(t, "actor", rows[0].ActorUserID)
	require.Equal(t, "target", rows[0].TargetUserID)
	require.NotContains(t, string(rows[0].Details), "secret-token")
}

// TestUserManagementBanUnbanIdempotent：重复 ban/unban 都是 200/无错。
func TestUserManagementBanUnbanIdempotent(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedTokens(t, env, "target", 2)
	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)

	for i := 0; i < 2; i++ {
		_, err := env.svc.BanUser(ctx, umTenantA, "target", "repeat")
		require.NoError(t, err, "repeated ban must be idempotent (#%d)", i)
	}
	require.False(t, umLoadUser(t, env, "target").IsActive)
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"))

	for i := 0; i < 2; i++ {
		_, err := env.svc.UnbanUser(ctx, umTenantA, "target", "repeat")
		require.NoError(t, err, "repeated unban must be idempotent (#%d)", i)
	}
	require.True(t, umLoadUser(t, env, "target").IsActive)
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"))
}

// TestUserManagementUnbanRejectsCrossTenantBan：解封要求「最近一次 ban
// 发生在本租户」，不是「本租户曾经 ban 过」。A 先封、B 后封时，A 空间
// owner 解封必须 403；系统管理员可以跨租户解封（02-plan §4.3）。
func TestUserManagementUnbanRejectsCrossTenantBan(t *testing.T) {
	// 子用例 1：本租户完全没有 ban 历史，目标只在 B 被封。
	// 同时自检夹具：is_active=false 必须真的落库，防止 GORM default:true
	// 把 Create 时的零值覆盖回 true（上一轮该用例的假红根因）。
	t.Run("no_ban_in_this_tenant", func(t *testing.T) {
		env := newUMTestEnv(t)
		umSeedUser(t, env, "actor-a", nil)
		umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
		umSeedUser(t, env, "target", func(u *types.User) { u.IsActive = false })
		umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
		umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)
		require.False(t, umLoadUser(t, env, "target").IsActive,
			"fixture must persist is_active=false (GORM default:true must not win)")

		// 目标是在 B 空间被 ban 的；A 空间没有 ban 审计。
		require.NoError(t, env.db.Create(&types.AuditLog{
			TenantID:     umTenantB,
			ActorUserID:  "actor-b",
			ActorRole:    string(types.TenantRoleOwner),
			Action:       types.AuditAction("rbac.member_banned"),
			TargetType:   "tenant_member",
			TargetUserID: "target",
			Outcome:      types.AuditOutcomeSuccess,
			CreatedAt:    time.Now(),
		}).Error)

		ownerA := umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false)
		_, err := env.svc.UnbanUser(ownerA, umTenantA, "target", "not mine")
		umRequireHTTPStatus(t, err, 403)
		require.False(t, umLoadUser(t, env, "target").IsActive,
			"a rejected cross-tenant unban must leave the account banned")
	})

	// 子用例 2：A 先封、B 后封。本租户有 ban 历史，但最近一次在 B，
	// 因此 A 空间 owner 解封仍必须 403；“曾经 ban 过”不等于“可以解封”。
	t.Run("older_ban_in_this_tenant", func(t *testing.T) {
		env := newUMTestEnv(t)
		umSeedUser(t, env, "actor-a", nil)
		umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
		umSeedUser(t, env, "actor-b", nil)
		umSeedMembership(t, env, "actor-b", umTenantB, types.TenantRoleOwner)
		umSeedUser(t, env, "target", nil)
		umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
		umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)
		umSeedTokens(t, env, "target", 2)

		ownerA := umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false)
		ownerB := umCallerCtx("actor-b", umTenantB, types.TenantRoleOwner, false)

		// 1) A 空间先封：本租户确实留下 ban 审计。
		_, err := env.svc.BanUser(ownerA, umTenantA, "target", "a-first")
		require.NoError(t, err)
		require.False(t, umLoadUser(t, env, "target").IsActive)

		// 2) B 空间后封：账号已 inactive，状态不变，但“最近一次 ban”变成 B。
		_, err = env.svc.BanUser(ownerB, umTenantB, "target", "b-later")
		require.NoError(t, err)
		require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")), 1)
		require.Len(t, umAuditRows(t, env, umTenantB, types.AuditAction("rbac.member_banned")), 1)

		// 3) A 空间 owner 解封：本租户有 ban 历史，但不是最近一次 → 403。
		_, err = env.svc.UnbanUser(ownerA, umTenantA, "target", "not mine")
		umRequireHTTPStatus(t, err, 403)
		require.False(t, umLoadUser(t, env, "target").IsActive,
			"a rejected cross-tenant unban must leave the account banned")
	})
}

// TestUserManagementSystemAdminUnbansCrossTenantBan：系统管理员不受
// “最近一次 ban 必须在本租户”的限制，可从任意空间解封（02-plan §4.3）。
func TestUserManagementSystemAdminUnbansCrossTenantBan(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "actor-b", nil)
	umSeedMembership(t, env, "actor-b", umTenantB, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleViewer)

	// 目标在 B 空间被 ban；A 空间没有 ban 审计。
	ownerB := umCallerCtx("actor-b", umTenantB, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ownerB, umTenantB, "target", "b-only")
	require.NoError(t, err)
	require.False(t, umLoadUser(t, env, "target").IsActive)

	sysadminCtx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	user, err := env.svc.UnbanUser(sysadminCtx, umTenantA, "target", "platform appeal")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.True(t, user.IsActive)
	require.True(t, umLoadUser(t, env, "target").IsActive)
}

// TestUserManagementUnbanSameTenantAfterOwnBan：最近一次 ban 在本租户时，
// owner 解封正常通过（AC-14/AC-16）。
func TestUserManagementUnbanSameTenantAfterOwnBan(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedTokens(t, env, "target", 1)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target", "spam")
	require.NoError(t, err)
	require.False(t, umLoadUser(t, env, "target").IsActive)

	user, err := env.svc.UnbanUser(ctx, umTenantA, "target", "appeal accepted")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.True(t, user.IsActive)
	require.True(t, umLoadUser(t, env, "target").IsActive)
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_unbanned")), 1)
}

// TestUserManagementTenantIsolation：目标不在本租户 → 404，且不产生任何副作用。
func TestUserManagementTenantIsolation(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-b", nil)
	umSeedMembership(t, env, "target-b", umTenantB, types.TenantRoleViewer)
	umSeedTokens(t, env, "target-b", 1)

	ctx := umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false)
	_, err := env.svc.BanUser(ctx, umTenantA, "target-b", "wrong tenant")
	umRequireHTTPStatus(t, err, 404)

	require.True(t, umLoadUser(t, env, "target-b").IsActive)
	require.EqualValues(t, 0, umRevokedTokenCount(t, env, "target-b"))
	member, err := env.memberRepo.Get(context.Background(), "target-b", umTenantB)
	require.NoError(t, err)
	require.NotNil(t, member, "the other tenant's membership must stay untouched")
	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")))
}

// TestUserManagementConcurrentBanIsIdempotent：并发重复 ban 不产生半状态
// （AC-4 的 T02 部分；ban/purge 并发归 T03）。
func TestUserManagementConcurrentBanIsIdempotent(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedTokens(t, env, "target", 2)

	const workers = 4
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
			_, errs[idx] = env.svc.BanUser(ctx, umTenantA, "target", fmt.Sprintf("concurrent-%d", idx))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "concurrent ban #%d must be idempotent", i)
	}
	require.False(t, umLoadUser(t, env, "target").IsActive)
	require.EqualValues(t, 2, umRevokedTokenCount(t, env, "target"))
	require.NotEmpty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")))
	// 成员关系不得被并发写坏。
	member, err := env.memberRepo.Get(context.Background(), "target", umTenantA)
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, types.TenantRoleViewer, member.Role)
}
