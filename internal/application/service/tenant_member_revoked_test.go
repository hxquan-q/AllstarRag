package service

import (
	"context"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// tenant_member_revoked_test.go 覆盖 HasRevokedAccess 的判定:
// 活跃成员为 0 且存在软删成员关系才算被移除;有一个活跃成员即不受影响;
// 系统管理员 / 跨租户超管永远返回 false。

func setupRevokedAccessService(
	t *testing.T,
	userRepo interfaces.UserRepository,
) (interfaces.TenantMemberService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.TenantMember{}))
	repo := apprepo.NewTenantMemberRepository(db)
	return NewTenantMemberService(repo, nil, userRepo, nil), db
}

type revokedAccessUserRepo struct {
	interfaces.UserRepository
	user *types.User
}

func (r *revokedAccessUserRepo) GetUserByID(context.Context, string) (*types.User, error) {
	return r.user, nil
}

func addMembership(t *testing.T, db *gorm.DB, userID string, tenantID uint64, deleted bool) {
	t.Helper()
	// CountDeletedByUser 只统计所属空间仍存在的行,所以测试必须先把空间建出来。
	require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&types.Tenant{
		ID:     tenantID,
		Name:   "workspace",
		Status: "active",
	}).Error)
	member := &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
	}
	require.NoError(t, db.Create(member).Error)
	if deleted {
		require.NoError(t, db.Delete(&types.TenantMember{}, member.ID).Error)
	}
}

func TestHasRevokedAccess_MembershipStates(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		members func(t *testing.T, db *gorm.DB)
		want    bool
	}{
		{
			name:    "brand new user without memberships",
			members: func(*testing.T, *gorm.DB) {},
			want:    false,
		},
		{
			name: "active membership",
			members: func(t *testing.T, db *gorm.DB) {
				addMembership(t, db, "user-1", 1, false)
			},
			want: false,
		},
		{
			name: "active membership plus a removed one",
			members: func(t *testing.T, db *gorm.DB) {
				addMembership(t, db, "user-1", 1, true)
				addMembership(t, db, "user-1", 2, false)
			},
			want: false,
		},
		{
			name: "all memberships removed",
			members: func(t *testing.T, db *gorm.DB) {
				addMembership(t, db, "user-1", 1, true)
				addMembership(t, db, "user-1", 2, true)
			},
			want: true,
		},
		{
			name: "other user's removed membership is irrelevant",
			members: func(t *testing.T, db *gorm.DB) {
				addMembership(t, db, "other-user", 1, true)
			},
			want: false,
		},
		{
			// 删除空间会级联软删成员,但那是用户自己删掉的空间,
			// 不等于被管理员移出,不应阻止其再建空间。
			name: "workspace deleted with cascade membership removal",
			members: func(t *testing.T, db *gorm.DB) {
				addMembership(t, db, "user-1", 1, true)
				require.NoError(t, db.Delete(&types.Tenant{}, 1).Error)
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, db := setupRevokedAccessService(t, nil)
			tt.members(t, db)
			got, err := svc.HasRevokedAccess(ctx, "user-1")
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHasRevokedAccess_PrivilegedUsersExempt(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		user *types.User
	}{
		{name: "system admin", user: &types.User{ID: "user-1", IsSystemAdmin: true}},
		{name: "cross-tenant superuser", user: &types.User{ID: "user-1", CanAccessAllTenants: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setupRevokedAccessService(t, &revokedAccessUserRepo{user: tc.user})
			addMembership(t, db, "user-1", 1, true)

			got, err := svc.HasRevokedAccess(ctx, "user-1")
			require.NoError(t, err)
			require.False(t, got)
		})
	}
}

func TestHasRevokedAccess_RequiresRemovedRow(t *testing.T) {
	// 活跃 0 但没有软删行(全新账号/从没加入过任何空间)不算被移除,
	// 否则新注册用户会被误伤,无法创建自己的第一个空间。
	ctx := context.Background()
	svc, _ := setupRevokedAccessService(t, nil)

	got, err := svc.HasRevokedAccess(ctx, "never-joined")
	require.NoError(t, err)
	require.False(t, got)
}
