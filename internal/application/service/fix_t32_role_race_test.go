package service

import (
	"context"
	"errors"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// T32 TDD 红阶段：角色变更“查角色 → 更新”之间的并发提权窗口。
//
// 契约（见 05-tests §46.4）：
//   - 新增 service.UpdateRoleGuarded(ctx, userID, tenantID, expectedRole,
//     newRole, allowOwnerDemotion)；事务内按 expectedRole 做 CAS，实际角色
//     已变化返回 ErrMembershipChanged。
//   - allowOwnerDemotion=false 时，实际角色 owner → 非 owner 返回
//     ErrOwnerDemotionRequiresTransfer（直接降级必须走 transfer-owner）。
//   - allowOwnerDemotion=true 仅供 transfer-owner 内部使用，仍保留最后
//     owner 保护。
//   - 拒绝零副作用、不写成功审计。
//
// 本文件用可控 repo 模拟事务内复核；当前实现缺少 UpdateRoleGuarded，
// 预期为红。

type t32GuardedRepo struct {
	*fakeTenantMemberRepo
}

var _ interface {
	UpdateRoleGuarded(
		ctx context.Context,
		userID string,
		tenantID uint64,
		expectedRole, newRole types.TenantRole,
		allowOwnerDemotion bool,
	) error
} = (*t32GuardedRepo)(nil)

func (r *t32GuardedRepo) UpdateRoleGuarded(
	_ context.Context,
	userID string,
	tenantID uint64,
	expectedRole, newRole types.TenantRole,
	allowOwnerDemotion bool,
) error {
	var target *types.TenantMember
	for _, e := range r.rows {
		if e.UserID == userID && e.TenantID == tenantID && !e.DeletedAt.Valid {
			target = e
			break
		}
	}
	if target == nil {
		return ErrMembershipNotFound
	}
	// 事务内复核：调用方读到的 expectedRole 已过期就拒绝。
	if target.Role != expectedRole {
		return ErrMembershipChanged
	}
	if target.Role == types.TenantRoleOwner && newRole != types.TenantRoleOwner {
		if !allowOwnerDemotion {
			return ErrOwnerDemotionRequiresTransfer
		}
		owners := 0
		for _, e := range r.rows {
			if e.TenantID == tenantID && !e.DeletedAt.Valid &&
				e.Status == types.TenantMemberStatusActive &&
				e.Role == types.TenantRoleOwner {
				owners++
			}
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	target.Role = newRole
	return nil
}

type t32GuardedService interface {
	UpdateRoleGuarded(
		ctx context.Context,
		userID string,
		tenantID uint64,
		expectedRole, newRole types.TenantRole,
		allowOwnerDemotion bool,
	) error
}

func newT32Env(t *testing.T) (t32GuardedService, *t32GuardedRepo, *capturingAuditLogService) {
	t.Helper()
	base := newFakeRepo()
	repo := &t32GuardedRepo{fakeTenantMemberRepo: base}
	audit := &capturingAuditLogService{}
	svc := NewTenantMemberService(repo, audit, nil, nil)
	guarded, ok := svc.(t32GuardedService)
	if !ok {
		t.Fatalf("UpdateRoleGuarded not implemented on TenantMemberService (T32 contract)")
	}
	return guarded, repo, audit
}

func roleChangeAudits(audit *capturingAuditLogService) []*types.AuditLog {
	var out []*types.AuditLog
	for _, e := range audit.entries {
		if e.Action == types.AuditActionMemberRoleChanged {
			out = append(out, e)
		}
	}
	return out
}

// 场景 1：调用方读到的 expectedRole 已过期（目标被并发提升为 owner），
// 降级请求必须被拒绝，不能依赖“最后 owner 保护”放行。
func TestT32StaleExpectedRoleRejected(t *testing.T) {
	svc, repo, audit := newT32Env(t)
	ctx := context.Background()
	const tenantID uint64 = 9

	if _, err := svc.(interface {
		EnsureOwner(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
	}).EnsureOwner(ctx, "u-target", tenantID); err != nil {
		t.Fatalf("seed target owner: %v", err)
	}

	// 调用方在并发前读到的是 viewer；目标已被提升为 owner。
	// 场景 1 只依赖目标当前是 owner，不需要第二个 owner 造数。
	err := svc.UpdateRoleGuarded(ctx, "u-target", tenantID,
		types.TenantRoleViewer, types.TenantRoleAdmin, false)
	if !errors.Is(err, ErrMembershipChanged) {
		t.Fatalf("stale expected role: err=%v, want ErrMembershipChanged", err)
	}
	current, err := repo.Get(ctx, "u-target", tenantID)
	if err != nil || current == nil {
		t.Fatalf("reload target: %v", err)
	}
	if current.Role != types.TenantRoleOwner {
		t.Fatalf("target role=%s, want owner (rejected request must not change role)", current.Role)
	}
	if got := roleChangeAudits(audit); len(got) != 0 {
		t.Fatalf("rejected stale update wrote %d role-change audits", len(got))
	}
}

// 场景 2：直接路径 owner → 非 owner 必须拒绝（必须走 transfer-owner）。
func TestT32DirectOwnerDemotionRejected(t *testing.T) {
	svc, repo, audit := newT32Env(t)
	ctx := context.Background()
	const tenantID uint64 = 9

	if _, err := svc.(interface {
		EnsureOwner(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
	}).EnsureOwner(ctx, "u-target", tenantID); err != nil {
		t.Fatalf("seed target owner: %v", err)
	}

	// 场景 2 只依赖「目标是 owner」；allowOwnerDemotion=false 在计数前就拒绝，
	// 不需要第二个 owner 造数。
	err := svc.UpdateRoleGuarded(ctx, "u-target", tenantID,
		types.TenantRoleOwner, types.TenantRoleAdmin, false)
	if !errors.Is(err, ErrOwnerDemotionRequiresTransfer) {
		t.Fatalf("direct owner demotion: err=%v, want ErrOwnerDemotionRequiresTransfer", err)
	}
	current, _ := repo.Get(ctx, "u-target", tenantID)
	if current == nil || current.Role != types.TenantRoleOwner {
		t.Fatalf("target role=%v, want owner", current)
	}
	if got := roleChangeAudits(audit); len(got) != 0 {
		t.Fatalf("rejected direct demotion wrote %d role-change audits", len(got))
	}
}

// 场景 3：transfer-owner 内部路径允许 owner 降级（allowOwnerDemotion=true），
// 但另一名 owner 必须存在。
func TestT32HandoverOwnerDemotionAllowed(t *testing.T) {
	svc, repo, _ := newT32Env(t)
	ctx := context.Background()
	const tenantID uint64 = 9

	if _, err := svc.(interface {
		EnsureOwner(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
	}).EnsureOwner(ctx, "u-target", tenantID); err != nil {
		t.Fatalf("seed target owner: %v", err)
	}
	// 唯一 owner 语义下 EnsureOwner 不会创建第二个 owner；交接内部路径
	// 需要「另一名 owner 存在」，因此直接经仓储写入 successor 的 owner 行。
	if err := repo.Create(ctx, &types.TenantMember{
		UserID:   "u-successor",
		TenantID: tenantID,
		Role:     types.TenantRoleOwner,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed successor owner: %v", err)
	}

	if err := svc.UpdateRoleGuarded(ctx, "u-target", tenantID,
		types.TenantRoleOwner, types.TenantRoleAdmin, true); err != nil {
		t.Fatalf("handover demotion: %v", err)
	}
	current, _ := repo.Get(ctx, "u-target", tenantID)
	if current == nil || current.Role != types.TenantRoleAdmin {
		t.Fatalf("target role=%v, want admin", current)
	}
}

// 场景 4：最后 owner 保护保持。
func TestT32LastOwnerStillProtected(t *testing.T) {
	svc, repo, _ := newT32Env(t)
	ctx := context.Background()
	const tenantID uint64 = 9

	if _, err := svc.(interface {
		EnsureOwner(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
	}).EnsureOwner(ctx, "u-only-owner", tenantID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	err := svc.UpdateRoleGuarded(ctx, "u-only-owner", tenantID,
		types.TenantRoleOwner, types.TenantRoleAdmin, true)
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner demotion: err=%v, want ErrLastOwner", err)
	}
	current, _ := repo.Get(ctx, "u-only-owner", tenantID)
	if current == nil || current.Role != types.TenantRoleOwner {
		t.Fatalf("target role=%v, want owner", current)
	}
}

// 场景 5：普通非 owner 改角色不受影响。
func TestT32NonOwnerRoleChangeStillAllowed(t *testing.T) {
	svc, repo, audit := newT32Env(t)
	ctx := context.Background()
	const tenantID uint64 = 9

	if _, err := svc.(interface {
		AddMember(ctx context.Context, userID string, tenantID uint64, role types.TenantRole, invitedBy *string) (*types.TenantMember, error)
	}).AddMember(ctx, "u-viewer", tenantID, types.TenantRoleViewer, nil); err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	if err := svc.UpdateRoleGuarded(ctx, "u-viewer", tenantID,
		types.TenantRoleViewer, types.TenantRoleAdmin, false); err != nil {
		t.Fatalf("viewer->admin: %v", err)
	}
	current, _ := repo.Get(ctx, "u-viewer", tenantID)
	if current == nil || current.Role != types.TenantRoleAdmin {
		t.Fatalf("target role=%v, want admin", current)
	}
	if got := roleChangeAudits(audit); len(got) != 1 {
		t.Fatalf("role-change audits=%d, want 1", len(got))
	}
}

var _ = apprepo.ErrLastOwner
