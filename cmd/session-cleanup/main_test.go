package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// newCleanupTestDB 建一个进程内 SQLite，只迁移本票用到的三张表。
func newCleanupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.TenantMember{}, &types.Session{}))
	return db
}

// seedCleanupFixture 造一组覆盖全部筛选分支的数据：
//
//	u1 viewer，绑定 agent-a
//	u2 viewer，零绑定
//	u3 admin
//	s1 u1 无产线          -> 删
//	s2 u1 产线 agent-a    -> 留（在绑定里）
//	s3 u1 产线 agent-z    -> 删（不在绑定里）
//	s4 u3 无产线          -> 留（管理员会话一律不动）
//	s5 u1 无产线但已软删  -> 留
//	s6 属主为空无产线     -> 留（不能算到任何 viewer 头上）
//	s7 u2 产线任意        -> 删（零绑定 = 全部可删）
//	s8 u1 assistant 无产线 -> 留（助手抽屉历史不是普通会话）
//	s9 u1 agent_delegated 无产线 -> 留（委派子会话转录不是普通会话）
func seedCleanupFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	joined := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	users := []*types.User{
		{
			ID: "u1", Username: "viewer1", Email: "viewer1@example.com", TenantID: 1, IsActive: true,
			Preferences: types.UserPreferences{
				BoundAgentIDsByTenant: map[string][]string{"1": {"agent-a"}},
			},
		},
		{
			ID: "u2", Username: "viewer2", Email: "viewer2@example.com", TenantID: 1, IsActive: true,
			// 在别的租户有绑定、在本租户没有，验证「按工作区分键」。
			Preferences: types.UserPreferences{
				BoundAgentIDsByTenant: map[string][]string{"2": {"agent-b"}},
			},
		},
		{
			ID: "u3", Username: "admin1", Email: "admin1@example.com", TenantID: 1, IsActive: true,
			Preferences: types.UserPreferences{
				BoundAgentIDsByTenant: map[string][]string{"1": {"agent-a"}},
			},
		},
	}
	require.NoError(t, db.Create(&users).Error)

	members := []*types.TenantMember{
		{
			UserID: "u1", TenantID: 1, Role: types.TenantRoleViewer,
			Status: types.TenantMemberStatusActive, JoinedAt: joined,
		},
		{
			UserID: "u2", TenantID: 1, Role: types.TenantRoleViewer,
			Status: types.TenantMemberStatusActive, JoinedAt: joined,
		},
		{
			UserID: "u3", TenantID: 1, Role: types.TenantRoleAdmin,
			Status: types.TenantMemberStatusActive, JoinedAt: joined,
		},
	}
	require.NoError(t, db.Create(&members).Error)

	sessions := []*types.Session{
		{ID: "s1", TenantID: 1, UserID: "u1", Title: "no line"},
		{ID: "s2", TenantID: 1, UserID: "u1", Title: "bound line", ProductLine: "agent-a"},
		{ID: "s3", TenantID: 1, UserID: "u1", Title: "stale line", ProductLine: "agent-z"},
		{ID: "s4", TenantID: 1, UserID: "u3", Title: "admin no line"},
		{ID: "s5", TenantID: 1, UserID: "u1", Title: "already deleted"},
		{ID: "s6", TenantID: 1, UserID: "", Title: "legacy tenant level"},
		{ID: "s7", TenantID: 1, UserID: "u2", Title: "zero binding", ProductLine: "anything"},
		{ID: "s8", TenantID: 1, UserID: "u1", Title: "assistant drawer", Kind: types.SessionKindAssistant},
		{
			ID: "s9", TenantID: 1, UserID: "u1", Title: "delegated transcript",
			Kind: types.SessionKindAgentDelegated,
		},
	}
	for _, session := range sessions {
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(session).Error)
	}
	require.NoError(t, db.Delete(&types.Session{}, "id = ?", "s5").Error)
}

func TestLoadScanDataAndBuildPlanApplyFilterRules(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)
	ctx := context.Background()

	data, err := loadScanData(ctx, db, 1)
	require.NoError(t, err)
	require.Len(t, data.Members, 2, "只应出现 viewer 活跃成员，admin 不在内")

	currentPlan := buildPlan(data)
	require.Equal(t, []string{"s1", "s3", "s7"}, sessionIDs(currentPlan.Sessions))
	require.NotContains(t, sessionIDs(currentPlan.Sessions), "s8", "assistant 抽屉历史不能删")
	require.NotContains(t, sessionIDs(currentPlan.Sessions), "s9", "委派子会话转录不能删")
	require.Equal(t, []string{"1/u2"}, ownerStrings(currentPlan.ZeroBindingOwners))
	require.Equal(t, 1, currentPlan.ZeroBindingSessionCount)
	require.Equal(t, 2, len(currentPlan.ByOwner[ownerKey{TenantID: 1, UserID: "u1"}]))
	require.Equal(t, 1, len(currentPlan.ByOwner[ownerKey{TenantID: 1, UserID: "u2"}]))
}

func TestAdminSessionNeverSelected(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)

	data, err := loadScanData(context.Background(), db, 1)
	require.NoError(t, err)
	currentPlan := buildPlan(data)
	for _, session := range currentPlan.Sessions {
		require.NotEqual(t, "u3", session.UserID, "管理员会话绝不能被选中")
	}
}

func TestNonOrdinarySessionKindsExcluded(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)

	data, err := loadScanData(context.Background(), db, 1)
	require.NoError(t, err)
	currentPlan := buildPlan(data)

	ids := sessionIDs(currentPlan.Sessions)
	require.NotContains(t, ids, "s8", "assistant 会话不属于清理范围")
	require.NotContains(t, ids, "s9", "agent_delegated 会话不属于清理范围")
	require.Contains(t, ids, "s1", "同用户的普通会话仍要清理")
	for _, session := range currentPlan.Sessions {
		require.Equal(t, "", session.Kind, "候选清单里只能有普通网页会话")
	}
}

func TestOwnerContextDrivesVisibility(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)
	ctx := context.Background()
	sessionRepo := repository.NewSessionRepository(db)

	// 正确：以属主身份可见。
	viewerCtx := ownerContext(ctx, 1, "u1")
	require.Equal(t, uint64(1), types.MustTenantIDFromContext(viewerCtx))
	require.Equal(t, "u1", types.SessionOwnerIDFromContext(viewerCtx))
	_, err := sessionRepo.Get(viewerCtx, 1, "u1", "s1")
	require.NoError(t, err)

	// 错误：用别人的身份看不到；普通 web 会话没有 admin 兜底。
	adminCtx := ownerContext(ctx, 1, "u3")
	_, err = sessionRepo.Get(adminCtx, 1, "u3", "s1")
	require.ErrorIs(t, err, apperrors.ErrSessionNotFound)
}

func TestApplyPlanDeletesViewerSessionsAndKeepsAdminSessions(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)
	ctx := context.Background()

	data, err := loadScanData(ctx, db, 1)
	require.NoError(t, err)
	currentPlan := buildPlan(data)

	sessionRepo := repository.NewSessionRepository(db)
	var seenOwners []string
	// 模拟 BatchDeleteSessions 的核心两步：先用上下文身份判可见，再按 owner 批删。
	deleteBatch := func(groupCtx context.Context, ids []string) error {
		tenantID := types.MustTenantIDFromContext(groupCtx)
		ownerID := types.SessionOwnerIDFromContext(groupCtx)
		seenOwners = append(seenOwners, ownerID)
		visible := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, err := sessionRepo.Get(groupCtx, tenantID, ownerID, id); err != nil {
				continue
			}
			visible = append(visible, id)
		}
		if len(visible) == 0 {
			return apperrors.ErrSessionNotFound
		}
		_, err := sessionRepo.BatchDelete(groupCtx, tenantID, ownerID, visible)
		return err
	}

	// 生产同款删除前复核：真实重读绑定（此处与扫描一致，复核不会改变结果）。
	reload := func(reloadCtx context.Context, key ownerKey) ([]string, error) {
		return reloadOwnerBindings(reloadCtx, db, key)
	}
	groupErrs, attempted, rebindKept := applyPlan(ctx, currentPlan, reload, deleteBatch)
	require.Empty(t, groupErrs)
	require.Equal(t, 0, rebindKept, "绑定没变化时复核不应保留任何会话")
	require.Equal(t, []string{"u1", "u2"}, seenOwners, "必须按 (tenant, owner) 分组，逐组换成属主身份")

	deletedIDs, failedIDs, verifyErrs := verifyDeletion(ctx, db, attempted)
	require.Empty(t, verifyErrs)
	require.Empty(t, failedIDs)
	require.Equal(t, []string{"s1", "s3", "s7"}, deletedIDs)

	// 管理员会话、「仍在绑定里」的会话、助手/委派会话都保持原样。
	for _, id := range []string{"s2", "s4", "s8", "s9"} {
		_, err := sessionRepo.GetByID(ctx, 1, id)
		require.NoError(t, err, "会话 %s 不应被删除", id)
	}
}

// F9：扫描与删除之间用户补绑了产线，对应会话必须在删除前复核时被保留；
// 整组都被保留时该组直接跳过（deleteBatch 不再被调用）。
func TestApplyPlanRechecksBindingsBeforeDelete(t *testing.T) {
	db := newCleanupTestDB(t)
	seedCleanupFixture(t, db)
	ctx := context.Background()

	data, err := loadScanData(ctx, db, 1)
	require.NoError(t, err)
	currentPlan := buildPlan(data)
	require.Equal(t, []string{"s1", "s3", "s7"}, sessionIDs(currentPlan.Sessions))

	// 模拟扫描后、删除前用户改了绑定：u1 绑上 agent-z（保住 s3），
	// u2 在本租户从零绑定变为绑定 anything（保住 s7，整组清空）。
	rebind := func(userID string, tenantKey string, ids []string) {
		var user types.User
		require.NoError(t, db.Where("id = ?", userID).First(&user).Error)
		user.Preferences.BoundAgentIDsByTenant[tenantKey] = ids
		require.NoError(t, db.Save(&user).Error)
	}
	rebind("u1", "1", []string{"agent-a", "agent-z"})
	rebind("u2", "1", []string{"anything"})

	sessionRepo := repository.NewSessionRepository(db)
	var seenOwners []string
	deleteBatch := func(groupCtx context.Context, ids []string) error {
		tenantID := types.MustTenantIDFromContext(groupCtx)
		ownerID := types.SessionOwnerIDFromContext(groupCtx)
		seenOwners = append(seenOwners, ownerID)
		_, err := sessionRepo.BatchDelete(groupCtx, tenantID, ownerID, ids)
		return err
	}

	groupErrs, attempted, rebindKept := applyPlan(
		ctx, currentPlan,
		func(reloadCtx context.Context, key ownerKey) ([]string, error) {
			return reloadOwnerBindings(reloadCtx, db, key)
		},
		deleteBatch)

	require.Empty(t, groupErrs)
	require.Equal(t, []string{"s1"}, sessionIDs(attempted), "只有 s1 仍满足清理条件")
	require.Equal(t, 2, rebindKept, "s3 与 s7 因删除前复核被保留")
	require.Equal(t, []string{"u1"}, seenOwners, "u2 的组过滤后为空，必须整组跳过")

	// s3 / s7 仍在库里（被新绑定保住），s1 已删。
	deletedIDs, failedIDs, verifyErrs := verifyDeletion(ctx, db, attempted)
	require.Empty(t, verifyErrs)
	require.Empty(t, failedIDs)
	require.Equal(t, []string{"s1"}, deletedIDs)
	for _, id := range []string{"s3", "s7"} {
		_, err := sessionRepo.GetByID(ctx, 1, id)
		require.NoError(t, err, "删除前补绑定的会话 %s 不应被删除", id)
	}
}

// F10：--apply 必须显式传绝对路径 --audit-dir；dry-run 不受影响。
func TestParseOptionsAuditDirGate(t *testing.T) {
	_, err := parseOptions([]string{"--apply", "--tenant", "1", "--confirm", "0"})
	require.ErrorContains(t, err, "--audit-dir", "apply 未显式传 audit-dir 必须拒绝")

	_, err = parseOptions([]string{"--apply", "--tenant", "1", "--confirm", "0", "--audit-dir", "audits"})
	require.ErrorContains(t, err, "绝对路径", "apply 传相对路径必须拒绝")

	opts, err := parseOptions([]string{"--apply", "--tenant", "1", "--confirm", "0", "--audit-dir", "/var/audits/"})
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(opts.auditDir))
	require.Equal(t, "/var/audits", opts.auditDir, "保存 Clean 后的绝对路径")

	// dry-run 不受影响：默认目录照常可用。
	opts, err = parseOptions(nil)
	require.NoError(t, err)
	require.False(t, opts.apply)
	require.Equal(t, ".", opts.auditDir)
}

func TestCheckConfirmRejectsMismatch(t *testing.T) {
	require.Error(t, checkConfirm(5, 3))
	require.Error(t, checkConfirm(0, 3))
	require.NoError(t, checkConfirm(3, 3))
	require.NoError(t, checkConfirm(0, 0))
}

func TestAuditFileGuard(t *testing.T) {
	dir := t.TempDir()

	found, err := findAuditFile(dir, 7)
	require.NoError(t, err)
	require.Empty(t, found)

	path, err := writeAudit(dir, auditRecord{TenantID: 7, TotalCandidates: 1})
	require.NoError(t, err)
	require.FileExists(t, path)
	require.Equal(t, filepath.Join(dir, filepath.Base(path)), path)
	require.Contains(t, filepath.Base(path), "session-cleanup-7-")

	found, err = findAuditFile(dir, 7)
	require.NoError(t, err)
	require.Equal(t, path, found, "同工作区已有审计文件必须被识别")

	found, err = findAuditFile(dir, 8)
	require.NoError(t, err)
	require.Empty(t, found, "其他工作区不受影响")

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), `"tenant_id": 7`)
}

func TestParseOptionsSafetyGates(t *testing.T) {
	opts, err := parseOptions(nil)
	require.NoError(t, err)
	require.False(t, opts.apply, "默认必须是 dry-run")

	_, err = parseOptions([]string{"--apply", "--tenant", "1"})
	require.ErrorContains(t, err, "--confirm")

	_, err = parseOptions([]string{"--apply", "--confirm", "0"})
	require.ErrorContains(t, err, "--tenant")

	_, err = parseOptions([]string{"--apply", "--dry-run", "--tenant", "1", "--confirm", "0"})
	require.ErrorContains(t, err, "不能同时使用")

	opts, err = parseOptions([]string{"--tenant", "9"})
	require.NoError(t, err)
	require.False(t, opts.apply)
	require.Equal(t, uint64(9), opts.tenantID)
}

func sessionIDs(sessions []*types.Session) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func ownerStrings(keys []ownerKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key.String())
	}
	return out
}
