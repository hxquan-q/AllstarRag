package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// tenant_member_revoked_test.go 覆盖「被移除空间」判定所依赖的软删计数:
// 普通 ListByUser 被 GORM 软删过滤隐藏,CountDeletedByUser 必须 Unscoped
// 才看得到被移除的成员关系。

func TestTenantMemberRepository_CountDeletedByUser(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTenantMemberRepository(db)

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
