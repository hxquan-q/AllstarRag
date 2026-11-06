package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// T26 红阶段：审计日志关键词搜索。
//
// 契约（给②实现）：
//   - interfaces.AuditLogQuery 新增 Search string；handler 从 ?q= 读取并 TrimSpace。
//   - 仓储 List 在 Search 非空时按以下任一命中：
//     action LIKE %q%（子串）、actor_user_id 或 target_user_id 属于
//     “username 或 email 包含 q”的用户集合。
//   - 大小写不敏感；%, _ 按字面匹配（LIKE 必须转义）；只作用于本租户；
//     与 after_id / action / outcome / actor / scope / UnscopedOnly 可叠加。
//   - 不搜索 details / request_path，避免把敏感内容带进匹配面。
func newAuditSearchTestRepo(t *testing.T) (interfaces.AuditLogRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&types.User{}, &types.AuditLog{}))
	return NewAuditLogRepository(db), db
}

func seedAuditSearchUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Create(&types.User{
		ID: "u-alice", Username: "alice", Email: "alice@example.com",
		PasswordHash: "x", IsActive: true, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&types.User{
		ID: "u-bob", Username: "bob", Email: "bob@example.com",
		PasswordHash: "x", IsActive: true, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&types.User{
		ID: "u-carol", Username: "carol", Email: "carol@example.com",
		PasswordHash: "x", IsActive: true, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

// seedAuditSearchRows inserts rows in id order and returns their IDs by key.
func seedAuditSearchRows(t *testing.T, db *gorm.DB) map[string]uint64 {
	t.Helper()
	rows := []*types.AuditLog{
		{TenantID: 7, ActorUserID: "u-alice", Action: types.AuditActionMemberAdded, TargetUserID: "u-bob", Outcome: types.AuditOutcomeSuccess},
		{TenantID: 7, ActorUserID: "u-bob", Action: types.AuditActionMemberBanned, TargetUserID: "u-alice", Outcome: types.AuditOutcomeSuccess},
		{TenantID: 7, ActorUserID: "u-alice", Action: types.AuditActionMemberPurged, TargetUserID: "u-carol", Outcome: types.AuditOutcomeSuccess},
		{TenantID: 8, ActorUserID: "u-alice", Action: types.AuditActionMemberAdded, TargetUserID: "u-carol", Outcome: types.AuditOutcomeSuccess},
	}
	ids := map[string]uint64{}
	for _, row := range rows {
		require.NoError(t, db.Create(row).Error)
	}
	ids["alice-added"] = rows[0].ID
	ids["bob-banned"] = rows[1].ID
	ids["alice-purged"] = rows[2].ID
	ids["tenant8"] = rows[3].ID
	return ids
}

func auditIDs(entries []*types.AuditLog) []uint64 {
	out := make([]uint64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// 命中 actor 用户名，且不跨租户。
func TestAuditLogListSearchMatchesActorUsername(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "alice", UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]uint64{ids["alice-added"], ids["bob-banned"], ids["alice-purged"]},
		auditIDs(entries), "actor/target 命中 alice 的三行；tenant 8 的行不得出现")
}

// 命中 target 邮箱；大小写不敏感。
func TestAuditLogListSearchMatchesTargetEmailCaseInsensitive(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "BOB@EXAMPLE.COM", UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint64{ids["alice-added"], ids["bob-banned"]}, auditIDs(entries))
}

// q 也能命中 action 子串（如 banned 命中 rbac.member_banned）。
func TestAuditLogListSearchMatchesActionSubstring(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "BANNED", UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []uint64{ids["bob-banned"]}, auditIDs(entries))
}

// 与既有 action 过滤、游标分页叠加。
func TestAuditLogListSearchComposesWithActionAndCursor(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "alice", Action: types.AuditActionMemberBanned, UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []uint64{ids["bob-banned"]}, auditIDs(entries))

	entries, err = repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "alice", AfterID: ids["alice-purged"], UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint64{ids["alice-added"], ids["bob-banned"]}, auditIDs(entries),
		"after_id 之后只返回更老的命中行")
}

// % 与 _ 必须按字面匹配，不能当通配符。
func TestAuditLogListSearchEscapesLikeWildcards(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	// 通配符字符必须按字面匹配，而不是当作 LIKE 的通配符：
	// 字面 "%" 不应该匹配任意行，字面 "al_ce" 也不应匹配 "alice"。
	for _, keyword := range []string{"%", "al%ce", "al_ce", "100%"} {
		entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
			Search: keyword, UnscopedOnly: true,
		})
		require.NoError(t, err)
		assert.Empty(t, entries, "keyword %q must be treated literally", keyword)
	}

	// 反例：字面 "_" 必须能命中本身含下划线的 action（rbac.member_*），
	// 证明转义后仍保留正常子串匹配能力，而不是把 "_" 当通配符。
	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{
		Search: "_", UnscopedOnly: true,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]uint64{ids["alice-added"], ids["bob-banned"], ids["alice-purged"]},
		auditIDs(entries),
		"literal underscore must match actions containing underscore, tenant 8 excluded")
}

// 空 Search 保持原行为（零回归）。
func TestAuditLogListSearchEmptyKeepsLegacyBehavior(t *testing.T) {
	repo, db := newAuditSearchTestRepo(t)
	seedAuditSearchUsers(t, db)
	ids := seedAuditSearchRows(t, db)

	entries, err := repo.List(context.Background(), 7, &interfaces.AuditLogQuery{UnscopedOnly: true})
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]uint64{ids["alice-added"], ids["bob-banned"], ids["alice-purged"]},
		auditIDs(entries))
}
