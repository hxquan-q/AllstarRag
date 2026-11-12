package middleware

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// T03 / CP3 B3：孤儿空间自愈收紧为「严格孤儿」。
//
//   - 空间已有 active owner → 跳过自愈，不新增成员；
//   - 空间仍有任何 active 成员（即使 0 owner）→ 跳过自愈，fail-closed，
//     由超管处理；不能把任意 home-tenant 用户补成 owner；
//   - 空间没有任何 active 成员 → 允许把一个 home-tenant 用户补为 owner。

// countT03ActiveOwners 从测试替身的成员表推导 active owner 数，
// 不依赖生产接口新增计数方法。
func countT03ActiveOwners(svc *fakeMemberService, tenantID uint64) int {
	n := 0
	for _, m := range svc.members {
		if m.TenantID == tenantID &&
			m.Status == types.TenantMemberStatusActive &&
			m.Role == types.TenantRoleOwner {
			n++
		}
	}
	return n
}

// CountActiveOwners 让测试替身可被未来的窄接口类型断言识别；
// 生产接口当前没有该方法时也不影响编译。
func (f *fakeMemberService) CountActiveOwners(ctx context.Context, tenantID uint64) (int64, error) {
	return int64(countT03ActiveOwners(f, tenantID)), nil
}

func TestT03OrphanSpaceSelfHeal(t *testing.T) {
	t.Run("active_owner_skips_self_heal", func(t *testing.T) {
		svc := newFakeMemberService()
		svc.seedActive("existing-owner", 7, types.TenantRoleOwner)
		user := &types.User{ID: "u1", TenantID: 7}

		got, ok := resolveTenantRole(context.Background(), svc, user, 7, false, cfgWithRBAC(true))

		require.False(t, ok,
			"a home-tenant user without membership must be rejected when the space already has an owner")
		require.Empty(t, got)
		require.Empty(t, svc.addCalls,
			"an owned space must not auto-add a second owner")
		require.Equal(t, 1, countT03ActiveOwners(svc, 7),
			"the workspace must keep exactly one active owner")
		_, exists := svc.members[memberKey("u1", 7)]
		require.False(t, exists, "self-heal must not create a membership row for the caller")
	})

	t.Run("zero_active_owners_with_active_member_does_not_self_heal", func(t *testing.T) {
		svc := newFakeMemberService()
		// 空间里有活跃成员，但没有 owner（例如存量 degraded 数据）。
		// 严格孤儿口径：只要还有 active 成员就必须 fail-closed，交给超管，
		// 不能把任意 home-tenant 用户补成 owner（CP3 B3 / N22）。
		svc.seedActive("existing-viewer", 7, types.TenantRoleViewer)
		user := &types.User{ID: "u1", TenantID: 7}

		got, ok := resolveTenantRole(context.Background(), svc, user, 7, false, cfgWithRBAC(true))

		require.False(t, ok,
			"a space that still has active members but no owner must fail closed")
		require.Empty(t, got)
		require.Empty(t, svc.addCalls,
			"self-heal must not promote a user when the space still has active members")
		require.Equal(t, 0, countT03ActiveOwners(svc, 7),
			"self-heal must not create an owner in a non-empty space")
		_, exists := svc.members[memberKey("u1", 7)]
		require.False(t, exists, "self-heal must not create a membership row for the caller")
	})

	t.Run("empty_space_adds_owner", func(t *testing.T) {
		svc := newFakeMemberService()
		user := &types.User{ID: "u1", TenantID: 7}

		got, ok := resolveTenantRole(context.Background(), svc, user, 7, false, cfgWithRBAC(true))

		require.True(t, ok)
		require.Equal(t, types.TenantRoleOwner, got)
		require.Len(t, svc.addCalls, 1)
		require.Equal(t, 1, countT03ActiveOwners(svc, 7))
	})
}
