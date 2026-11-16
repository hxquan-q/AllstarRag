package service

import (
	"context"
	"encoding/json"
	"errors"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// userManagementService 实现 interfaces.UserManagementService。
//
// 封禁语义是账号级的：users.is_active=false 会同时影响该账号所在的
// 所有空间，但 tenant_members 行保持不动。解封只恢复 is_active，
// 不恢复任何历史 token——被封禁的人必须重新登录。
type userManagementService struct {
	userRepo   interfaces.UserRepository
	memberRepo interfaces.TenantMemberRepository
	// audit 用于写封禁/解封审计。
	audit interfaces.AuditLogService
	// latestBan 查"全平台最近一次成功 ban"，解封时用来判断这次 ban 是否
	// 发生在本租户。为 nil 时按失败关闭处理（普通 owner 一律 403）。
	latestBan interfaces.LatestBanAuditLookup
	// purgeRepo 提供墓碑化事务。由真实 user repository 实现；缺失时
	// PurgeUser 直接报错，不做"删一半"的降级路径。
	purgeRepo userPurgeTx
	// unbanRepo 提供"事务内复核最新 ban + 解封"。由真实 user repository
	// 实现；缺失时回退旧的预检查路径（测试替身兼容），生产必须走事务。
	unbanRepo userUnbanTx
	// banRepo 提供"状态变更 + ban 审计同事务"。由真实 user repository
	// 实现；缺失时回退旧的 SetUserActive 路径（测试替身兼容）。
	banRepo userBanTx
	// listRepo 提供用户管理列表的分页/计数窄查询。由真实 tenant member
	// 仓储实现；缺失时 ListUsers 直接报内部错误，不返回空列表冒充成功。
	listRepo userManagementListRepo
	// scopedListRepo 提供多空间视图（scope=other/all）的窄查询。同样由
	// 真实 tenant member 仓储实现；缺失时 ListUsersScoped 报内部错误。
	scopedListRepo userManagementScopedListRepo

	// T04 owner 空间 / co-owner / 交接所需的跨域服务。为保持三参构造
	// 不变，这些依赖由 SetOwnerManagementDeps 在容器或测试里注入；
	// 缺失时对应方法返回内部错误，不做静默降级。
	tenantService     interfaces.TenantService
	memberService     interfaces.TenantMemberService
	invitationService interfaces.TenantInvitationService
	userService       interfaces.UserService
}

// NewUserManagementService 构造用户管理服务。三个依赖都是既有仓储/服务；
// 后续票若需要额外依赖（如 token 吊销器），用可选 setter 注入，不要改
// 这个构造签名。
func NewUserManagementService(
	userRepo interfaces.UserRepository,
	memberRepo interfaces.TenantMemberRepository,
	audit interfaces.AuditLogService,
) interfaces.UserManagementService {
	svc := &userManagementService{
		userRepo:   userRepo,
		memberRepo: memberRepo,
		audit:      audit,
	}
	// 生产与测试注入的都是 *auditLogService，它实现了按目标查最新 ban 的
	// 窄接口。自动探测可以让三参构造保持原样。
	if lookup, ok := audit.(interfaces.LatestBanAuditLookup); ok {
		svc.latestBan = lookup
	}
	// 墓碑化事务挂在 user repository 上（同包仓储方法），自动探测可让
	// 三参构造保持原样。
	if purge, ok := userRepo.(userPurgeTx); ok {
		svc.purgeRepo = purge
	}
	if unban, ok := userRepo.(userUnbanTx); ok {
		svc.unbanRepo = unban
	}
	if ban, ok := userRepo.(userBanTx); ok {
		svc.banRepo = ban
	}
	// 列表窄查询挂在 tenant member 仓储上，同样自动探测以保持三参构造。
	if listRepo, ok := memberRepo.(userManagementListRepo); ok {
		svc.listRepo = listRepo
	}
	// 多空间视图（T16 scope=other/all）窄查询同上自动探测。
	if scopedRepo, ok := memberRepo.(userManagementScopedListRepo); ok {
		svc.scopedListRepo = scopedRepo
	}
	return svc
}

// WithLatestBanLookup 显式注入"全平台最近一次成功 ban"的窄查询。
// lookup 为 nil 时普通 owner 的解封一律 403（失败关闭）。
func (s *userManagementService) WithLatestBanLookup(lookup interfaces.LatestBanAuditLookup) {
	s.latestBan = lookup
}

// userUnbanTx 是"事务内复核最新 ban + 解封"的窄接口，由真实 user
// repository 实现。测试替身可不实现，服务会回退旧路径。
type userUnbanTx interface {
	UnbanUserTx(
		ctx context.Context,
		tenantID uint64,
		targetUserID string,
		actorID string,
		isSystemAdmin bool,
		expectedTargetRole types.TenantRole,
	) (*types.User, error)
}

// userBanTx 是"状态变更与 ban 审计同事务"的窄接口，由真实 user
// repository 实现。测试替身可不实现，服务会回退旧路径。
type userBanTx interface {
	BanUserTx(
		ctx context.Context,
		tenantID uint64,
		targetUserID string,
		actorID string,
		expectedTargetRole types.TenantRole,
		audit *types.AuditLog,
	) (*types.User, error)
}

// managedTarget 是一次状态变更的目标快照：用户行 + 本租户的成员关系。
type managedTarget struct {
	user   *types.User
	member *types.TenantMember
}

// BanUser 封禁目标账号。成功后写 rbac.member_banned；重复调用幂等。
func (s *userManagementService) BanUser(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	reason string,
) (*types.User, error) {
	target, err := s.loadTarget(ctx, tenantID, targetUserID)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, target); err != nil {
		return nil, err
	}

	// 生产路径：状态变更与 ban 审计在同一事务内提交，解封事务读到的
	// “最近一次 ban”因此一定是完整、已提交的记录。
	if s.banRepo != nil {
		var audit *types.AuditLog
		if s.audit != nil {
			audit = statusAuditEntry(ctx, tenantID, target,
				types.AuditActionMemberBanned, reason)
		}
		// expectedTargetRole 让事务内复核目标角色未被并发提升；目标保护
		// 因此不再只依赖事务外的快照（N9 / N20）。
		user, err := s.banRepo.BanUserTx(
			ctx, tenantID, target.user.ID, auditActor(ctx), target.member.Role, audit)
		if err != nil {
			return nil, mapUserStatusError(err)
		}
		return user, nil
	}

	// 测试替身/无事务能力时的兼容路径（生产仓储始终实现 BanUserTx）。
	wasActive := target.user.IsActive
	user, err := s.userRepo.SetUserActive(ctx, target.user.ID, auditActor(ctx), false)
	if err != nil {
		return nil, mapUserStatusError(err)
	}

	// 只有真正发生 active→inactive 时才写 ban 审计。重复 ban 不写新审计，
	// 否则会改写“全平台最近一次 ban 租户”，让别处 owner 失去解封权。
	if wasActive {
		s.emitStatusAudit(ctx, tenantID, target,
			types.AuditActionMemberBanned, reason)
	}
	return user, nil
}

// UnbanUser 解封目标账号。普通 owner 只能解封最近一次 ban 发生在本租户的
// 账号；系统管理员可跨租户解封。成功后写 rbac.member_unbanned。
func (s *userManagementService) UnbanUser(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
	reason string,
) (*types.User, error) {
	target, err := s.loadTarget(ctx, tenantID, targetUserID)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, target); err != nil {
		return nil, err
	}

	isSystemAdmin := types.IsSystemAdminFromContext(ctx)

	// 生产路径：检查与解封在同一事务内完成，避免并发新 ban 被旧来源的
	// 解封覆盖。事务内还会再查一次最新 ban，不能依赖事务外的预检查。
	if s.unbanRepo != nil {
		// expectedTargetRole 让事务内复核目标角色未被并发提升；目标保护
		// 因此不再只依赖事务外的快照（与 ban/purge 的 B2 模式一致）。
		user, err := s.unbanRepo.UnbanUserTx(
			ctx, tenantID, target.user.ID, auditActor(ctx), isSystemAdmin, target.member.Role)
		if err != nil {
			switch {
			case errors.Is(err, apprepo.ErrUnbanNotLatestTenant):
				return nil, apperrors.NewForbiddenError(
					"this account was banned in another workspace; ask a system administrator")
			case errors.Is(err, apprepo.ErrUserNotFound):
				return nil, apperrors.NewNotFoundError("user not found")
			case errors.Is(err, apprepo.ErrRoleChanged):
				return nil, apperrors.NewConflictError(
					"the member's role changed; refresh and retry")
			default:
				return nil, err
			}
		}
		s.emitStatusAudit(ctx, tenantID, target,
			types.AuditActionMemberUnbanned, reason)
		return user, nil
	}

	// 测试替身/无事务能力时的兼容路径（生产仓储始终实现 UnbanUserTx）。
	if !isSystemAdmin {
		// 只有"最近一次 ban 发生在本租户"才允许普通 owner 解封。
		// 查询缺失、报错或无记录都按失败关闭处理。
		if !s.latestBanIsInTenant(ctx, tenantID, target.user.ID) {
			return nil, apperrors.NewForbiddenError(
				"this account was banned in another workspace; ask a system administrator")
		}
	}

	user, err := s.userRepo.SetUserActive(ctx, target.user.ID, auditActor(ctx), true)
	if err != nil {
		return nil, mapUserStatusError(err)
	}

	s.emitStatusAudit(ctx, tenantID, target,
		types.AuditActionMemberUnbanned, reason)
	return user, nil
}

// loadTarget 要求目标必须是本租户的 active 成员，否则 404；用户行缺失
// 同样按 404 处理（不泄露"账号存在但成员关系没了"的内部状态）。
func (s *userManagementService) loadTarget(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
) (*managedTarget, error) {
	if targetUserID == "" {
		return nil, apperrors.NewValidationError("target user id is required")
	}
	member, err := s.memberRepo.Get(ctx, targetUserID, tenantID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.Status != types.TenantMemberStatusActive {
		return nil, apperrors.NewNotFoundError("membership not found")
	}
	user, err := s.userRepo.GetUserByID(ctx, targetUserID)
	if err != nil {
		if errors.Is(err, apprepo.ErrUserNotFound) {
			return nil, apperrors.NewNotFoundError("user not found")
		}
		return nil, err
	}
	return &managedTarget{user: user, member: member}, nil
}

// authorize 实现 T06 的目标保护矩阵：
//   - 任何人都不能操作自己（含系统管理员）；
//   - 系统管理员可操作任何目标（含 owner）；
//   - 本空间 owner 与普通管理员都能继续，但普通管理员不能操作 owner/同级；
//   - 目标不能是本空间 owner（owner 只能先交接）；
//   - owner/admin 不能操作系统管理员 / 跨空间超管；
//   - 不能操作在任意空间持有 owner 角色的账号。
//
// 角色/目标保护先于任何账号状态写入；被拒绝的请求不会写成功审计。
func (s *userManagementService) authorize(ctx context.Context, target *managedTarget) error {
	actorID, _ := types.UserIDFromContext(ctx)
	if actorID != "" && actorID == target.user.ID {
		return apperrors.NewForbiddenError("you cannot change your own account status")
	}
	if types.IsSystemAdminFromContext(ctx) {
		return nil
	}
	callerRole := types.TenantRoleFromContext(ctx)
	if callerRole != types.TenantRoleOwner && callerRole != types.TenantRoleAdmin {
		return apperrors.NewForbiddenError(
			"only workspace owners or administrators can manage member status")
	}
	if err := memberTargetProtection(ctx, target.user.ID, target.member.Role, false); err != nil {
		return err
	}
	if target.user.IsSystemAdmin || target.user.CanAccessAllTenants {
		return apperrors.NewForbiddenError(
			"system administrators and cross-workspace superusers are managed by a system administrator")
	}
	holdsOwner, err := s.holdsOwnerRole(ctx, target.user.ID)
	if err != nil {
		return err
	}
	if holdsOwner {
		return apperrors.NewForbiddenError(
			"owner accounts can only be managed by a system administrator")
	}
	return nil
}

// holdsOwnerRole 检查目标是否在任意空间持有 active owner 角色。
func (s *userManagementService) holdsOwnerRole(ctx context.Context, userID string) (bool, error) {
	members, err := s.memberRepo.ListByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member.Role == types.TenantRoleOwner &&
			member.Status == types.TenantMemberStatusActive {
			return true, nil
		}
	}
	return false, nil
}

// latestBanIsInTenant 判断目标"最近一次成功 ban"是否发生在本租户。
// 窄查询未注入、查询报错或没有记录时一律返回 false（失败关闭），
// 只有系统管理员能绕过这条限制。
func (s *userManagementService) latestBanIsInTenant(
	ctx context.Context,
	tenantID uint64,
	targetUserID string,
) bool {
	if s.latestBan == nil {
		return false
	}
	latest, err := s.latestBan.LatestBanAuditForTarget(ctx, targetUserID)
	if err != nil {
		return false
	}
	return latest != nil && latest.TenantID == tenantID
}

// emitStatusAudit 写封禁/解封审计。审计是尽力而为：写失败不影响已经
// 完成的账号状态变更（与 tenant_member 服务的 emitAudit 语义一致）。
func (s *userManagementService) emitStatusAudit(
	ctx context.Context,
	tenantID uint64,
	target *managedTarget,
	action types.AuditAction,
	reason string,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, statusAuditEntry(ctx, tenantID, target, action, reason))
}

// statusAuditEntry 组装租户级封禁/解封审计行。封禁事务会把同一份条目
// 与状态变更一起提交。
func statusAuditEntry(
	ctx context.Context,
	tenantID uint64,
	target *managedTarget,
	action types.AuditAction,
	reason string,
) *types.AuditLog {
	details, _ := json.Marshal(map[string]string{
		"target_role": string(target.member.Role),
		"reason":      reason,
	})
	return &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: auditActor(ctx),
		ActorRole:   auditActorRole(ctx),
		Action:      action,
		// 空间操作记录读接口按 scope_type='' 过滤（UnscopedOnly），
		// 这里必须与 tenant_member 服务的既有成员审计保持一致，否则
		// 封禁/解封不会出现在操作记录里（F18）。
		ScopeID:      target.user.ID,
		TargetType:   "tenant_member",
		TargetUserID: target.user.ID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      types.JSON(details),
	}
}

// mapUserStatusError 把仓储层的账号状态保护错误翻译成 AppError，
// 让 handler 能稳定映射 403/404。
func mapUserStatusError(err error) error {
	switch {
	case errors.Is(err, apprepo.ErrUserNotFound):
		return apperrors.NewNotFoundError("user not found")
	case errors.Is(err, apprepo.ErrCannotDisableSelf):
		return apperrors.NewForbiddenError("you cannot change your own account status")
	case errors.Is(err, apprepo.ErrCannotDisableLastAdmin),
		errors.Is(err, apprepo.ErrLastSystemAdmin):
		return apperrors.NewForbiddenError(
			"cannot disable the last remaining system administrator")
	case errors.Is(err, apprepo.ErrRoleChanged):
		// 目标角色在事务内已变化（并发提权/降级），按冲突让调用方刷新重试。
		return apperrors.NewConflictError("the member's role changed; refresh and retry")
	default:
		return err
	}
}
