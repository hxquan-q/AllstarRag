package service

import (
	"context"
	"strings"
	"sync"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// userProvisionLocker 是仓储层“按用户串行化 owner 空间开通”的窄接口。
// 具体 *userRepository 在 PG/MySQL 上用 users 行锁事务，SQLite 用进程锁。
type userProvisionLocker interface {
	LockUserProvision(ctx context.Context, userID string) (func(), error)
}

// provisionFallbackLocks 仅在仓储不支持 LockUserProvision（测试替身）时兜底。
// 键为 userID；条目数与被开通为 owner 的账号数同阶，保留不删除。
var provisionFallbackLocks sync.Map

func (s *userManagementService) lockUserProvision(
	ctx context.Context,
	userID string,
) (func(), error) {
	// 与 TenantMemberService 的成员写入共用同一把按用户锁，保证
	// ProvisionOwnerSpace 与 AddMember / 邀请接受互相串行化。
	return acquireUserProvisionLock(ctx, s.userRepo, userID)
}

// ProvisionOwnerSpace 由系统管理员把已有账号设为 owner。
//
// 幂等规则（AC-11）：目标已经在任意空间持有 active owner 角色时复用该
// 空间，不重复建租户/成员；否则走 tenantService.CreateTenant +
// memberService.EnsureOwner，保证目标成为新空间唯一 owner，而调用者
// （系统管理员）不会顺带成为成员。
//
// 并发：查询与新建必须在同一把“按用户”锁内完成，否则并发请求会各自
// 建出一个空间。锁由 lockUserProvision 提供（PG/MySQL 用户行锁，SQLite
// 进程锁），释放前完成查/建/审计。
func (s *userManagementService) ProvisionOwnerSpace(
	ctx context.Context,
	targetUserID, spaceName string,
) (*types.Tenant, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.ownerDepsReady(); err != nil {
		return nil, err
	}
	targetUserID = strings.TrimSpace(targetUserID)
	if targetUserID == "" {
		return nil, apperrors.NewValidationError("target user id is required")
	}
	target, err := s.ensureTargetUser(ctx, targetUserID)
	if err != nil {
		return nil, err
	}

	// 串行化：同一账号的并发开通在这里排队，先查后建。
	release, err := s.lockUserProvision(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	defer release()

	existing, err := s.activeOwnerMembership(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		tenant, err := s.tenantService.GetTenantByID(ctx, existing.TenantID)
		if err != nil {
			return nil, err
		}
		if err := s.adoptHomeTenant(ctx, target.ID, tenant.ID); err != nil {
			return nil, err
		}
		s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
			targetUserID, map[string]string{"space_name": tenant.Name, "reused": "true"})
		return tenant, nil
	}

	// 一号一空间（T04）：非超管目标若已以 admin/viewer/invited 身份属于
	// 其他空间，必须在 CreateTenant 之前拒绝，不能「先建空间再回滚」
	// （AC-15/AC-16/AC-20）。owner 场景已在上面幂等复用。
	if err := s.assertTargetCanOwnNewSpace(ctx, target, targetUserID); err != nil {
		return nil, err
	}

	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{
		Name: defaultOwnerSpaceName(spaceName, target.Username),
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.ensureOwnerForProvision(ctx, targetUserID, tenant.ID); err != nil {
		// 空间刚建、还没有任何成员，删除不会留下悬挂引用。
		_ = s.tenantService.DeleteTenant(ctx, tenant.ID)
		return nil, err
	}
	if err := s.adoptHomeTenant(ctx, target.ID, tenant.ID); err != nil {
		return nil, err
	}
	s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
		targetUserID, map[string]string{"space_name": tenant.Name})
	return tenant, nil
}

// assertTargetCanOwnNewSpace 校验非超管目标尚未属于任何空间。
// 只有 active/invited 计入归属；suspended/deleted 不阻挡（AC-18）。
// 超管豁免（AC-17）。
func (s *userManagementService) assertTargetCanOwnNewSpace(
	ctx context.Context,
	target *types.User,
	userID string,
) error {
	if target == nil || target.IsSystemAdmin || target.CanAccessAllTenants {
		return nil
	}
	members, err := s.memberRepo.ListByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member == nil || member.DeletedAt.Valid || member.TenantID == 0 {
			continue
		}
		if member.Status == types.TenantMemberStatusActive ||
			member.Status == types.TenantMemberStatusInvited {
			return ErrUserAlreadyInSpace
		}
	}
	return nil
}

// ensureOwnerForProvision 在已持锁的 ProvisionOwnerSpace 内创建 owner 成员。
// 真实会员服务实现锁内版本（避免 PG advisory lock 自锁）；测试替身没有
// 时退化为公开的 EnsureOwner。
func (s *userManagementService) ensureOwnerForProvision(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (*types.TenantMember, error) {
	if locked, ok := s.memberService.(singleSpaceMemberWriter); ok {
		return locked.ensureOwnerLocked(ctx, userID, tenantID)
	}
	return s.memberService.EnsureOwner(ctx, userID, tenantID)
}

// compile-time 断言：T04 依赖注入契约需要具体实现暴露该 setter。
var _ interface {
	SetOwnerManagementDeps(
		interfaces.TenantService,
		interfaces.TenantMemberService,
		interfaces.TenantInvitationService,
		interfaces.UserService,
	)
} = (*userManagementService)(nil)
