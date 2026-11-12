package repository

import (
	"context"
	"errors"
	"sync"
)

// UserProvisionLocker 串行化同一账号的 owner 空间开通。
//
// 目的：ProvisionOwnerSpace 的“查已有 owner 空间 → 没有则新建”必须在
// 同一把锁内完成，否则并发请求会各建一个空间。
//
// PostgreSQL 用事务级 advisory lock（pg_advisory_xact_lock），MySQL 用
// GET_LOCK/RELEASE_LOCK；两者都是服务端全局锁，多副本部署同样有效。
// 不能用 users 行 FOR UPDATE：开通流程随后会在另一条连接上更新 users
// （写 home tenant），行锁会把自己锁死。
//
// SQLite（Lite/单进程）不支持上述锁，退化为进程内按 userID 的互斥锁；
// 生产 PG/MySQL 不受影响。
type UserProvisionLocker interface {
	LockUserProvision(ctx context.Context, userID string) (release func(), err error)
}

// sqliteProvisionLocks 只在非 PG/MySQL 数据库上使用；键为 userID。
// 条目数量与“被开通为 owner 的账号数”同阶，保留不删除以避免释放/获取竞态。
var sqliteProvisionLocks sync.Map

func provisionLockName(userID string) string {
	return "owner-provision:" + userID
}

func (r *userRepository) LockUserProvision(
	ctx context.Context,
	userID string,
) (func(), error) {
	switch r.db.Dialector.Name() {
	case "postgres":
		tx := r.db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, tx.Error
		}
		// 事务级 advisory lock：提交/回滚时自动释放，不占 users 行。
		if err := tx.Exec(
			"SELECT pg_advisory_xact_lock(hashtext(?))", provisionLockName(userID),
		).Error; err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		return func() { _ = tx.Rollback() }, nil
	case "mysql":
		tx := r.db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, tx.Error
		}
		var got int
		if err := tx.Raw(
			"SELECT GET_LOCK(?, 30)", provisionLockName(userID),
		).Scan(&got).Error; err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if got != 1 {
			_ = tx.Rollback()
			return nil, errors.New("timed out acquiring owner provisioning lock")
		}
		return func() {
			// GET_LOCK 是连接级锁，必须显式释放后再回滚。
			_ = tx.Exec("SELECT RELEASE_LOCK(?)", provisionLockName(userID)).Error
			_ = tx.Rollback()
		}, nil
	default:
		value, _ := sqliteProvisionLocks.LoadOrStore(userID, &sync.Mutex{})
		mu := value.(*sync.Mutex)
		mu.Lock()
		return mu.Unlock, nil
	}
}

// 编译期断言：具体仓储满足开通锁窄接口。
var _ UserProvisionLocker = (*userRepository)(nil)
