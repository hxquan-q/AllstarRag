package service

// 管理端账号动作与云之家员工目录之间的反向钩子（T06 / R4）。
//
// 三参构造保持不变，依赖由容器/测试用可选 setter 注入；nil 时调用方
// 保持旧行为。钩子不受 YunzhijiaSSO.Enabled 门控：即使同步关闭，人工
// 封禁/停用/删除也必须让员工目录进入正确状态，否则恢复同步后会被
// 自动重建或解封。
//
// R4 起钩子先看已加载 users 行的 preferences.yunzhijia_employee 标记：
// 非员工直接跳过，不再查询员工目录；这样迁移缺失或员工表不可用时，
// 普通账号的封禁/停用/删除不受影响。员工操作仍然 fail-closed。

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// WithYunzhijiaEmployeeState 注入员工目录反向标记；nil 时保持旧行为。
func (s *userManagementService) WithYunzhijiaEmployeeState(
	state interfaces.YunzhijiaEmployeeState,
) {
	s.yunzhijiaState = state
}

// WithYunzhijiaEmployeeState 注入员工目录反向标记；nil 时保持旧行为。
func (s *userService) WithYunzhijiaEmployeeState(state interfaces.YunzhijiaEmployeeState) {
	s.yunzhijiaState = state
}

// notifyYunzhijiaAdminDisabled 在封禁/停用前清 disabled_by_sync。
//
// 返回清除前的标记值，供封禁失败时补偿。先标记再封禁：标记失败直接
// 返回错误，调用方不得继续封禁，否则会留下「已封禁、标记仍为 true」
// 的账号被下次同步当成同步停用自动解封（INV-4）。
func notifyYunzhijiaAdminDisabled(
	ctx context.Context,
	state interfaces.YunzhijiaEmployeeState,
	user *types.User,
) (bool, error) {
	if state == nil || !isYunzhijiaEmployeeUser(user) {
		return false, nil
	}
	wasDisabled, err := state.MarkAdminDisabled(ctx, user.ID)
	if err != nil {
		logger.Warnf(ctx,
			"yunzhijia: 封禁前标记 disabled_by_sync 失败（未封禁）: user=%s err=%v",
			user.ID, err)
		return false, fmt.Errorf("yunzhijia: 标记员工管理员停用失败: %w", err)
	}
	return wasDisabled, nil
}

// restoreYunzhijiaAdminDisabled 在封禁事务失败后恢复清除前的
// disabled_by_sync，避免留下「账号仍停用、标记已清」后再入职不自动恢复
// 的不可恢复态（AC-11）。补偿尽力而为，失败只记日志。
func restoreYunzhijiaAdminDisabled(
	ctx context.Context,
	state interfaces.YunzhijiaEmployeeState,
	userID string,
	wasDisabled bool,
) {
	if state == nil || !wasDisabled {
		return
	}
	if err := state.RestoreAdminDisabled(ctx, userID, true); err != nil {
		logger.Warnf(ctx,
			"yunzhijia: 封禁失败后恢复 disabled_by_sync 失败: user=%s err=%v",
			userID, err)
	}
}

// markYunzhijiaBlockedBeforePurge 在墓碑化之前标记员工禁止重建。
// 非员工直接跳过；员工标记失败直接返回，调用方不得继续 purge。
func markYunzhijiaBlockedBeforePurge(
	ctx context.Context,
	state interfaces.YunzhijiaEmployeeState,
	user *types.User,
	actorID string,
) error {
	if state == nil || !isYunzhijiaEmployeeUser(user) {
		return nil
	}
	if err := state.MarkBlockedAfterPurge(ctx, user.ID, actorID); err != nil {
		logger.Warnf(ctx,
			"yunzhijia: 删除前标记 blocked 失败，已阻止 purge: user=%s err=%v",
			user.ID, err)
		return fmt.Errorf("yunzhijia: 删除前标记员工 blocked 失败: %w", err)
	}
	return nil
}
