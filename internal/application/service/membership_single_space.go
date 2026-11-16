package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T04 一号一空间守卫（AC-15/16/17/18/19/20）。
//
// 设计要点：
//   - 非超管账号同一时刻最多一条 status IN ('active','invited') 的成员关系；
//     suspended/deleted 不计入归属（AC-18）。
//   - 所有成员写入路径复用 repository.UserProvisionLocker（LockUserProvision，
//     与 ProvisionOwnerSpace 同一把锁），按 user_id 串行化。
//   - 超级管理员（is_system_admin / can_access_all_tenants）豁免（AC-17）。
//   - 不新增 DB 唯一索引：超管豁免无法用部分索引谓词表达（AC-20）。

// ErrUserAlreadyInSpace 表示非超管账号已属于其他空间（active/invited），
// 不能再加入/开通第二个空间。handler 映射为 409。
var ErrUserAlreadyInSpace = errors.New("user already belongs to another workspace")

// singleSpaceMemberWriter 是服务内各写入路径共享的「按用户锁 + 锁内写入」
// 窄接口。方法未导出，只在同包内使用；测试替身不实现时调用方回退到公开
// 方法（公开方法自己加锁）。
type singleSpaceMemberWriter interface {
	lockUser(ctx context.Context, userID string) (func(), error)
	assertSingleSpaceMembership(ctx context.Context, userID string, tenantID uint64) error
	addMemberLocked(
		ctx context.Context,
		userID string,
		tenantID uint64,
		role types.TenantRole,
		invitedBy *string,
	) (*types.TenantMember, error)
	ensureOwnerLocked(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
}

// singleSpaceMemberChecker 只需要归属校验（邀请创建路径不写成员行）。
type singleSpaceMemberChecker interface {
	assertSingleSpaceMembership(ctx context.Context, userID string, tenantID uint64) error
}

// usersMembershipLocker 是会员服务对用户级锁的最小依赖；具体
// *userRepository 实现它，测试替身没有时退化为进程内互斥。
type usersMembershipLocker interface {
	LockUserProvision(ctx context.Context, userID string) (func(), error)
}

// acquireUserProvisionLock 复用 repository.UserProvisionLocker（与开通
// owner 空间同一个锁名）。userRepo 未注入或测试替身不支持时退化为进程内
// 按 userID 的互斥锁；生产 PG/MySQL 走服务端全局锁。
func acquireUserProvisionLock(
	ctx context.Context,
	userRepo interfaces.UserRepository,
	userID string,
) (func(), error) {
	if locker, ok := userRepo.(usersMembershipLocker); ok {
		return locker.LockUserProvision(ctx, userID)
	}
	value, _ := provisionFallbackLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
}

// lockUser 是 tenantMemberService 对用户级锁的入口。
func (s *tenantMemberService) lockUser(ctx context.Context, userID string) (func(), error) {
	return acquireUserProvisionLock(ctx, s.userRepo, userID)
}

// assertSingleSpaceMembership 校验 userID 是否已属于 tenantID 之外的
// 空间。userRepo 未注入（部分单测/局部 DI）或用户不存在时跳过归属检查，
// 保持既有 fake 单测可用；超管豁免。
func (s *tenantMemberService) assertSingleSpaceMembership(
	ctx context.Context,
	userID string,
	tenantID uint64,
) error {
	if s.userRepo == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	// 用户记录只用于超管豁免判断；读不到时按普通账号继续做成员归属检查
	// （fail-closed，不会因此放行第二个空间）。既有单测的轻量 userRepo
	// 替身对不存在用户返回普通 error，这里不能让它中断成员写入。
	user, userErr := s.userRepo.GetUserByID(ctx, userID)
	if userErr != nil {
		if !errors.Is(userErr, apprepo.ErrUserNotFound) {
			logger.Warnf(ctx,
				"single-space guard: failed to load user %s: %v", userID, userErr)
		}
		user = nil
	}
	if user != nil && (user.IsSystemAdmin || user.CanAccessAllTenants) {
		return nil
	}
	members, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member == nil || member.DeletedAt.Valid || member.TenantID == tenantID {
			continue
		}
		if member.Status == types.TenantMemberStatusActive ||
			member.Status == types.TenantMemberStatusInvited {
			// 顺带修正没有 home 的账号：AC-19 要求 users.tenant_id 指向
			// 用户仍是 active 成员的空间。invited 还不是 active，不写。
			if member.Status == types.TenantMemberStatusActive && user != nil && user.TenantID == 0 {
				s.adoptHomeTenantIfEmpty(ctx, userID, member.TenantID)
			}
			return ErrUserAlreadyInSpace
		}
	}
	return nil
}

// emptyHomeTenantWriter 是 adoptHomeTenantIfEmpty 依赖的窄接口：只条件
// 更新 users.tenant_id / updated_at。真实 user 仓储实现它；测试替身没有
// 时跳过回填（不再整行 Save，避免覆盖并发写入）。
type emptyHomeTenantWriter interface {
	SetTenantIDIfEmpty(ctx context.Context, userID string, tenantID uint64) (bool, error)
}

// adoptHomeTenantIfEmpty 在成员创建成功且用户当前没有家空间（0/NULL）时，
// 把 users.tenant_id 指向刚加入的空间（AC-19）。已有家空间的账号保持原值，
// 避免把超管的 home 改到第二个空间。nil-safe 且尽力而为：写失败只记日志，
// 不推翻已建立的成员关系（与 cleanupRemovedMemberState 同口径）。
//
// 只做列级条件更新，不整行 Save：整行 Save 会把并发密码重置/撤权写回旧值
// （N23 / N26）。条件更新本身保证「先到者生效」，无需再预读用户。
func (s *tenantMemberService) adoptHomeTenantIfEmpty(
	ctx context.Context,
	userID string,
	tenantID uint64,
) {
	if s.userRepo == nil || tenantID == 0 || strings.TrimSpace(userID) == "" {
		return
	}
	writer, ok := s.userRepo.(emptyHomeTenantWriter)
	if !ok {
		logger.Warnf(ctx,
			"membership home-tenant adopt: repository lacks conditional update; skip user=%s tenant=%d",
			userID, tenantID)
		return
	}
	if _, err := writer.SetTenantIDIfEmpty(ctx, userID, tenantID); err != nil {
		logger.Warnf(ctx,
			"membership home-tenant adopt: failed to set user %s tenant %d: %v",
			userID, tenantID, err)
	}
}
