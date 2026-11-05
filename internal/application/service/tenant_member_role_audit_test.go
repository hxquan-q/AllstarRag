package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// T12 TDD 红阶段：service.UpdateRole 的回归保护网。
//
// T12 的目标保护落在 handler 层；service.UpdateRole 仍是最后一位 owner
// 原子保护 + 审计的唯一出口（transfer-owner 内部流程也复用），因此这里
// 锁定两件事：
//   - 成功改角色写 rbac.member_role_changed，details 含 before/after；
//   - 非 owner -> owner 的 co-owner 提升仍允许。
//
// 这两条在当前实现下应为绿（保护网）；若实现把保护下沉到 service，
// 必须保持审计字段与升级语义不变。

// capturingAuditLogService records Log calls for assertions. The other
// AuditLogService methods are no-ops: UpdateRole only reaches Log.
type capturingAuditLogService struct {
	entries []*types.AuditLog
}

func (s *capturingAuditLogService) Log(_ context.Context, entry *types.AuditLog) error {
	cp := *entry
	s.entries = append(s.entries, &cp)
	return nil
}

func (s *capturingAuditLogService) LogDenied(
	context.Context, *gin.Context, uint64, string, string, types.TenantRole,
) error {
	return nil
}

func (s *capturingAuditLogService) List(
	context.Context, uint64, *interfaces.AuditLogQuery,
) ([]*types.AuditLog, error) {
	return nil, nil
}

func (s *capturingAuditLogService) Purge(context.Context, int) (int64, error) { return 0, nil }

var _ interfaces.AuditLogService = (*capturingAuditLogService)(nil)

func TestTenantMemberService_UpdateRole_EmitsAuditWithBeforeAfter(t *testing.T) {
	repo := newFakeRepo()
	audit := &capturingAuditLogService{}
	svc := NewTenantMemberService(repo, audit, nil, nil)

	ctx := context.Background()
	if _, err := svc.EnsureOwner(ctx, "u-owner", 9); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.AddMember(ctx, "u-target", 9, types.TenantRoleViewer, nil); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	ctx = context.WithValue(ctx, types.UserIDContextKey, "u-owner")
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleOwner)

	if err := svc.UpdateRole(ctx, "u-target", 9, types.TenantRoleAdmin); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}

	// AddMember also audits (rbac.member_added); only the role-change row is
	// relevant here.
	var roleChanged []*types.AuditLog
	for _, e := range audit.entries {
		if e.Action == types.AuditActionMemberRoleChanged {
			roleChanged = append(roleChanged, e)
		}
	}
	require.Len(t, roleChanged, 1, "a successful role change must write exactly one audit row")
	entry := roleChanged[0]
	require.Equal(t, types.AuditActionMemberRoleChanged, entry.Action)
	require.Equal(t, uint64(9), entry.TenantID)
	require.Equal(t, "u-owner", entry.ActorUserID)
	require.Equal(t, string(types.TenantRoleOwner), entry.ActorRole)
	require.Equal(t, "u-target", entry.TargetUserID)
	require.Equal(t, "tenant_member", entry.TargetType)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)

	var details map[string]string
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, string(types.TenantRoleViewer), details["old_role"], "details must carry the before role")
	require.Equal(t, string(types.TenantRoleAdmin), details["new_role"], "details must carry the after role")
}

func TestTenantMemberService_UpdateRole_PromotesToCoOwner(t *testing.T) {
	svc, repo := newServiceWithRepo()
	if _, err := svc.EnsureOwner(context.Background(), "owner", 1); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := svc.AddMember(context.Background(), "target", 1, types.TenantRoleViewer, nil); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	if err := svc.UpdateRole(context.Background(), "target", 1, types.TenantRoleOwner); err != nil {
		t.Fatalf("promoting a viewer to co-owner must succeed, got %v", err)
	}
	ownerCount, err := repo.CountActiveOwners(context.Background(), 1)
	if err != nil {
		t.Fatalf("count owners: %v", err)
	}
	require.Equal(t, int64(2), ownerCount, "co-owner promotion must leave two active owners")
}
