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
	if aware, ok := svc.(interface {
		WithYunzhijiaEmployeeState(interfaces.YunzhijiaEmployeeState)
	}); ok {
		aware.WithYunzhijiaEmployeeState(yzjLifecycle)
	}
	return svc
}

// initYunzhijiaSSOConfig 暴露 config.Config 里的云之家配置段。
// 配置段缺失时返回 nil，登录入口按未启用 fail-closed。
func initYunzhijiaSSOConfig(cfg *config.Config) *config.YunzhijiaSSOConfig {
	return cfg.YunzhijiaSSO
}

// initYunzhijiaClient 按配置构造外部客户端。未启用或配置非法时返回
// nil；登录服务以 s.client == nil 判断并拒绝，不会向 nil 客户端发请求。
func initYunzhijiaClient(cfg *config.YunzhijiaSSOConfig) ssoyunzhijia.Client {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	client, err := ssoyunzhijia.NewClientFromConfig(cfg)
	if err != nil {
		logger.Warnf(context.Background(), "[Yunzhijia] 客户端未初始化（登录将按不可用处理）: %v", err)
		return nil
	}
	return client
}
