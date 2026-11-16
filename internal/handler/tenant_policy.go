package handler

import (
	"context"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	tenantSelfServiceCreationSettingKey = "tenant.self_service_creation_enabled"
	tenantSelfServiceCreationEnvName    = "WEKNORA_TENANT_SELF_SERVICE_CREATION_ENABLED"
)

// resolveTenantSelfServiceCreationEnabled 读取上游的
// tenant.self_service_creation_enabled（或环境变量）部署开关。
//
// 注意（T11 / AC-44，产品决策）：本产品已把“创建新空间”收敛为仅平台级管理员
// （系统管理员 / 跨空间超管 / 平台 API Key）。上游这个开关原本允许任意已登录
// 用户自助建空间，该语义在本产品里已被覆盖：开关关闭时普通用户仍返回 403
// （沿用旧错误码 2005），开关开启时由 tenant.go 的产品门禁返回 403。
// 因此本函数不得再作为普通用户建空间的放行依据；保留函数与上游配置默认值，
// 只为减少上游同步冲突，后续不要把普通用户建空间路径恢复回来。
func resolveTenantSelfServiceCreationEnabled(
	ctx context.Context,
	cfg *config.Config,
	settings interfaces.SystemSettingService,
) bool {
	enabled := true
	if cfg != nil && cfg.Tenant != nil {
		enabled = cfg.Tenant.IsSelfServiceCreationEnabled()
	}
	if settings == nil {
		return enabled
	}
	return settings.GetBool(
		ctx,
		tenantSelfServiceCreationSettingKey,
		tenantSelfServiceCreationEnvName,
		enabled,
	)
}
