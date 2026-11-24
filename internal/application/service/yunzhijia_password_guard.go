package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrYunzhijiaEmployeePasswordDisabled 表示目标是云之家员工账号，禁止
// 使用密码。改密返回 400、重置密码返回 403；不写用户、不吊销 token。
var ErrYunzhijiaEmployeePasswordDisabled = errors.New(
	"yunzhijia employee accounts cannot use passwords")

// yunzhijiaEmployeeLookup 是员工仓储的薄适配：按本地 user_id 查员工目录。
// 有记录即视为员工账号，不论其上游状态或本地启停状态。
type yunzhijiaEmployeeLookup struct {
	repo interfaces.YunzhijiaEmployeeRepository
}

// NewYunzhijiaEmployeeLookup 把员工仓储适配为 YunzhijiaEmployeeLookup，
// 仓储由容器统一注入。
func NewYunzhijiaEmployeeLookup(
	repo interfaces.YunzhijiaEmployeeRepository,
) interfaces.YunzhijiaEmployeeLookup {
	return &yunzhijiaEmployeeLookup{repo: repo}
}

// IsYunzhijiaEmployee 查询失败原样返回 error，由调用方按员工处理。
func (l *yunzhijiaEmployeeLookup) IsYunzhijiaEmployee(
	ctx context.Context, userID string,
) (bool, error) {
	employee, err := l.repo.GetByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	return employee != nil, nil
}

// isYunzhijiaEmployee 判断目标是否必须禁用密码。lookup 未注入或查询失败时
// 分别返回 false / true：未注入保持旧行为，查询失败 fail-closed。
func (s *userService) isYunzhijiaEmployee(ctx context.Context, userID string) bool {
	if s.yunzhijiaEmployeeLookup == nil {
		return false
	}
	employee, err := s.yunzhijiaEmployeeLookup.IsYunzhijiaEmployee(ctx, userID)
	if err != nil {
		logger.Errorf(ctx, "Yunzhijia employee lookup failed, denying password path: user=%s err=%v",
			userID, err)
		return true
	}
	return employee
}
