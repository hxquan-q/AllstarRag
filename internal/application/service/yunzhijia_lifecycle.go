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
) error {
	if employee.Blocked {
		return nil
	}
	if employee.UserID == nil {
		_, err := l.provisioner.EnsureAccount(ctx, employee)
		return err
	}

	user, err := l.loadEmployeeUser(ctx, *employee.UserID)
	if err != nil {
		return err
	}
	if !user.IsActive {
		if !employee.DisabledBySync {
			return ErrYunzhijiaAccountBanned
		}
		return l.restoreRehiredUser(ctx, user, employee)
	}
	return l.applyActiveUser(ctx, user, employee)
}

// ApplyInactive 处理离职员工：停用账号、移除成员关系并写同步停用标记。
func (l *YunzhijiaLifecycle) ApplyInactive(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) error {
	if employee.Blocked || employee.UserID == nil {
		return nil
	}

	user, err := l.loadEmployeeUser(ctx, *employee.UserID)
	if err != nil {
		return err
	}
	// 管理员封禁或已停用：同步不复用、不改写任何状态。
	if !user.IsActive {
		return nil
	}
	if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", false); err != nil {
		return err
	}
	if err := l.removeAllMemberships(ctx, user.ID); err != nil {
		return err
	}
	return l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, true)
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
	if user == nil {
		return nil, ErrYunzhijiaLocalUserMissing
	}
	return user, nil
}

// restoreRehiredUser 恢复同步停用的账号：启用、回目标空间、清标记。
func (l *YunzhijiaLifecycle) restoreRehiredUser(
	ctx context.Context, user *types.User, employee *types.YunzhijiaEmployee,
) error {
	if _, err := l.userRepo.SetUserActive(ctx, user.ID, "", true); err != nil {
		return err
	}
	if err := l.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		return err
	}
	return l.employeeRepo.SetDisabledBySync(ctx, employee.OpenID, false)
}

// applyActiveUser 处理在职账号：同空间确保 viewer；跨空间先移旧再加新，
// 清除最后活跃空间（真实 RemoveMember 同时吊销 token）。
func (l *YunzhijiaLifecycle) applyActiveUser(
	ctx context.Context, user *types.User, employee *types.YunzhijiaEmployee,
) error {
	memberships, err := l.memberService.ListByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	moved := false
	for _, member := range memberships {
		if member.TenantID == employee.TenantID {
			continue
		}
		if err := l.memberService.RemoveMember(ctx, user.ID, member.TenantID); err != nil {
			return err
		}
		moved = true
	}
	if err := l.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		return err
	}
	if !moved {
		return nil
	}
	user.Preferences.LastActiveTenantID = nil
	return l.userRepo.UpdateUser(ctx, user)
}

// ensureViewerMembership 仅在目标空间缺少成员关系时加入 viewer。
func (l *YunzhijiaLifecycle) ensureViewerMembership(
	ctx context.Context, userID string, tenantID uint64,
) error {
	current, err := l.memberService.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if current != nil {
		return nil
	}
	_, err = l.memberService.AddMember(ctx, userID, tenantID, types.TenantRoleViewer, nil)
	return err
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
