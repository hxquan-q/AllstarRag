package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	ssoyunzhijia "github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// provideUserService 构造 UserService，并把云之家员工判定注入密码路径。
// lookup 是可选依赖的显式接线，interfaces.UserService 公共契约不变。
func provideUserService(
	configInfo *config.Config,
	userRepo interfaces.UserRepository,
	tokenRepo interfaces.AuthTokenRepository,
	tenantService interfaces.TenantService,
	memberService interfaces.TenantMemberService,
	systemSettingSvc interfaces.SystemSettingService,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
	yzjLifecycle *service.YunzhijiaLifecycle,
) interfaces.UserService {
	svc := service.NewUserService(
		configInfo, userRepo, tokenRepo, tenantService, memberService, systemSettingSvc,
		service.NewYunzhijiaEmployeeLookup(employeeRepo),
	)
	// 接线不对时启动直接 panic，避免云之家登录钩子静默失效。
	svc.(interface {
		WithYunzhijiaEmployeeState(interfaces.YunzhijiaEmployeeState)
	}).WithYunzhijiaEmployeeState(yzjLifecycle)
	return svc
}

// provideYunzhijiaAccountLifecycle 把具体生命周期服务暴露为同步服务需要的
// 窄接口。具体类型 *service.YunzhijiaLifecycle 仍单独提供：用户管理侧的
// 封禁/停用/删除钩子注入的是具体类型，不能用 dig.As 顶掉。
func provideYunzhijiaAccountLifecycle(
	lifecycle *service.YunzhijiaLifecycle,
) interfaces.YunzhijiaAccountLifecycle {
	return lifecycle
}

// initYunzhijiaSSOConfig 暴露 config.Config 里的云之家配置段。
// 配置段缺失时返回 nil，登录入口按未启用 fail-closed。
func initYunzhijiaSSOConfig(cfg *config.Config) *config.YunzhijiaSSOConfig {
	return cfg.YunzhijiaSSO
}

// initYunzhijiaScheduler 构造同步调度器；配置段缺失或 enabled=false 时
// Start 不注册任务，手动同步/预检由管理端直接调用同步服务。
func initYunzhijiaScheduler(
	cfg *config.YunzhijiaSSOConfig,
	syncSvc *service.YunzhijiaSyncService,
	runs interfaces.YunzhijiaSyncRunRepository,
) *service.YunzhijiaScheduler {
	return service.NewYunzhijiaScheduler(cfg, syncSvc, runs)
}

// initYunzhijiaClient 按配置构造外部客户端。配置段缺失或配置非法时返回
// nil；登录服务另外检查 enabled，预检/手动同步在 enabled=false 下仍可构造
// 客户端运行。
func initYunzhijiaClient(cfg *config.YunzhijiaSSOConfig) ssoyunzhijia.Client {
	if cfg == nil {
		return nil
	}
	client, err := ssoyunzhijia.NewClientFromConfig(cfg)
	if err != nil {
		logger.Warnf(context.Background(), "[Yunzhijia] 客户端未初始化（登录将按不可用处理）: %v", err)
		return nil
	}
	return client
}
