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

	user, err := l.loadEmployeeUser(ctx, *employee.UserID)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	// 先看同步停用标记：再入职、管理员先手动解封等场景会留下「在职但
	// 标记仍为 true」，登录侧会因此永久拒绝。标记为 true 一律走恢复。
	if employee.DisabledBySync {
		if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", true); err != nil {
			return types.YunzhijiaAccountResultNone, err
		}
		// 复用调岗逻辑清理其他空间的残留成员关系，并补上目标空间。
		if _, err := l.applyActiveUser(ctx, user, employee); err != nil {
			return types.YunzhijiaAccountResultNone, err
		}
		if err := l.employeeRepo.SetDisabledBySync(
			ctx, employee.OpenID, false,
		); err != nil {
			return types.YunzhijiaAccountResultNone, err
		}
		return types.YunzhijiaAccountResultUpdated, nil
	}
	if !user.IsActive {
		return types.YunzhijiaAccountResultNone, ErrYunzhijiaAccountBanned
	}
	migrated, err := l.applyActiveUser(ctx, user, employee)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if migrated {
		return types.YunzhijiaAccountResultMigrated, nil
	}
	return types.YunzhijiaAccountResultUpdated, nil
}

// ApplyInactive 处理离职员工：先写同步停用标记，再停用账号、移除成员关系。
func (l *YunzhijiaLifecycle) ApplyInactive(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (types.YunzhijiaAccountResult, error) {
	if employee.Blocked || employee.UserID == nil {
		return types.YunzhijiaAccountResultNone, nil
	}

	user, err := l.loadEmployeeUser(ctx, *employee.UserID)
	if err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	// 管理员封禁或已停用：同步不复用、不改写任何状态。
	if !user.IsActive {
		return types.YunzhijiaAccountResultNone, nil
	}
	// 先写同步停用标记：若后续停用/移除成员关系失败，账号仍是 active，
	// 下一轮 ApplyInactive 会重试，语义不会退化成「管理员封禁」。反过来
	// （先停用后标记）中途失败会把账号永久当成管理员封禁，再入职不恢复。
	if err := l.employeeRepo.SetDisabledBySync(
		ctx, employee.OpenID, true,
	); err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", false); err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	if err := l.removeAllMemberships(ctx, user.ID); err != nil {
		return types.YunzhijiaAccountResultNone, err
	}
	return types.YunzhijiaAccountResultDisabled, nil
}

// MarkAdminDisabled 清除员工的 disabled_by_sync 标记。员工目录没有该
// 用户时直接返回，不影响封禁/停用主流程。
func (l *YunzhijiaLifecycle) MarkAdminDisabled(ctx context.Context, userID string) error {
	employee, err := l.employeeRepo.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if employee == nil {
		return nil
	}
	return l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, false)
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
