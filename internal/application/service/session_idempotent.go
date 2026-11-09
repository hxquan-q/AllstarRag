package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// sessionIdempotentCreator 是 automation 运行与委托运行建会话所需的窄接口。
// 实现方先锁 live web 用户行（墓碑账号拒绝、零插入），再按调用方预分配的
// ID 幂等插入（重复 ID 不报错、保留首条）。
//
// 按接口隔离只暴露这一项能力：这两条路径不依赖 SessionService 的其余职责。
type sessionIdempotentCreator interface {
	CreateIdempotent(ctx context.Context, session *types.Session) (*types.Session, error)
}

// newSessionIdempotentCreator 返回窄接口的仓储实现。它与服务共用同一个 db，
// 因此 live-user 用户锁与插入事务和 SessionRepository.CreateIdempotent
// 完全一致。
func newSessionIdempotentCreator(db *gorm.DB) sessionIdempotentCreator {
	return repository.NewSessionRepository(db)
}
