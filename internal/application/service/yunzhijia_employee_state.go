package service

// 管理端账号动作与云之家员工目录之间的反向钩子（T06）。
//
// 三参构造保持不变，依赖由容器/测试用可选 setter 注入；nil 时调用方
// 保持旧行为。钩子不受 YunzhijiaSSO.Enabled 门控：即使同步关闭，人工
// 封禁/停用/删除也必须让员工目录进入正确状态，否则恢复同步后会被
// 自动重建或解封。

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
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

// notifyYunzhijiaAdminDisabled 在封禁/人工停用成功后清 disabled_by_sync。
//
// 标记失败不回滚已生效的封禁：返回错误让调用方看到，重试同一封禁仍
// 幂等，下一次成功标记即可收敛。
func (s *userManagementService) notifyYunzhijiaAdminDisabled(
	ctx context.Context, userID string,
) error {
	if s.yunzhijiaState == nil {
		return nil
	}
	if err := s.yunzhijiaState.MarkAdminDisabled(ctx, userID); err != nil {
		logger.Warnf(ctx,
			"yunzhijia: 封禁后标记 disabled_by_sync 失败（封禁已生效）: user=%s err=%v",
			userID, err)
		return fmt.Errorf("yunzhijia: 标记员工管理员停用失败: %w", err)
	}
	return nil
}

// markYunzhijiaBlockedBeforePurge 在墓碑化之前标记员工禁止重建。
// 标记失败直接返回，调用方不得继续 purge。
func (s *userManagementService) markYunzhijiaBlockedBeforePurge(
	ctx context.Context, userID, actorID string,
) error {
	if s.yunzhijiaState == nil {
		return nil
	}
	if err := s.yunzhijiaState.MarkBlockedAfterPurge(ctx, userID, actorID); err != nil {
		logger.Warnf(ctx,
			"yunzhijia: 删除前标记 blocked 失败，已阻止 purge: user=%s err=%v",
			userID, err)
		return fmt.Errorf("yunzhijia: 删除前标记员工 blocked 失败: %w", err)
	}
	return nil
}
