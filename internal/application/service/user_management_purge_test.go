package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T03 墓碑化彻底删除服务层红阶段用例（AC-19..25b、AC-33）。
//
// 假定契约（02-plan §4.4 + 队长派单）：
//   - interfaces.UserManagementService 增加
//     PurgeUser(ctx, tenantID, targetUserID, confirmEmail, reason) error；
//   - 墓碑化不物理删 users：deleted_at 非空、is_active=false、清空
//     password_hash/avatar/preferences、email/username 改为唯一匿名值；
//   - 同一事务清理 tenant_members（软删）、tenant_invitations、auth_tokens、
//     user_resource_favorites、user_kb_pins、mcp_oauth_tokens、
//     tenant_user_env_vars、sessions、该用户的 notifications；
//   - 资产表（knowledge_bases/knowledge/custom_agents）不动，creator_id 保留；
//   - 写 rbac.member_purged，details 不含密码/token/完整 PII；
//   - confirm_email 必须与目标邮箱一致，否则 400；
//   - 重复 purge 已删除目标锁定为 404（另见 05-tests.md 的 open question）。
//
// 业务规则只在 service 测试里断言；handler 只负责把同一份错误映射成 HTTP。

// newUMPurgeEnv 在 T02 夹具（users/tenant_members/auth_tokens/audit_logs）
// 之上补齐 T03 涉及的清理表与资产表。只新建测试库，不触碰实现文件。
func newUMPurgeEnv(t *testing.T) *umTestEnv {
	t.Helper()
	env := newUMTestEnv(t)
	require.NoError(t, env.db.AutoMigrate(
		&types.TenantInvitation{},
		&types.UserResourceFavorite{},
		&types.MCPOAuthToken{},
		&types.TenantUserEnvVar{},
		&types.Session{},
		&types.Notification{},
	))
	// user_kb_pins 没有导出的 GORM 模型（repository 内部私有行结构），
	// 按 migration 的实际列建表即可。
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS user_kb_pins (
			tenant_id integer NOT NULL,
			user_id varchar(36) NOT NULL,
			kb_id varchar(64) NOT NULL,
			pinned_at datetime
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id varchar(36) PRIMARY KEY,
			tenant_id integer,
			creator_id varchar(36)
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id varchar(36) PRIMARY KEY,
			tenant_id integer,
			creator_id varchar(36)
		)`,
		`CREATE TABLE IF NOT EXISTS custom_agents (
			id varchar(36) PRIMARY KEY,
			tenant_id integer,
			creator_id varchar(36)
		)`,
	} {
		require.NoError(t, env.db.Exec(ddl).Error)
	}
	return env
}

// umSeedPurgeArtifacts 给目标用户种一份"要在 purge 时被清理"的个人数据。
func umSeedPurgeArtifacts(t *testing.T, env *umTestEnv, userID string, tenantID uint64) {
	t.Helper()
	umSeedTokens(t, env, userID, 2)
	require.NoError(t, env.db.Create(&types.TenantInvitation{
		TenantID:      tenantID,
		InviteeUserID: userID,
		Role:          types.TenantRoleViewer,
		Status:        types.TenantInvitationStatusPending,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}).Error)
	require.NoError(t, env.db.Create(&types.UserResourceFavorite{
		UserID:       userID,
		TenantID:     tenantID,
		ResourceType: types.ResourceTypeKB,
		ResourceID:   "kb-target",
	}).Error)
	require.NoError(t, env.db.Exec(
		`INSERT INTO user_kb_pins (tenant_id, user_id, kb_id, pinned_at) VALUES (?, ?, ?, ?)`,
		tenantID, userID, "kb-target", time.Now(),
	).Error)
	require.NoError(t, env.db.Create(&types.MCPOAuthToken{
		ID:            "mcp-" + userID,
		TenantID:      tenantID,
		UserID:        userID,
		PrincipalType: "user",
		PrincipalID:   userID,
		ServiceID:     "svc-1",
		ExpiresAt:     time.Now().Add(time.Hour),
	}).Error)
	require.NoError(t, env.db.Create(&types.TenantUserEnvVar{
		ID:              "env-" + userID,
		TenantID:        tenantID,
		PrincipalType:   "user",
		PrincipalID:     userID,
		SandboxConfigID: "cfg-1",
		Name:            "TOKEN",
		Value:           "secret-env-value",
	}).Error)
	require.NoError(t, env.db.Create(&types.Session{
		ID:       "session-" + userID,
		TenantID: tenantID,
		UserID:   userID,
		Title:    "chat with the doomed account",
	}).Error)
	require.NoError(t, env.db.Create(&types.Notification{
		ID:       "notif-" + userID + "-" + fmt.Sprint(tenantID),
		TenantID: tenantID,
		UserID:   userID,
		Kind:     types.NotificationAutomationFailed,
		Title:    "document processing failed",
		Body:     "retry exhausted",
	}).Error)
}

// umSeedAssets 在资产表里种几行 creator_id=userID 的数据。
func umSeedAssets(t *testing.T, env *umTestEnv, userID string, tenantID uint64) {
	t.Helper()
	for _, table := range []string{"knowledge_bases", "knowledge", "custom_agents"} {
		require.NoError(t, env.db.Exec(
			"INSERT INTO "+table+" (id, tenant_id, creator_id) VALUES (?, ?, ?)",
			table+"-"+userID, tenantID, userID,
		).Error)
	}
}

// umLoadUserUnscoped 读取被 GORM 软删过滤隐藏的墓碑行。
func umLoadUserUnscoped(t *testing.T, env *umTestEnv, userID string) *types.User {
	t.Helper()
	var user types.User
	require.NoError(t, env.db.Unscoped().Where("id = ?", userID).First(&user).Error)
	return &user
}

// umCount 是简化的一表一条件计数。
func umCount(t *testing.T, env *umTestEnv, model any, query string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, env.db.Model(model).Where(query, args...).Count(&n).Error)
	return n
}

// TestUserManagementPurgeTombstonesAndCleansUserData 覆盖 AC-19/22/23/24/
// 25/25b/33 的成功路径：墓碑化 + 全量清理 + 资产保留 + 审计 + 邮箱释放。
//
// AC-19b 收紧后，删除一个同时属于 A/B 两个空间的账号只能由超管执行，
// 因此成功路径的调用者改为系统管理员；owner 的同空间成功路径由
// TestUserManagementPurgeConfirmEmailTrimsAndIgnoresCase 等用例覆盖。
func TestUserManagementPurgeTombstonesAndCleansUserData(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)

	instructions := "private browser instructions"
	umSeedUser(t, env, "target", func(u *types.User) {
		u.Avatar = "https://cdn.example.com/avatar/target.png"
		u.Preferences = types.UserPreferences{BrowserSearchInstructions: &instructions}
	})
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleContributor)
	umSeedPurgeArtifacts(t, env, "target", umTenantA)
	// 第二个空间的个人数据也必须清掉（墓碑是账号级，不是单空间级）。
	require.NoError(t, env.db.Create(&types.Notification{
		ID: "notif-target-b", TenantID: umTenantB, UserID: "target",
		Kind: types.NotificationAutomationFailed, Title: "b", Body: "b",
	}).Error)
	require.NoError(t, env.db.Create(&types.UserResourceFavorite{
		UserID: "target", TenantID: umTenantB, ResourceType: "agent", ResourceID: "agent-target",
	}).Error)
	require.NoError(t, env.db.Create(&types.TenantInvitation{
		TenantID: umTenantB, InviteeUserID: "target", Role: types.TenantRoleContributor,
		Status: types.TenantInvitationStatusPending, ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
	require.NoError(t, env.db.Exec(
		`INSERT INTO user_kb_pins (tenant_id, user_id, kb_id, pinned_at) VALUES (?, ?, ?, ?)`,
		umTenantB, "target", "kb-target-b", time.Now(),
	).Error)
	require.NoError(t, env.db.Create(&types.MCPOAuthToken{
		ID: "mcp-target-b", TenantID: umTenantB, UserID: "target",
		PrincipalType: "user", PrincipalID: "target", ServiceID: "svc-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
	require.NoError(t, env.db.Create(&types.TenantUserEnvVar{
		ID: "env-target-b", TenantID: umTenantB, PrincipalType: "user", PrincipalID: "target",
		SandboxConfigID: "cfg-1", Name: "TOKEN", Value: "secret-env-value-b",
	}).Error)
	require.NoError(t, env.db.Create(&types.Session{
		ID: "session-target-b", TenantID: umTenantB, UserID: "target", Title: "b-space chat",
	}).Error)
	umSeedAssets(t, env, "target", umTenantA)
	// 历史审计必须在 purge 后保留（AC-25b）。
	require.NoError(t, env.db.Create(&types.AuditLog{
		TenantID:     umTenantA,
		ActorUserID:  "actor",
		Action:       types.AuditAction("rbac.member_banned"),
		TargetUserID: "target",
		Outcome:      types.AuditOutcomeSuccess,
		CreatedAt:    time.Now(),
	}).Error)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	require.NoError(t, env.svc.PurgeUser(
		ctx, umTenantA, "target", "target@example.com", "offboarding"))

	// users 行墓碑化：默认查询不可见，unscoped 才能读到。
	require.Zero(t, umCount(t, env, &types.User{}, "id = ?", "target"),
		"a tombstoned user must disappear from default-scope queries")
	tomb := umLoadUserUnscoped(t, env, "target")
	require.True(t, tomb.DeletedAt.Valid, "users.deleted_at must be set")
	require.False(t, tomb.IsActive)
	require.Empty(t, tomb.PasswordHash, "password hash must be wiped")
	require.Empty(t, tomb.Avatar, "avatar must be wiped")
	prefs, err := json.Marshal(tomb.Preferences)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(prefs), "preferences must be wiped")
	require.NotEqual(t, "target@example.com", tomb.Email, "email must be anonymised")
	require.NotEqual(t, "target", tomb.Username, "username must be anonymised")
	require.NotEmpty(t, tomb.Email)
	require.NotEmpty(t, tomb.Username)
	require.Zero(t, tomb.TenantID, "plan §4.4 clears the home tenant on tombstone")
	require.False(t, tomb.IsSystemAdmin)
	require.False(t, tomb.CanAccessAllTenants)
	require.Equal(t, "target", tomb.ID, "user id must survive for asset/audit references")

	// tenant_members 全部软删（行保留供审计/资产引用，但不再 active）。
	activeMembers, err := env.memberRepo.ListByUser(context.Background(), "target")
	require.NoError(t, err)
	require.Empty(t, activeMembers, "no active membership may survive a purge (AC-25)")
	var members []types.TenantMember
	require.NoError(t, env.db.Unscoped().Where("user_id = ?", "target").Find(&members).Error)
	require.Len(t, members, 2)
	for _, member := range members {
		require.True(t, member.DeletedAt.Valid,
			"membership in tenant %d must be soft-deleted", member.TenantID)
	}

	require.Zero(t, umCount(t, env, &types.AuthToken{},
		"user_id = ? AND is_revoked = ?", "target", false),
		"no usable token may survive a purge")

	require.Zero(t, umCount(t, env, &types.TenantInvitation{},
		"invitee_user_id = ?", "target"), "pending invitations must be cleaned up")
	require.Zero(t, umCount(t, env, &types.UserResourceFavorite{},
		"user_id = ?", "target"), "favorites must be cleaned up")
	var pins int64
	require.NoError(t, env.db.Table("user_kb_pins").Where("user_id = ?", "target").Count(&pins).Error)
	require.Zero(t, pins, "user_kb_pins must be cleaned up")
	require.Zero(t, umCount(t, env, &types.MCPOAuthToken{},
		"user_id = ?", "target"), "mcp oauth tokens must be cleaned up")
	require.Zero(t, umCount(t, env, &types.TenantUserEnvVar{},
		"principal_id = ?", "target"), "tenant_user_env_vars must be cleaned up")
	require.Zero(t, umCount(t, env, &types.Session{},
		"user_id = ?", "target"), "sessions must be cleaned up")
	require.Zero(t, umCount(t, env, &types.Notification{},
		"user_id = ?", "target"), "personal notifications must be cleaned up")

	// 资产保留，creator_id 仍指向墓碑行（AC-22）。
	for _, table := range []string{"knowledge_bases", "knowledge", "custom_agents"} {
		var n int64
		require.NoError(t, env.db.Table(table).Where("creator_id = ?", "target").Count(&n).Error)
		require.EqualValues(t, 1, n,
			"%s rows created by the purged user must survive the purge", table)
	}

	// 审计：新写一条 member_purged，且不含密码/token/完整 PII；旧审计保留。
	rows := umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged"))
	require.Len(t, rows, 1)
	require.Equal(t, "sysadmin", rows[0].ActorUserID)
	require.Equal(t, "target", rows[0].TargetUserID)
	require.Equal(t, types.AuditOutcomeSuccess, rows[0].Outcome)
	require.False(t, rows[0].CreatedAt.IsZero(), "audit must carry a timestamp (AC-33)")
	details := string(rows[0].Details)
	require.NotContains(t, details, "hash-target", "audit must not carry password hashes")
	require.NotContains(t, details, "secret-token", "audit must not carry token values")
	require.NotContains(t, details, "secret-env-value", "audit must not carry env secret values")
	require.NotContains(t, details, "target@example.com", "audit must not carry full PII")
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_banned")), 1,
		"purge must never delete existing audit rows (AC-25b)")

	// 原邮箱/用户名被释放，可重新注册（AC-24）。
	require.NoError(t, env.userRepo.CreateUser(context.Background(), &types.User{
		ID:           "re-registered",
		Username:     "target",
		Email:        "target@example.com",
		PasswordHash: "new-hash",
		IsActive:     true,
	}), "the tombstone must release the original email and username")
}

// TestUserManagementPurgeRejectsCrossSpaceTargetForOwner：AC-19b。目标同时
// 是 A、B 两个空间的 active 成员时，A 空间的 owner 不得执行账号级的彻底
// 删除（会连带清掉对方空间的数据），必须由超管处理。拒绝时不得产生任何
// 副作用：账号仍 active、PII 未清空、A/B 成员关系都在、无 purge 审计。
func TestUserManagementPurgeRejectsCrossSpaceTargetForOwner(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)

	umSeedUser(t, env, "target", func(u *types.User) {
		u.Avatar = "https://cdn.example.com/avatar/target.png"
	})
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleContributor)
	umSeedPurgeArtifacts(t, env, "target", umTenantA)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	err := env.svc.PurgeUser(ctx, umTenantA, "target", "target@example.com", "cross-space")
	umRequireHTTPStatus(t, err, 403)

	// 无墓碑化、无 PII 清空。
	user := umLoadUser(t, env, "target")
	require.True(t, user.IsActive, "a rejected cross-space purge must leave the target active")
	require.Equal(t, "hash-target", user.PasswordHash, "PII must not be wiped on rejection")
	require.Equal(t, "https://cdn.example.com/avatar/target.png", user.Avatar,
		"avatar must not be wiped on rejection")
	require.Equal(t, "target@example.com", user.Email, "email must not be anonymised on rejection")
	require.Equal(t, "target", user.Username, "username must not be anonymised on rejection")

	// A/B 两个空间的成员关系都保持 active。
	members, err := env.memberRepo.ListByUser(context.Background(), "target")
	require.NoError(t, err)
	require.Len(t, members, 2, "both memberships must stay active after a rejected purge")
	tenants := map[uint64]bool{}
	for _, member := range members {
		tenants[member.TenantID] = true
	}
	require.True(t, tenants[umTenantA])
	require.True(t, tenants[umTenantB])

	// 不能有任何 purge 副作用：token 未吊销、无审计。
	require.EqualValues(t, 0, umRevokedTokenCount(t, env, "target"),
		"a rejected cross-space purge must not revoke tokens")
	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")),
		"a rejected cross-space purge must not write a success audit row")
	require.Empty(t, umAuditRows(t, env, umTenantB, types.AuditAction("rbac.member_purged")))
}

// TestUserManagementPurgeAllowsCrossSpaceTargetForSystemAdmin：AC-19b。同一个
// 跨空间目标，超管可以执行账号级彻底删除：墓碑化、A/B 成员关系全部清理、
// 只写 1 条 rbac.member_purged 审计。
func TestUserManagementPurgeAllowsCrossSpaceTargetForSystemAdmin(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedMembership(t, env, "target", umTenantB, types.TenantRoleContributor)
	umSeedPurgeArtifacts(t, env, "target", umTenantA)
	// B 空间个人数据也必须随账号级墓碑清理。
	require.NoError(t, env.db.Create(&types.Notification{
		ID: "notif-target-b", TenantID: umTenantB, UserID: "target",
		Kind: types.NotificationAutomationFailed, Title: "b", Body: "b",
	}).Error)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	require.NoError(t, env.svc.PurgeUser(
		ctx, umTenantA, "target", "target@example.com", "cross-space offboarding"))

	tomb := umLoadUserUnscoped(t, env, "target")
	require.True(t, tomb.DeletedAt.Valid, "a system admin must be able to purge a cross-space account")
	require.False(t, tomb.IsActive)
	require.Empty(t, tomb.PasswordHash)

	// A/B 成员关系全部软删，且原租户语义保留在墓碑行上供审计。
	activeMembers, err := env.memberRepo.ListByUser(context.Background(), "target")
	require.NoError(t, err)
	require.Empty(t, activeMembers, "both A and B memberships must be cleaned up")
	var members []types.TenantMember
	require.NoError(t, env.db.Unscoped().Where("user_id = ?", "target").Find(&members).Error)
	require.Len(t, members, 2)
	tenants := map[uint64]bool{}
	for _, member := range members {
		require.True(t, member.DeletedAt.Valid,
			"membership in tenant %d must be soft-deleted", member.TenantID)
		tenants[member.TenantID] = true
	}
	require.True(t, tenants[umTenantA])
	require.True(t, tenants[umTenantB])

	// B 空间个人数据同步清理。
	require.Zero(t, umCount(t, env, &types.Notification{}, "user_id = ?", "target"))

	rows := umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged"))
	require.Len(t, rows, 1, "a cross-space purge must write exactly one success audit row")
	require.Equal(t, "sysadmin", rows[0].ActorUserID)
	require.Equal(t, "target", rows[0].TargetUserID)
	require.Equal(t, types.AuditOutcomeSuccess, rows[0].Outcome)
}

// TestUserManagementPurgeAnonymisesUniquely：两个墓碑的匿名 email/username
// 不得互相冲突（AC-24）。不锁定具体格式（deleted-<id> 只是建议形状）。
func TestUserManagementPurgeAnonymisesUniquely(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-a", nil)
	umSeedMembership(t, env, "target-a", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "target-b", nil)
	umSeedMembership(t, env, "target-b", umTenantA, types.TenantRoleAdmin)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.PurgeUser(ctx, umTenantA, "target-a", "target-a@example.com", "one"))
	require.NoError(t, env.svc.PurgeUser(ctx, umTenantA, "target-b", "target-b@example.com", "two"))

	a := umLoadUserUnscoped(t, env, "target-a")
	b := umLoadUserUnscoped(t, env, "target-b")
	require.NotEqual(t, a.Email, b.Email, "tombstone emails must be unique")
	require.NotEqual(t, a.Username, b.Username, "tombstone usernames must be unique")
}

// TestUserManagementPurgeRequiresMatchingConfirmEmail：AC-23 的邮箱二次确认。
func TestUserManagementPurgeRequiresMatchingConfirmEmail(t *testing.T) {
	for name, email := range map[string]string{
		"mismatch": "someone-else@example.com",
		"empty":    "",
	} {
		email := email
		t.Run(name, func(t *testing.T) {
			env := newUMPurgeEnv(t)
			umSeedUser(t, env, "actor", nil)
			umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
			umSeedUser(t, env, "target", nil)
			umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
			umSeedTokens(t, env, "target", 1)

			ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
			err := env.svc.PurgeUser(ctx, umTenantA, "target", email, "wrong confirmation")
			umRequireHTTPStatus(t, err, 400)

			user := umLoadUser(t, env, "target")
			require.True(t, user.IsActive, "a rejected purge must not tombstone the account")
			require.Equal(t, "hash-target", user.PasswordHash)
			require.EqualValues(t, 0, umRevokedTokenCount(t, env, "target"),
				"a rejected purge must not revoke tokens")
			member, err := env.memberRepo.Get(context.Background(), "target", umTenantA)
			require.NoError(t, err)
			require.NotNil(t, member, "a rejected purge must keep the membership active")
			require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")))
		})
	}
}

// TestUserManagementPurgeConfirmEmailTrimsAndIgnoresCase：邮箱二次确认按
// 去首尾空白 + 大小写不敏感比较；仅大小写/空白不同不构成"不一致"。
func TestUserManagementPurgeConfirmEmailTrimsAndIgnoresCase(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.PurgeUser(
		ctx, umTenantA, "target", "  TARGET@Example.COM  ", "case/space check"))
	require.True(t, umLoadUserUnscoped(t, env, "target").DeletedAt.Valid)
}

// TestUserManagementPurgeProtectsPrivilegedTargets：AC-4/5/6/7/21 的目标保护。
func TestUserManagementPurgeProtectsPrivilegedTargets(t *testing.T) {
	env := newUMPurgeEnv(t)
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
			err := env.svc.PurgeUser(ctx, umTenantA, tc.target, tc.target+"@example.com", "denied")
			umRequireHTTPStatus(t, err, tc.status)
			require.True(t, umLoadUser(t, env, tc.target).IsActive,
				"a rejected purge must leave the target active")
		})
	}
	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")),
		"rejected purges must not write a success audit row")
}

// TestUserManagementPurgeNonOwnerRejected：服务层自证防御，contributor/viewer
// 403。T06 起普通管理员可 purge 游客，admin→viewer 成功与 admin→owner/同级
// 403 由 TestT06PurgeTargetProtectionMatrix 覆盖。
func TestUserManagementPurgeNonOwnerRejected(t *testing.T) {
	for _, role := range []types.TenantRole{
		types.TenantRoleContributor,
		types.TenantRoleViewer,
	} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			env := newUMPurgeEnv(t)
			umSeedUser(t, env, "actor", nil)
			umSeedMembership(t, env, "actor", umTenantA, role)
			umSeedUser(t, env, "target", nil)
			umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)

			ctx := umCallerCtx("actor", umTenantA, role, false)
			err := env.svc.PurgeUser(ctx, umTenantA, "target", "target@example.com", "nope")
			umRequireHTTPStatus(t, err, 403)
			require.True(t, umLoadUser(t, env, "target").IsActive)
		})
	}
}

// TestUserManagementPurgeTenantIsolation：目标不在本租户 → 404 且无副作用。
func TestUserManagementPurgeTenantIsolation(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor-a", nil)
	umSeedMembership(t, env, "actor-a", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target-b", nil)
	umSeedMembership(t, env, "target-b", umTenantB, types.TenantRoleViewer)
	umSeedTokens(t, env, "target-b", 1)

	ctx := umCallerCtx("actor-a", umTenantA, types.TenantRoleOwner, false)
	err := env.svc.PurgeUser(ctx, umTenantA, "target-b", "target-b@example.com", "wrong tenant")
	umRequireHTTPStatus(t, err, 404)

	require.True(t, umLoadUser(t, env, "target-b").IsActive)
	require.EqualValues(t, 0, umRevokedTokenCount(t, env, "target-b"))
	member, err := env.memberRepo.Get(context.Background(), "target-b", umTenantB)
	require.NoError(t, err)
	require.NotNil(t, member, "the other tenant's membership must stay untouched")
	require.Empty(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")))
}

// TestUserManagementPurgeSystemAdminRequiresOwnerHandover：AC-20。超管注销
// owner 前必须先完成空间交接；未交接 403，且空间不得变成无主空间。
func TestUserManagementPurgeSystemAdminRequiresOwnerHandover(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "owner-target", nil)
	umSeedMembership(t, env, "owner-target", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "successor", nil)
	umSeedMembership(t, env, "successor", umTenantA, types.TenantRoleOwner)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)
	err := env.svc.PurgeUser(ctx, umTenantA, "owner-target", "owner-target@example.com", "offboard owner")
	umRequireHTTPStatus(t, err, 403)
	require.True(t, umLoadUser(t, env, "owner-target").IsActive,
		"an owner must not be purged before the handover")

	// 模拟交接完成：目标降为 admin，successor 仍是 owner。
	require.NoError(t, env.memberRepo.UpdateRole(
		context.Background(), "owner-target", umTenantA, types.TenantRoleAdmin))
	require.NoError(t, env.svc.PurgeUser(
		ctx, umTenantA, "owner-target", "owner-target@example.com", "handover done"))

	tomb := umLoadUserUnscoped(t, env, "owner-target")
	require.True(t, tomb.DeletedAt.Valid)
	successor, err := env.memberRepo.Get(context.Background(), "successor", umTenantA)
	require.NoError(t, err)
	require.NotNil(t, successor, "the workspace must keep its new owner after the purge")
	require.Equal(t, types.TenantRoleOwner, successor.Role)
}

// TestUserManagementPurgeRequiresRevokingPrivileges：AC-21。系统管理员 /
// 跨空间超管必须先撤销身份，才能被彻底删除。
func TestUserManagementPurgeRequiresRevokingPrivileges(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "sysadmin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "target-admin", func(u *types.User) { u.IsSystemAdmin = true })
	umSeedMembership(t, env, "target-admin", umTenantA, types.TenantRoleViewer)
	umSeedUser(t, env, "target-cross", func(u *types.User) { u.CanAccessAllTenants = true })
	umSeedMembership(t, env, "target-cross", umTenantA, types.TenantRoleViewer)

	ctx := umCallerCtx("sysadmin", umTenantA, types.TenantRoleViewer, true)

	err := env.svc.PurgeUser(ctx, umTenantA, "target-admin", "target-admin@example.com", "must revoke first")
	umRequireHTTPStatus(t, err, 403)
	err = env.svc.PurgeUser(ctx, umTenantA, "target-cross", "target-cross@example.com", "must revoke first")
	umRequireHTTPStatus(t, err, 403)

	// 撤销身份后允许墓碑化。
	require.NoError(t, env.db.Model(&types.User{}).Where("id = ?", "target-admin").
		Update("is_system_admin", false).Error)
	require.NoError(t, env.svc.PurgeUser(ctx, umTenantA, "target-admin", "target-admin@example.com", "revoked"))
	require.NoError(t, env.db.Model(&types.User{}).Where("id = ?", "target-cross").
		Update("can_access_all_tenants", false).Error)
	require.NoError(t, env.svc.PurgeUser(ctx, umTenantA, "target-cross", "target-cross@example.com", "revoked"))

	require.True(t, umLoadUserUnscoped(t, env, "target-admin").DeletedAt.Valid)
	require.True(t, umLoadUserUnscoped(t, env, "target-cross").DeletedAt.Valid)
	require.False(t, umLoadUserUnscoped(t, env, "target-admin").IsSystemAdmin)
	require.False(t, umLoadUserUnscoped(t, env, "target-cross").CanAccessAllTenants)
}

// TestUserManagementPurgeIsIdempotent：重复 purge 已删除目标锁定为 404，
// 且不重复写审计（本轮口径见 05-tests.md 的 open question）。
func TestUserManagementPurgeIsIdempotent(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)

	ctx := umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false)
	require.NoError(t, env.svc.PurgeUser(ctx, umTenantA, "target", "target@example.com", "first"))

	err := env.svc.PurgeUser(ctx, umTenantA, "target", "target@example.com", "second")
	umRequireHTTPStatus(t, err, 404)
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")), 1,
		"a repeated purge must not append a second success audit row")
}

// TestUserManagementPurgeAndBanDoNotLeaveHalfState：并发 purge/ban 后，
// 最终状态必须是完整墓碑，不能出现"清一半"的账号（T03-8）。
func TestUserManagementPurgeAndBanDoNotLeaveHalfState(t *testing.T) {
	env := newUMPurgeEnv(t)
	umSeedUser(t, env, "actor", nil)
	umSeedMembership(t, env, "actor", umTenantA, types.TenantRoleOwner)
	umSeedUser(t, env, "target", nil)
	umSeedMembership(t, env, "target", umTenantA, types.TenantRoleViewer)
	umSeedPurgeArtifacts(t, env, "target", umTenantA)

	var (
		wg       sync.WaitGroup
		purgeErr error
		banErrs  = make([]error, 2)
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		purgeErr = env.svc.PurgeUser(
			umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
			umTenantA, "target", "target@example.com", "concurrent purge")
	}()
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, banErrs[i] = env.svc.BanUser(
				umCallerCtx("actor", umTenantA, types.TenantRoleOwner, false),
				umTenantA, "target", fmt.Sprintf("concurrent ban %d", i))
		}()
	}
	wg.Wait()

	require.NoError(t, purgeErr, "the purge must win or no-op cleanly against concurrent bans")
	for i, err := range banErrs {
		if err == nil {
			continue
		}
		var appErr *apperrors.AppError
		require.Truef(t, errors.As(err, &appErr),
			"concurrent ban #%d must fail with an AppError, got %T: %v", i, err, err)
		require.Contains(t, []int{403, 404}, appErr.HTTPCode)
	}

	// 最终一致性：墓碑完整、无 active 成员、无可用 token、无会话。
	tomb := umLoadUserUnscoped(t, env, "target")
	require.True(t, tomb.DeletedAt.Valid)
	require.False(t, tomb.IsActive)
	require.Empty(t, tomb.PasswordHash)
	activeMembers, err := env.memberRepo.ListByUser(context.Background(), "target")
	require.NoError(t, err)
	require.Empty(t, activeMembers)
	require.Zero(t, umCount(t, env, &types.AuthToken{},
		"user_id = ? AND is_revoked = ?", "target", false))
	require.Zero(t, umCount(t, env, &types.Session{}, "user_id = ?", "target"))
	require.Len(t, umAuditRows(t, env, umTenantA, types.AuditAction("rbac.member_purged")), 1)
}
