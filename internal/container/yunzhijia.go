package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	ssoyunzhijia "github.com/Tencent/WeKnora/internal/sso/yunzhijia"
)

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
