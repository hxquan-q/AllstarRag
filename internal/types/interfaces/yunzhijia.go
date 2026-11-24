package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// YunzhijiaEmployeeRepository 持久化云之家员工目录（open_id 与本地账号的映射）。
//
// 查询“不存在”返回 (nil, nil)，由服务层决定是“账号开通中”还是其它口径；
// 写操作找不到目标行时返回 repository.ErrYunzhijiaEmployeeNotFound。
type YunzhijiaEmployeeRepository interface {
	// Create 插入一行员工记录。open_id 冲突返回
	// repository.ErrYunzhijiaEmployeeExists。
	Create(ctx context.Context, employee *types.YunzhijiaEmployee) error

	// UpsertByOpenID 按 open_id 插入或更新同步字段。
	// 不覆盖本地状态：user_id、disabled_by_sync、blocked、blocked_at、
	// blocked_by 由本地流程维护。
	UpsertByOpenID(ctx context.Context, employee *types.YunzhijiaEmployee) error

	// GetByOpenID 按云之家 open_id 查询；不存在返回 (nil, nil)。
	GetByOpenID(ctx context.Context, openID string) (*types.YunzhijiaEmployee, error)

	// GetByUserID 按本地 users.id 查询；不存在返回 (nil, nil)。
	GetByUserID(ctx context.Context, userID string) (*types.YunzhijiaEmployee, error)

	// SetUserID 在本地账号创建成功后回填 employee.user_id。
	// 目标行不存在时返回 repository.ErrYunzhijiaEmployeeNotFound。
	SetUserID(ctx context.Context, openID, userID string) error

	// FindActiveByName 返回所有 status=active 的同名记录（含 blocked），
	// 供登录时区分 0 条 / 1 条 / 多条 / blocked。按 id 升序。
	FindActiveByName(ctx context.Context, name string) ([]*types.YunzhijiaEmployee, error)

	// ListByStatus 返回指定状态的员工记录，按 id 升序。
	ListByStatus(ctx context.Context, status types.YunzhijiaEmployeeStatus) ([]*types.YunzhijiaEmployee, error)

	// ListBlocked 返回 blocked=true 的记录，按 blocked_at 倒序。
	ListBlocked(ctx context.Context) ([]*types.YunzhijiaEmployee, error)

	// MarkBlocked 把指定 open_id 置为禁止重建：blocked=true、
	// 写入 blocked_at/blocked_by，并清空 user_id（账号已不存在）。
	MarkBlocked(ctx context.Context, openID, blockedBy string) error

	// Unblock 解除禁止重建：blocked=false、清空 blocked_at/blocked_by/user_id，
	// 下一轮同步可重建账号。重复调用（已解除）返回
	// repository.ErrYunzhijiaEmployeeNotFound，管理端按 404 处理。
	Unblock(ctx context.Context, openID string) error

	// SetDisabledBySync 设置 disabled_by_sync 标记（不修改 status）。
	SetDisabledBySync(ctx context.Context, openID string, disabled bool) error

	// UpdateSyncedFields 更新来自上游的同步字段（name、department、mobile、
	// tenant_id、status、last_synced_at）；不碰 user_id/disabled_by_sync/blocked。
	UpdateSyncedFields(ctx context.Context, employee *types.YunzhijiaEmployee) error
}

// YunzhijiaEmployeeLookup 判断本地用户是否由云之家开通。
//
// 员工账号只能走云之家免登录，密码登录、自行改密、管理员重置都要拒绝。
// 查询失败时调用方必须按员工处理（fail-closed），不能放行密码路径。
type YunzhijiaEmployeeLookup interface {
	IsYunzhijiaEmployee(ctx context.Context, userID string) (bool, error)
}

// YunzhijiaAccountLifecycle 承接一轮同步后的账号动作。
//
// T05 只定义窄接口并用测试替身驱动同步编排；真实的开号/停用/恢复/
// 调岗实现由 T06 提供。实现方拿到的 employee 必须是 upsert 之后从
// 仓储重新读出的行，其中 user_id / disabled_by_sync / blocked 等本地
// 字段不能被上游入参覆盖。
type YunzhijiaAccountLifecycle interface {
	// ApplyEmployee 处理上游在职员工：首次开号、再入职恢复或调岗。
	// 账号被封禁时返回哨兵 ErrYunzhijiaAccountBanned；
	// 关联本地用户缺失时返回 ErrYunzhijiaLocalUserMissing。
	ApplyEmployee(ctx context.Context, employee *types.YunzhijiaEmployee) error
	// ApplyInactive 处理上游离职员工：停用账号并作废会话。
	ApplyInactive(ctx context.Context, employee *types.YunzhijiaEmployee) error
}

// YunzhijiaLoginService 处理云之家 ticket 免登录。
//
// 业务拒绝（无效 ticket、同名、离职/封禁、配置不可用等）通过返回
// Success=false 的 LoginResponse 表达，error 为 nil；只有未预期的
// 内部错误才返回 error。
type YunzhijiaLoginService interface {
	Login(ctx context.Context, ticket string) (*types.LoginResponse, error)
}

// YunzhijiaSyncRunRepository 持久化同步运行记录，供管理端状态页读取。
// 首版只增不删，Latest/ListRecent 按 started_at 倒序取最近记录。
type YunzhijiaSyncRunRepository interface {
	// CreateRunning 写入一条 running 记录并把自增 id 回填到 run。
	CreateRunning(ctx context.Context, run *types.YunzhijiaSyncRun) error

	// Finish 按 run.ID 更新结束状态、计数、异常与错误摘要。
	// 目标记录不存在时返回 repository.ErrYunzhijiaSyncRunNotFound。
	Finish(ctx context.Context, run *types.YunzhijiaSyncRun) error

	// Latest 返回最近一条运行记录；没有记录时返回 (nil, nil)。
	Latest(ctx context.Context) (*types.YunzhijiaSyncRun, error)

	// ListRecent 返回最近 limit 条记录（limit<=0 时按 20 条）；
	// 按 started_at 倒序、同时间按 id 倒序。
	ListRecent(ctx context.Context, limit int) ([]*types.YunzhijiaSyncRun, error)
}
