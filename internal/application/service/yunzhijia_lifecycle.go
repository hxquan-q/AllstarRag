package service

// 云之家员工账号生命周期（T06）。
//
// 一轮同步 upsert 员工目录后，由本服务把上游在职/离职状态落到本地账号：
// 首次开号、再入职恢复、调岗迁移、离职停用。它同时实现管理端反向钩子
// YunzhijiaEmployeeState（封禁/删除时标记员工目录）。
//
// 约束：
//   - 员工账号角色固定 viewer，一人一空间；
//   - 不写 tenant_members.status=suspended；
//   - blocked 员工完全跳过，不写账号、成员关系或员工状态；
//   - 管理员封禁（is_active=false 且 disabled_by_sync=false）不自动解封；
//   - 钩子不受 enabled 门控。

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// YunzhijiaLifecycle 承接同步后的账号动作与管理端反向标记。
type YunzhijiaLifecycle struct {
	provisioner   *YunzhijiaProvisioner
	userRepo      interfaces.UserRepository
	memberService interfaces.TenantMemberService
	employeeRepo  interfaces.YunzhijiaEmployeeRepository
}

// NewYunzhijiaLifecycle 构造生命周期服务。cfg 为红阶段锁定的构造签名
// 保留位：目标空间已由同步规划写入 employee.TenantID，这里不再读取配置。
func NewYunzhijiaLifecycle(
	cfg *config.YunzhijiaSSOConfig,
	provisioner *YunzhijiaProvisioner,
	userRepo interfaces.UserRepository,
	memberService interfaces.TenantMemberService,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
) *YunzhijiaLifecycle {
	return &YunzhijiaLifecycle{
		provisioner:   provisioner,
		userRepo:      userRepo,
		memberService: memberService,
		employeeRepo:  employeeRepo,
	}
}

// ApplyEmployee 处理在职员工：开号、再入职恢复或调岗。
//
// 已有账号时与管理员封禁/解封/停用/删除共享按用户串行锁（复用
// LockUserProvision）。锁内重读员工行与 users 行，覆盖「读标记/IsActive →
// 写标记/状态」；成员关系的 Add/Remove 在释放锁后执行，避免与成员服务
// 的同一把用户锁重入死锁。
func (l *YunzhijiaLifecycle) ApplyEmployee(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (types.YunzhijiaAccountResult, error) {
	if employee.Blocked {
		return types.YunzhijiaAccountResultNone, nil
	}
	if employee.UserID == nil {
		if _, err := l.provisioner.EnsureAccount(ctx, employee); err != nil {
			return types.YunzhijiaAccountResultNone, err
		}
		return types.YunzhijiaAccountResultCreated, nil
	}

	userID := *employee.UserID
	user, current, runMemberOps, err := l.lockActiveUser(ctx, employee.OpenID, userID)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if !runMemberOps {
		return types.YunzhijiaAccountResultNone, nil
	}
	migrated, err := l.applyActiveUser(ctx, user, current)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if migrated {
		return types.YunzhijiaAccountResultMigrated, nil
	}
	return types.YunzhijiaAccountResultUpdated, nil
}

// lockActiveUser 在用户锁内重读 users/员工行，完成「读标记/IsActive →
// 写标记/状态」。返回的 user/employee 供锁外的成员 Add/Remove 使用；
// runMemberOps=false 表示本地已 blocked，无需再做成员动作。
// 锁由 defer 释放，成员写入不会落在锁内。
func (l *YunzhijiaLifecycle) lockActiveUser(
	ctx context.Context, openID, userID string,
) (*types.User, *types.YunzhijiaEmployee, bool, error) {
	release, err := acquireUserProvisionLock(ctx, l.userRepo, userID)
	if err != nil {
		return nil, nil, false, err
	}
	defer release()

	user, err := l.loadEmployeeUser(ctx, userID)
	if err != nil {
		return nil, nil, false, err
	}
	employee, err := l.reloadEmployee(ctx, openID)
	if err != nil {
		return nil, nil, false, err
	}
	// 已绑定账号必须与开号/登录走同一道闸门（AC-7 / §10.3）：标记缺失或
	// 已设密码的账号不得被同步补成员、恢复或改状态。失败零写入，由同步
	// 记 provision_failed，部署巡检回填标记后可恢复。
	if err := checkReusableEmployeeAccount(user); err != nil {
		return nil, nil, false, err
	}
	if employee.Blocked {
		return nil, nil, false, nil
	}
	// 先看同步停用标记：再入职、管理员先手动解封等场景会留下「在职但
	// 标记仍为 true」，登录侧会因此永久拒绝。标记为 true 一律走恢复。
	if employee.DisabledBySync {
		if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", true); err != nil {
			return nil, nil, false, err
		}
		if err := l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, false); err != nil {
			return nil, nil, false, err
		}
		return user, employee, true, nil
	}
	if !user.IsActive {
		return nil, nil, false, ErrYunzhijiaAccountBanned
	}
	return user, employee, true, nil
}

// ApplyInactive 处理离职员工：先写同步停用标记，再停用账号、移除成员关系。
//
// 与 ApplyEmployee 相同：用户锁覆盖标记/状态写入，成员关系移除在释放锁后
// 执行。
func (l *YunzhijiaLifecycle) ApplyInactive(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (types.YunzhijiaAccountResult, error) {
	if employee.Blocked || employee.UserID == nil {
		return types.YunzhijiaAccountResultNone, nil
	}

	userID := *employee.UserID
	user, runMemberOps, err := l.lockInactiveUser(ctx, employee.OpenID, userID)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if !runMemberOps {
		return types.YunzhijiaAccountResultNone, nil
	}
	if err := l.removeAllMemberships(ctx, user.ID); err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	return types.YunzhijiaAccountResultDisabled, nil
}

// lockInactiveUser 在用户锁内重读 users/员工行，完成「读标记/IsActive →
// 写标记/状态」。runMemberOps=true 表示锁内已把账号置为同步停用，调用方
// 在锁外移除成员关系。
func (l *YunzhijiaLifecycle) lockInactiveUser(
	ctx context.Context, openID, userID string,
) (*types.User, bool, error) {
	release, err := acquireUserProvisionLock(ctx, l.userRepo, userID)
	if err != nil {
		return nil, false, err
	}
	defer release()

	user, err := l.loadEmployeeUser(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	employee, err := l.reloadEmployee(ctx, openID)
	if err != nil {
		return nil, false, err
	}
	// 离职是收权方向，闸门只拦授权路径：不得因标记缺失或有密码而
	// fail-closed，把离职员工留在 active；停用/移除成员/吊销会话照常执行。
	if employee.Blocked {
		return nil, false, nil
	}
	// 管理员封禁或已停用：同步不复用、不改写任何状态。
	if !user.IsActive {
		return nil, false, nil
	}
	// 先写同步停用标记：若后续停用/移除成员关系失败，账号仍是 active，
	// 下一轮 ApplyInactive 会重试，语义不会退化成「管理员封禁」。反过来
	// （先停用后标记）中途失败会把账号永久当成管理员封禁，再入职不恢复。
	if err := l.employeeRepo.SetDisabledBySync(
		ctx, employee.OpenID, true,
	); err != nil {
		return nil, false, err
	}
	if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", false); err != nil {
		return nil, false, err
	}
	return user, true, nil
}

// reloadEmployee 在用户锁内重读员工行，避免使用加锁前的旧快照决定
// blocked / disabled_by_sync。行缺失视为数据不一致，记同步异常而不是
// 中断整轮。
func (l *YunzhijiaLifecycle) reloadEmployee(
	ctx context.Context, openID string,
) (*types.YunzhijiaEmployee, error) {
	employee, err := l.employeeRepo.GetByOpenID(ctx, openID)
	if err != nil {
		return nil, err
	}
	if employee == nil {
		return nil, fmt.Errorf("yunzhijia: 员工 %s 记录不存在", openID)
	}
	return employee, nil
}

// MarkAdminDisabled 清除员工的 disabled_by_sync 标记，并返回清除前的值。
// 员工目录没有该用户时直接返回，不影响封禁/停用主流程。返回值供调用方
// 在封禁事务失败时补偿。
func (l *YunzhijiaLifecycle) MarkAdminDisabled(
	ctx context.Context, userID string,
) (bool, error) {
	employee, err := l.employeeRepo.GetByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	if employee == nil {
		return false, nil
	}
	wasDisabled := employee.DisabledBySync
	if err := l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, false); err != nil {
		return false, err
	}
	return wasDisabled, nil
}

// RestoreAdminDisabled 把 disabled_by_sync 恢复为封禁前的值，仅用于封禁
// 事务失败后的补偿。wasDisabled=false 时为空操作。
func (l *YunzhijiaLifecycle) RestoreAdminDisabled(
	ctx context.Context, userID string, wasDisabled bool,
) error {
	if !wasDisabled {
		return nil
	}
	employee, err := l.employeeRepo.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if employee == nil {
		return nil
	}
	return l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, true)
}

// MarkBlockedAfterPurge 在墓碑化之前把员工置为禁止重建。没有员工记录时
// 直接返回，普通账号删除保持旧行为。
func (l *YunzhijiaLifecycle) MarkBlockedAfterPurge(
	ctx context.Context, userID, actorID string,
) error {
	employee, err := l.employeeRepo.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if employee == nil {
		return nil
	}
	return l.employeeRepo.MarkBlocked(ctx, employee.OpenID, actorID)
}

// loadEmployeeUser 按 user_id 加载本地账号。缺失时映射为生命周期哨兵，
// 让同步把该员工记成异常而不是中断整轮。
func (l *YunzhijiaLifecycle) loadEmployeeUser(
	ctx context.Context, userID string,
) (*types.User, error) {
	user, err := l.userRepo.GetUserByID(ctx, userID)
	if err != nil {
		if isUserLookupNotFound(err) {
			return nil, ErrYunzhijiaLocalUserMissing
		}
		return nil, err
	}
	return user, nil
}

// applyActiveUser 处理在职账号：同空间确保 viewer；跨空间先移旧再加新。
// 真实 RemoveMember 会清 tenant_id / LastActiveTenantID 并吊销 token，
// AddMember 再设新家空间，因此这里不再整行写回旧快照。
// 返回 true 表示发生了跨空间迁移（移除了其他空间的成员关系）。
func (l *YunzhijiaLifecycle) applyActiveUser(
	ctx context.Context, user *types.User, employee *types.YunzhijiaEmployee,
) (bool, error) {
	memberships, err := l.memberService.ListByUser(ctx, user.ID)
	if err != nil {
		return false, err
	}
	migrated := false
	for _, member := range memberships {
		if member.TenantID == employee.TenantID {
			continue
		}
		if err := l.memberService.RemoveMember(ctx, user.ID, member.TenantID); err != nil {
			return false, err
		}
		migrated = true
	}
	// 复用 provisioner 的入组实现：它把并发插入冲突（成员关系已存在）
	// 视为成功，避免登录与同步并发补成员时误记 provision_failed。
	if err := l.provisioner.ensureViewerMembership(
		ctx, user.ID, employee.TenantID,
	); err != nil {
		return false, err
	}
	return migrated, nil
}

// removeAllMemberships 逐一移除用户当前所有 active 成员关系。
func (l *YunzhijiaLifecycle) removeAllMemberships(
	ctx context.Context, userID string,
) error {
	memberships, err := l.memberService.ListByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, member := range memberships {
		if err := l.memberService.RemoveMember(ctx, userID, member.TenantID); err != nil {
			return err
		}
	}
	return nil
}
