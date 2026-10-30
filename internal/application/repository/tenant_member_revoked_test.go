package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// tenant_member_revoked_test.go 覆盖「被移除空间」判定所依赖的软删计数:
// 普通 ListByUser 被 GORM 软删过滤隐藏,CountDeletedByUser 必须 Unscoped
// 才看得到被移除的成员关系;同时只统计所属空间仍存在(未软删)的行,
// 避免「用户删掉自己的空间」被误判为被管理员移出。

func seedTenant(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	require.NoError(t, db.Create(&types.Tenant{
		ID:     id,
		Name:   "workspace",
		Status: "active",
	}).Error)
}

func TestTenantMemberRepository_CountDeletedByUser(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantMemberRepository(db)
	seedTenant(t, db, 1)

	member := &types.TenantMember{
		UserID:   "removed-user",
		TenantID: 1,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, repo.Create(ctx, member))
	require.NoError(t, repo.SoftDelete(ctx, "removed-user", 1))

	active, err := repo.ListByUser(ctx, "removed-user")
	require.NoError(t, err)
	require.Empty(t, active, "ListByUser must hide soft-deleted rows")

	deleted, err := repo.CountDeletedByUser(ctx, "removed-user")
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	other, err := repo.CountDeletedByUser(ctx, "other-user")
	require.NoError(t, err)
	require.Zero(t, other)
}

func TestTenantMemberRepository_CountDeletedByUser_IgnoresActiveRows(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantMemberRepository(db)
	seedTenant(t, db, 1)

	require.NoError(t, repo.Create(ctx, &types.TenantMember{
		UserID:   "active-user",
		TenantID: 1,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
	}))

	deleted, err := repo.CountDeletedByUser(ctx, "active-user")
	require.NoError(t, err)
	require.Zero(t, deleted)
}

func TestTenantMemberRepository_CountDeletedByUser_IgnoresDeletedTenant(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantMemberRepository(db)
	seedTenant(t, db, 1)

	require.NoError(t, repo.Create(ctx, &types.TenantMember{
		UserID:   "removed-user",
		TenantID: 1,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
	}))
	require.NoError(t, repo.SoftDelete(ctx, "removed-user", 1))

	// 删除空间会级联软删成员;这些行不代表用户被管理员移出,不能计入。
	require.NoError(t, db.Delete(&types.Tenant{}, 1).Error)

	deleted, err := repo.CountDeletedByUser(ctx, "removed-user")
	require.NoError(t, err)
	require.Zero(t, deleted)
}
