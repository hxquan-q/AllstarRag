package service

import (
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
)

// ErrYunzhijiaEmployeePasswordDisabled 表示目标是云之家员工账号，禁止
// 使用密码。改密返回 400、重置密码返回 403；不写用户、不吊销 token。
var ErrYunzhijiaEmployeePasswordDisabled = errors.New(
	"yunzhijia employee accounts cannot use passwords")

// isYunzhijiaEmployeeUser 判断已加载的 users 行是否是云之家员工账号。
//
// 员工标记由开号流程写入 preferences.yunzhijia_employee，是 users 行上的
// 本地字段。密码守卫与管理端钩子只看这一标记，不再查询 yunzhijia_employees：
// 迁移缺失或员工表不可用时，普通账号的密码登录与管理操作不受影响；
// 员工账号的标记随 users 行一起读出，判定不会失败开放。
func isYunzhijiaEmployeeUser(user *types.User) bool {
	return user != nil &&
		user.Preferences.YunzhijiaEmployee != nil &&
		*user.Preferences.YunzhijiaEmployee
}
