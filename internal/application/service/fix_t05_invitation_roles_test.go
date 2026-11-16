package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T05 红阶段：邀请只分配 admin/viewer，拒绝 owner/contributor；历史非法
// 角色的 pending 邀请在 Accept / AcceptByToken 时被拒绝且不写成员行。
//
// 当前实现只做 IsValid()，owner/contributor 仍可通过邀请与分享链接进入，
// 因此下面的用例预期失败。

func TestT05InvitationRoleWhitelist(t *testing.T) {
	allowed := []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleViewer}
	denied := []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor}

	t.Run("create", func(t *testing.T) {
		for _, role := range allowed {
			t.Run("allow_"+string(role), func(t *testing.T) {
				svc, _, _ := newInvitationSvc()
				inv, err := svc.Create(context.Background(), 1, "u-bob", role, nil, "")
				require.NoError(t, err, "Create must allow %s", role)
				require.NotNil(t, inv)
				require.Equal(t, role, inv.Role)
			})
		}
		for _, role := range denied {
			t.Run("deny_"+string(role), func(t *testing.T) {
				svc, repo, _ := newInvitationSvc()
				inv, err := svc.Create(context.Background(), 1, "u-bob", role, nil, "")
				require.Error(t, err, "Create must reject %s", role)
				require.Nil(t, inv)
				rows, listErr := repo.ListByTenant(context.Background(), 1, true)
				require.NoError(t, listErr)
				require.Empty(t, rows, "rejected role must not persist an invitation")
			})
		}
	})

	t.Run("share_link", func(t *testing.T) {
		for _, role := range allowed {
			t.Run("allow_"+string(role), func(t *testing.T) {
				svc, _, _ := newInvitationSvc()
				inv, token, err := svc.CreateShareLink(context.Background(), 1, role, nil, "")
				require.NoError(t, err, "CreateShareLink must allow %s", role)
				require.NotNil(t, inv)
				require.NotEmpty(t, token)
				require.Equal(t, role, inv.Role)
			})
		}
		for _, role := range denied {
			t.Run("deny_"+string(role), func(t *testing.T) {
				svc, repo, _ := newInvitationSvc()
				inv, token, err := svc.CreateShareLink(context.Background(), 1, role, nil, "")
				require.Error(t, err, "CreateShareLink must reject %s", role)
				require.Nil(t, inv)
				require.Empty(t, token)
				rows, listErr := repo.ListByTenant(context.Background(), 1, true)
				require.NoError(t, listErr)
				require.Empty(t, rows, "rejected share link must not be persisted")
			})
		}
	})
}

func TestT05HistoricalInvitationRoleRejected(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleContributor, types.TenantRoleOwner} {
		role := role
		t.Run("accept_"+string(role), func(t *testing.T) {
			svc, repo, memberSvc := newInvitationSvc()
			ctx := context.Background()
			inv := &types.TenantInvitation{
				TenantID:      1,
				InviteeUserID: "u-bob",
				Role:          role,
				Status:        types.TenantInvitationStatusPending,
				ExpiresAt:     time.Now().Add(time.Hour),
			}
			require.NoError(t, repo.Create(ctx, inv))

			_, err := svc.Accept(ctx, inv.ID, "u-bob")
			require.Error(t, err,
				"accepting a historical %s invitation must be rejected with a readable error", role)

			member, getErr := memberSvc.GetMembership(ctx, "u-bob", 1)
			require.NoError(t, getErr)
			require.Nil(t, member, "rejected historical invitation must not write a membership row")

			row, getErr := repo.GetByID(ctx, inv.ID)
			require.NoError(t, getErr)
			require.NotNil(t, row)
			require.Equal(t, types.TenantInvitationStatusPending, row.Status,
				"rejected acceptance must leave the invitation pending")
		})

		t.Run("accept_by_token_"+string(role), func(t *testing.T) {
			svc, repo, memberSvc := newInvitationSvc()
			ctx := context.Background()
			token := "t05-token-" + string(role)
			inv := &types.TenantInvitation{
				TenantID:      1,
				InviteeUserID: "",
				Token:         token,
				Role:          role,
				Status:        types.TenantInvitationStatusPending,
				ExpiresAt:     time.Now().Add(time.Hour),
			}
			require.NoError(t, repo.Create(ctx, inv))

			_, err := svc.AcceptByToken(ctx, token, "u-alice")
			require.Error(t, err,
				"share link with historical %s role must be rejected", role)

			member, getErr := memberSvc.GetMembership(ctx, "u-alice", 1)
			require.NoError(t, getErr)
			require.Nil(t, member, "rejected share link must not write a membership row")
		})
	}
}
