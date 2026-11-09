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
	if locker, ok := s.userRepo.(userProvisionLocker); ok {
		return locker.LockUserProvision(ctx, userID)
	}
	value, _ := provisionFallbackLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
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
		if err := s.adoptHomeTenant(ctx, target, tenant.ID); err != nil {
			return nil, err
		}
		s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
			targetUserID, map[string]string{"space_name": tenant.Name, "reused": "true"})
		return tenant, nil
	}

	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{
		Name: defaultOwnerSpaceName(spaceName, target.Username),
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.memberService.EnsureOwner(ctx, targetUserID, tenant.ID); err != nil {
		// 空间刚建、还没有任何成员，删除不会留下悬挂引用。
		_ = s.tenantService.DeleteTenant(ctx, tenant.ID)
		return nil, err
	}
	if err := s.adoptHomeTenant(ctx, target, tenant.ID); err != nil {
		return nil, err
	}
	s.emitOwnerAudit(ctx, tenant.ID, types.AuditActionOwnerProvisioned,
		targetUserID, map[string]string{"space_name": tenant.Name})
	return tenant, nil
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
