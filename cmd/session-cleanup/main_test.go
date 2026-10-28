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
		{ID: "u2", Username: "viewer2", Email: "viewer2@example.com", TenantID: 1, IsActive: true},
		{ID: "u3", Username: "admin1", Email: "admin1@example.com", TenantID: 1, IsActive: true},
	}
	require.NoError(t, db.Create(&users).Error)

	members := []*types.TenantMember{
		{UserID: "u1", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: joined},
		{UserID: "u2", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: joined},
		{UserID: "u3", TenantID: 1, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive, JoinedAt: joined},
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

	groupErrs := applyPlan(ctx, currentPlan, deleteBatch)
	require.Empty(t, groupErrs)
	require.Equal(t, []string{"u1", "u2"}, seenOwners, "必须按 (tenant, owner) 分组，逐组换成属主身份")

	deletedIDs, failedIDs, verifyErrs := verifyDeletion(ctx, db, currentPlan.Sessions)
	require.Empty(t, verifyErrs)
	require.Empty(t, failedIDs)
	require.Equal(t, []string{"s1", "s3", "s7"}, deletedIDs)

	// 管理员会话与「仍在绑定里」的会话保持原样。
	for _, id := range []string{"s2", "s4"} {
		_, err := sessionRepo.GetByID(ctx, 1, id)
		require.NoError(t, err, "会话 %s 不应被删除", id)
	}
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
