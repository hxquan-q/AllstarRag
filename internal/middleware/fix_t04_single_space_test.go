package middleware

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T04 红阶段：认证中间件的孤儿空间自愈必须服从「一号一空间」。
//
// 场景：用户 U 在 101 已是 active 成员，但 users.tenant_id 指向 102，
// 且 102 是没有任何成员的空空间。当前实现只按「目标空间 0 个 active
// owner」判断，会把 U 补成 102 的 owner，从而产生第二条成员关系。
//
// 新行为：自愈前必须先看 U 是否已属于其他空间（active/invited）；已有
// 归属则跳过自愈。无任何成员时的基线自愈仍需保留。

func TestT04SelfHealRespectsSingleSpace(t *testing.T) {
	t.Run("existing_membership_elsewhere_skips_self_heal", func(t *testing.T) {
		svc := newFakeMemberService()
		svc.seedActive("u1", 101, types.TenantRoleViewer)
		user := &types.User{ID: "u1", TenantID: 102}

		got, ok := resolveTenantRole(
			context.Background(), svc, user, 102, false, cfgWithRBAC(true))

		require.False(t, ok,
			"a user who already belongs to 101 must not be auto-healed into 102")
		require.Empty(t, got)
		require.Empty(t, svc.addCalls,
			"self-heal must not attempt to add a membership for a user who belongs elsewhere")
		_, exists := svc.members[memberKey("u1", 102)]
		require.False(t, exists, "no membership row may be created for 102")

		rows, err := svc.ListByUser(context.Background(), "u1")
		require.NoError(t, err)
		require.Len(t, rows, 1, "the user must still belong only to 101")
		require.EqualValues(t, 101, rows[0].TenantID)
	})

	t.Run("no_membership_still_self_heals", func(t *testing.T) {
		svc := newFakeMemberService()
		user := &types.User{ID: "u1", TenantID: 102}

		got, ok := resolveTenantRole(
			context.Background(), svc, user, 102, false, cfgWithRBAC(true))

		require.True(t, ok, "the orphan-space baseline self-heal must keep working")
		require.Equal(t, types.TenantRoleOwner, got)
		require.Len(t, svc.addCalls, 1)
		require.Equal(t, types.TenantRoleOwner, svc.addCalls[0].Role)
	})
}
