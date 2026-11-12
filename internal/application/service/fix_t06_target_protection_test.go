package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// T06 红阶段：普通管理员 co-owner 能力集 + 目标保护 + 角色目录收敛。
//
// 契约来源：01-spec AC-5/8/9/10/12/13/14、02-plan §2.2 / §4.1 / §4.5、
// 03-tickets/T06.md、队长派单的目标保护矩阵。
//
// 本文件锁定服务层行为（业务规则只在这里断言）：
//   - 普通管理员（admin）对 owner：ban/unban/purge/移除/改角色 → 403；
//   - 普通管理员对另一个普通管理员：同上 → 403（同级保护）；
//   - 普通管理员对游客：同上成功；
//   - owner 对普通管理员/游客成功，对自己 → 403；
//   - 超管保持现状（可管理 owner，但 owner 仍须先交接才能 purge）；
//   - 角色计数只保留 owner/admin/viewer 三桶，contributor/editor 桶消失；
//   - 普通管理员只看当前空间（scoped other/all 仍 403）。

// t06ManagedService 暴露 T06 需要的两个服务面：账号级（ban/unban/purge）
// 与成员关系级（移除/改角色）。
type t06ManagedService struct {
	um    interfaces.UserManagementService
	mem   interfaces.TenantMemberService
	audit interfaces.AuditLogService
}

func t06NewUMEnv(t *testing.T) (*umTestEnv, *t06ManagedService) {
	t.Helper()
	env := newUMTestEnv(t)
	return env, &t06ManagedService{um: env.svc, audit: env.audit}
}

func t06NewMemberEnv(t *testing.T) (*omOwnerEnv, *t06ManagedService) {
	t.Helper()
	env := newOMOwnerEnv(t)
	return env, &t06ManagedService{mem: env.memberSvc, audit: env.audit}
}

// t06UpdateRoleGuarded 通过窄接口调用 CAS 改角色。接口本身不包含该方法
// （T32 用类型断言接入），测试同样按断言调用，避免扩大生产接口。
func t06UpdateRoleGuarded(
	t *testing.T,
	svc interfaces.TenantMemberService,
	ctx context.Context,
	userID string,
	tenantID uint64,
	expectedRole, newRole types.TenantRole,
	allowOwnerDemotion bool,
) error {
	t.Helper()
	guarded, ok := svc.(interface {
		UpdateRoleGuarded(
			ctx context.Context,
			userID string,
			tenantID uint64,
			expectedRole, newRole types.TenantRole,
			allowOwnerDemotion bool,
		) error
	})
	require.True(t, ok, "TenantMemberService must expose UpdateRoleGuarded")
	return guarded.UpdateRoleGuarded(ctx, userID, tenantID, expectedRole, newRole, allowOwnerDemotion)
}

func t06SeedTargets(t *testing.T, env *omOwnerEnv, tenantID uint64) {
	t.Helper()
	for _, id := range []string{"u-owner", "u-admin", "u-admin2", "u-viewer"} {
		omOwnerSeedUser(t, env, id, nil)
	}
	omOwnerSeedMembership(t, env, "u-owner", tenantID, types.TenantRoleOwner)
	omOwnerSeedMembership(t, env, "u-admin", tenantID, types.TenantRoleAdmin)
	omOwnerSeedMembership(t, env, "u-admin2", tenantID, types.TenantRoleAdmin)
	omOwnerSeedMembership(t, env, "u-viewer", tenantID, types.TenantRoleViewer)
}

// ---------------------------------------------------------------------------
// 账号级：ban / unban / purge
// ---------------------------------------------------------------------------

func TestT06BanTargetProtectionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		actorID    string
		actorRole  types.TenantRole
		sysAdmin   bool
		targetID   string
		wantStatus int // 200 = success, 403 = protected
	}{
		{"admin_to_owner", "u-admin", types.TenantRoleAdmin, false, "u-owner", 403},
		{"admin_to_peer_admin", "u-admin", types.TenantRoleAdmin, false, "u-admin2", 403},
		{"admin_to_viewer", "u-admin", types.TenantRoleAdmin, false, "u-viewer", 200},
		{"owner_to_admin", "u-owner", types.TenantRoleOwner, false, "u-admin", 200},
		{"owner_to_viewer", "u-owner", types.TenantRoleOwner, false, "u-viewer", 200},
		{"owner_to_self", "u-owner", types.TenantRoleOwner, false, "u-owner", 403},
		{"sysadmin_to_owner", "u-sysadmin", types.TenantRoleViewer, true, "u-owner", 200},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, svc := t06NewUMEnv(t)
			umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
			umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-admin2", "admin2@example.com", "admin2", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
			umSeedUser(t, env, "u-sysadmin", nil)

			ctx := umCallerCtx(tc.actorID, umTenantA, tc.actorRole, tc.sysAdmin)
			_, err := svc.um.BanUser(ctx, umTenantA, tc.targetID, "t06")
			if tc.wantStatus == 200 {
				require.NoError(t, err, "actor %s must be allowed to ban %s", tc.actorID, tc.targetID)
				return
			}
			umRequireHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

func TestT06UnbanTargetProtectionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		actorID    string
		actorRole  types.TenantRole
		sysAdmin   bool
		targetID   string
		wantStatus int
	}{
		{"admin_to_owner", "u-admin", types.TenantRoleAdmin, false, "u-owner", 403},
		{"admin_to_peer_admin", "u-admin", types.TenantRoleAdmin, false, "u-admin2", 403},
		{"admin_to_viewer", "u-admin", types.TenantRoleAdmin, false, "u-viewer", 200},
		{"owner_to_admin", "u-owner", types.TenantRoleOwner, false, "u-admin", 200},
		{"owner_to_self", "u-owner", types.TenantRoleOwner, false, "u-owner", 403},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, svc := t06NewUMEnv(t)
			umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
			umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-admin2", "admin2@example.com", "admin2", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
			umSeedUser(t, env, "u-sysadmin", nil)

			// 先由超管在本空间封禁目标，制造「最近一次 ban 在本租户」的前置。
			sysCtx := umCallerCtx("u-sysadmin", umTenantA, types.TenantRoleViewer, true)
			_, err := svc.um.BanUser(sysCtx, umTenantA, tc.targetID, "setup")
			require.NoError(t, err, "system admin must be able to set up the banned target")

			ctx := umCallerCtx(tc.actorID, umTenantA, tc.actorRole, tc.sysAdmin)
			_, err = svc.um.UnbanUser(ctx, umTenantA, tc.targetID, "t06")
			if tc.wantStatus == 200 {
				require.NoError(t, err, "actor %s must be allowed to unban %s", tc.actorID, tc.targetID)
				return
			}
			umRequireHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

func TestT06PurgeTargetProtectionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		actorID    string
		actorRole  types.TenantRole
		sysAdmin   bool
		targetID   string
		wantStatus int
	}{
		{"admin_to_owner", "u-admin", types.TenantRoleAdmin, false, "u-owner", 403},
		{"admin_to_peer_admin", "u-admin", types.TenantRoleAdmin, false, "u-admin2", 403},
		{"admin_to_viewer", "u-admin", types.TenantRoleAdmin, false, "u-viewer", 200},
		{"owner_to_viewer", "u-owner", types.TenantRoleOwner, false, "u-viewer", 200},
		{"owner_to_self", "u-owner", types.TenantRoleOwner, false, "u-owner", 403},
		{"sysadmin_to_current_owner", "u-sysadmin", types.TenantRoleViewer, true, "u-owner", 403},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, svc := t06NewUMEnv(t)
			umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
			umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-admin2", "admin2@example.com", "admin2", umTenantA, types.TenantRoleAdmin, true)
			umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
			umSeedUser(t, env, "u-sysadmin", nil)

			ctx := umCallerCtx(tc.actorID, umTenantA, tc.actorRole, tc.sysAdmin)
			email := map[string]string{
				"u-owner":  "owner@example.com",
				"u-admin":  "admin@example.com",
				"u-admin2": "admin2@example.com",
				"u-viewer": "viewer@example.com",
			}[tc.targetID]
			err := svc.um.PurgeUser(ctx, umTenantA, tc.targetID, email, "t06")
			if tc.wantStatus == 200 {
				require.NoError(t, err, "actor %s must be allowed to purge %s", tc.actorID, tc.targetID)
				return
			}
			umRequireHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

// ---------------------------------------------------------------------------
// 成员关系级：移除 / 改角色（TenantMemberService）
// ---------------------------------------------------------------------------

func TestT06RemoveMemberTargetProtectionMatrix(t *testing.T) {
	const tenantID uint64 = 101
	cases := []struct {
		name       string
		actorID    string
		actorRole  types.TenantRole
		targetID   string
		wantStatus int
	}{
		{"admin_to_owner", "u-admin", types.TenantRoleAdmin, "u-owner", 403},
		{"admin_to_peer_admin", "u-admin", types.TenantRoleAdmin, "u-admin2", 403},
		{"admin_to_viewer", "u-admin", types.TenantRoleAdmin, "u-viewer", 200},
		{"owner_to_admin", "u-owner", types.TenantRoleOwner, "u-admin", 200},
		{"owner_to_viewer", "u-owner", types.TenantRoleOwner, "u-viewer", 200},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, svc := t06NewMemberEnv(t)
			t06SeedTargets(t, env, tenantID)
			ctx := umCallerCtx(tc.actorID, tenantID, tc.actorRole, false)

			err := svc.mem.RemoveMember(ctx, tc.targetID, tenantID)
			if tc.wantStatus == 200 {
				require.NoError(t, err, "actor %s must be allowed to remove %s", tc.actorID, tc.targetID)
				return
			}
			umRequireHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

func TestT06UpdateRoleTargetProtectionMatrix(t *testing.T) {
	const tenantID uint64 = 101
	cases := []struct {
		name       string
		actorID    string
		actorRole  types.TenantRole
		targetID   string
		fromRole   types.TenantRole
		toRole     types.TenantRole
		wantStatus int
		anyError   bool
	}{
		{"admin_to_peer_admin", "u-admin", types.TenantRoleAdmin, "u-admin2",
			types.TenantRoleAdmin, types.TenantRoleViewer, 403, false},
		// owner 降级在服务层已有「必须先交接」保护（sentinel 错误），
		// handler 统一映射 403；这里只要求被拒绝，不锁具体错误类型。
		{"admin_to_owner", "u-admin", types.TenantRoleAdmin, "u-owner",
			types.TenantRoleOwner, types.TenantRoleAdmin, 0, true},
		{"admin_to_viewer", "u-admin", types.TenantRoleAdmin, "u-viewer",
			types.TenantRoleViewer, types.TenantRoleAdmin, 200, false},
		{"owner_to_admin", "u-owner", types.TenantRoleOwner, "u-admin",
			types.TenantRoleAdmin, types.TenantRoleViewer, 200, false},
		{"owner_to_viewer", "u-owner", types.TenantRoleOwner, "u-viewer",
			types.TenantRoleViewer, types.TenantRoleAdmin, 200, false},
		{"owner_self_demotion", "u-owner", types.TenantRoleOwner, "u-owner",
			types.TenantRoleOwner, types.TenantRoleAdmin, 0, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, svc := t06NewMemberEnv(t)
			t06SeedTargets(t, env, tenantID)
			ctx := umCallerCtx(tc.actorID, tenantID, tc.actorRole, false)

			err := t06UpdateRoleGuarded(t, svc.mem, ctx, tc.targetID, tenantID,
				tc.fromRole, tc.toRole, false)
			if tc.wantStatus == 200 {
				require.NoError(t, err, "actor %s must be allowed to change %s", tc.actorID, tc.targetID)
				member, loadErr := env.memberRepo.Get(context.Background(), tc.targetID, tenantID)
				require.NoError(t, loadErr)
				require.NotNil(t, member)
				require.Equal(t, tc.toRole, member.Role)
				return
			}
			if tc.anyError {
				require.Error(t, err, "actor %s must not be allowed to change %s", tc.actorID, tc.targetID)
				return
			}
			umRequireHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

// ---------------------------------------------------------------------------
// 审计：成功操作写审计；被目标保护拒绝不写成功审计
// ---------------------------------------------------------------------------

func TestT06AdminSuccessfulOperationsWriteAudit(t *testing.T) {
	env, svc := t06NewMemberEnv(t)
	const tenantID uint64 = 101
	t06SeedTargets(t, env, tenantID)

	adminCtx := umCallerCtx("u-admin", tenantID, types.TenantRoleAdmin, false)
	require.NoError(t, t06UpdateRoleGuarded(t, svc.mem, adminCtx, "u-viewer", tenantID,
		types.TenantRoleViewer, types.TenantRoleAdmin, false))

	rows := omOwnerAuditsByAction(t, env, types.AuditActionMemberRoleChanged)
	require.NotEmpty(t, rows, "a successful role change by an admin must be audited")
	found := false
	for _, row := range rows {
		if row.TargetUserID == "u-viewer" {
			found = true
			require.Equal(t, "u-admin", row.ActorUserID)
			require.Equal(t, string(types.TenantRoleAdmin), row.ActorRole)
		}
	}
	require.True(t, found, "audit must name the viewer role-change target")
}

func TestT06RejectedTargetProtectionWritesNoSuccessAudit(t *testing.T) {
	env, svc := t06NewMemberEnv(t)
	const tenantID uint64 = 101
	t06SeedTargets(t, env, tenantID)

	adminCtx := umCallerCtx("u-admin", tenantID, types.TenantRoleAdmin, false)
	err := t06UpdateRoleGuarded(t, svc.mem, adminCtx, "u-admin2", tenantID,
		types.TenantRoleAdmin, types.TenantRoleViewer, false)
	umRequireHTTPStatus(t, err, 403)

	rows := omOwnerAuditsByAction(t, env, types.AuditActionMemberRoleChanged)
	require.Empty(t, rows, "a target-protection rejection must not write a success audit")
}

// ---------------------------------------------------------------------------
// 角色目录：owner/admin/viewer 三桶；contributor/editor 桶消失
// ---------------------------------------------------------------------------

func TestT06TenantRoleCountsUseThreeBuckets(t *testing.T) {
	env := newUMTestEnv(t)
	umListSeedMember(t, env, "u-owner", "owner@example.com", "owner", umTenantA, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin", "admin@example.com", "admin", umTenantA, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-viewer", "viewer@example.com", "viewer", umTenantA, types.TenantRoleViewer, true)
	// 历史 contributor 行仍可能存在（枚举保留），但目录不得再有 contributor 桶。
	umListSeedMember(t, env, "u-contrib", "contrib@example.com", "contrib", umTenantA, types.TenantRoleContributor, true)

	res, err := env.svc.ListUsers(
		umCallerCtx("u-owner", umTenantA, types.TenantRoleOwner, false),
		umTenantA, "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, map[types.TenantRole]int64{
		types.TenantRoleOwner:  1,
		types.TenantRoleAdmin:  1,
		types.TenantRoleViewer: 1,
	}, res.RoleCounts, "role_counts must only expose owner/admin/viewer buckets")
	_, hasContributor := res.RoleCounts[types.TenantRoleContributor]
	require.False(t, hasContributor, "contributor bucket must be removed from the directory")

	// 普通管理员当前空间列表可读（AC-5：拿 co-owner 能力集）。
	adminRes, adminErr := env.svc.ListUsers(
		umCallerCtx("u-admin", umTenantA, types.TenantRoleAdmin, false),
		umTenantA, "", "", 1, 20)
	require.NoError(t, adminErr, "an admin must be able to read the current-space user list")
	require.NotNil(t, adminRes)
}

func TestT06ScopedRoleCountsThreeBucketsAndNoSpace(t *testing.T) {
	env := umScopeEnv(t)
	umListSeedMember(t, env, "u-owner-b", "owner-b@example.com", "owner-b",
		umTenantB, types.TenantRoleOwner, true)
	umListSeedMember(t, env, "u-admin-b", "admin-b@example.com", "admin-b",
		umTenantB, types.TenantRoleAdmin, true)
	umListSeedMember(t, env, "u-viewer-b", "viewer-b@example.com", "viewer-b",
		umTenantB, types.TenantRoleViewer, true)
	umSeedUser(t, env, "u-nospace", func(u *types.User) {
		u.Email = "nospace@example.com"
		u.Username = "nospace"
	})

	res, err := env.svc.ListUsersScoped(umScopeSysAdminCtx(umTenantA), umTenantA,
		interfaces.UserManagementScopeAll, "", "", 1, 20)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, map[types.TenantRole]int64{
		types.TenantRoleOwner:  1,
		types.TenantRoleAdmin:  1,
		types.TenantRoleViewer: 1,
	}, res.RoleCounts, "platform scope=all must expose only owner/admin/viewer buckets")
	_, hasContributor := res.RoleCounts[types.TenantRoleContributor]
	require.False(t, hasContributor, "contributor bucket must be gone from the platform view")
	require.EqualValues(t, 1, res.NoMembershipCount, "no_space directory must still count tenantless accounts")
}

func TestT06AdminCannotUsePlatformScopes(t *testing.T) {
	env := umScopeEnv(t)
	for _, scope := range []interfaces.UserManagementListScope{
		interfaces.UserManagementScopeOther,
		interfaces.UserManagementScopeAll,
	} {
		scope := scope
		t.Run(string(scope), func(t *testing.T) {
			_, err := env.svc.ListUsersScoped(
				umCallerCtx("u-admin", umTenantA, types.TenantRoleAdmin, false),
				umTenantA, scope, "", "", 1, 20)
			umRequireHTTPStatus(t, err, 403)
		})
	}
}
