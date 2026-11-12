package service

// T42 TDD 红阶段：平台解封的自目标保护（GPT 复核 #3）。
//
// 票面验收 2（03-tickets/T42.md）：
//   - 系统管理员对自身 user_id 调 PlatformUnbanUser → 403；
//   - 目标状态零副作用（不复活、不写审计）；
//   - 覆盖“自身已被并发 ban 置为 is_active=false”的确定性场景：登录时
//     有效的解封请求在 ban 提交后才执行，也不得把自己解封回去。
//
// 使用真实 SQLite 仓储（newUMTestEnv），权限上下文沿用 T35 的
// t35SystemAdminCtx（system_admin=true 的调用者上下文）。

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// S1：自身已被封禁（is_active=false）时，平台解封自己必须 403 且保持
// 封禁状态、不得写平台 unban 审计。
func TestT42PlatformUnbanRejectsBannedSelfWithoutSideEffects(t *testing.T) {
	env := newUMTestEnv(t)
	// “最后一名活跃系统管理员”保护会拒绝把唯一管理员置为 inactive；
	// 先种一个活跃管理员，才能把调用者本身做成“已被并发 ban”的状态。
	umSeedUser(t, env, "backup-admin", func(u *types.User) {
		u.IsSystemAdmin = true
	})
	umSeedUser(t, env, "sysadmin", func(u *types.User) {
		u.IsSystemAdmin = true
		u.IsActive = false
	})
	require.False(t, umLoadUser(t, env, "sysadmin").IsActive,
		"fixture: the caller starts banned, as if a concurrent admin ban just committed")

	ctx := t35SystemAdminCtx("sysadmin")
	_, err := env.svc.PlatformUnbanUser(ctx, "sysadmin", "self unban")
	umRequireHTTPStatus(t, err, 403)

	require.False(t, umLoadUser(t, env, "sysadmin").IsActive,
		"a rejected self-unban must not reactivate the caller")
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberUnbanned),
		"a rejected self-unban must not write a platform audit row")
}

// S2：自身 active 时，平台解封自己同样 403，且不产生任何审计行。
func TestT42PlatformUnbanRejectsActiveSelfWithoutAudit(t *testing.T) {
	env := newUMTestEnv(t)
	umSeedUser(t, env, "sysadmin", func(u *types.User) {
		u.IsSystemAdmin = true
	})

	ctx := t35SystemAdminCtx("sysadmin")
	_, err := env.svc.PlatformUnbanUser(ctx, "sysadmin", "self unban")
	umRequireHTTPStatus(t, err, 403)

	require.True(t, umLoadUser(t, env, "sysadmin").IsActive)
	require.Empty(t, umAuditRows(t, env, 0, types.AuditActionMemberUnbanned),
		"a rejected self-unban must not write a platform audit row")
}
