package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// T24：纯系统管理员（IsSystemAdmin=true，CanAccessAllTenants=false）
// 切换空间不应要求 active 成员关系；普通用户仍要求成员关系。
// 封禁/墓碑账号由 auth 中间件在进入 service 前拒绝，不在本层重复判定。

// switchTenantMemberProbe 记录 service 是否尝试写成员关系。
type switchTenantMemberProbe struct {
	interfaces.TenantMemberService
	byTenant    map[uint64]*types.TenantMember
	getCalls    int
	addCalls    int
	ensureCalls int
}

func (p *switchTenantMemberProbe) GetMembership(
	_ context.Context, _ string, tenantID uint64,
) (*types.TenantMember, error) {
	p.getCalls++
	return p.byTenant[tenantID], nil
}

func (p *switchTenantMemberProbe) ListByUser(
	context.Context, string,
) ([]*types.TenantMember, error) {
	return nil, nil
}

func (p *switchTenantMemberProbe) AddMember(
	context.Context, string, uint64, types.TenantRole, *string,
) (*types.TenantMember, error) {
	p.addCalls++
	return nil, errors.New("unexpected AddMember during tenant switch")
}

func (p *switchTenantMemberProbe) EnsureOwner(
	context.Context, string, uint64,
) (*types.TenantMember, error) {
	p.ensureCalls++
	return nil, errors.New("unexpected EnsureOwner during tenant switch")
}

func TestSwitchTenantSystemAdminWithoutMembershipAllowed(t *testing.T) {
	ctx := context.Background()
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"admin": {ID: "admin", TenantID: 7, IsSystemAdmin: true},
	}}
	memberSvc := &switchTenantMemberProbe{byTenant: map[uint64]*types.TenantMember{}}
	svc := newSwitchTenantTestService(repo, memberSvc)
	user, _ := repo.GetUserByID(ctx, "admin")

	resp, err := svc.SwitchTenant(ctx, user, 42, "")
	if err != nil {
		t.Fatalf("pure system admin switch without membership: %v", err)
	}
	if resp == nil || resp.ActiveTenant == nil || resp.ActiveTenant.ID != 42 {
		t.Fatalf("active tenant = %+v, want 42", resp)
	}
	stored, _ := repo.GetUserByID(ctx, "admin")
	if stored.Preferences.LastActiveTenantID == nil || *stored.Preferences.LastActiveTenantID != 42 {
		t.Fatalf("LastActiveTenantID = %v, want 42", stored.Preferences.LastActiveTenantID)
	}
}

func TestSwitchTenantSystemAdminCreatesNoMembership(t *testing.T) {
	ctx := context.Background()
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"admin": {ID: "admin", TenantID: 7, IsSystemAdmin: true},
	}}
	memberSvc := &switchTenantMemberProbe{byTenant: map[uint64]*types.TenantMember{}}
	svc := newSwitchTenantTestService(repo, memberSvc)
	user, _ := repo.GetUserByID(ctx, "admin")

	if _, err := svc.SwitchTenant(ctx, user, 42, ""); err != nil {
		t.Fatalf("SwitchTenant: %v", err)
	}
	if memberSvc.addCalls != 0 || memberSvc.ensureCalls != 0 {
		t.Fatalf("membership writes = add:%d ensure:%d, want 0 (system admin switch must not create membership)",
			memberSvc.addCalls, memberSvc.ensureCalls)
	}
}

func TestSwitchTenantRegularUserWithoutMembershipStillRejected(t *testing.T) {
	ctx := context.Background()
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"alice": {ID: "alice", TenantID: 7},
	}}
	memberSvc := &switchTenantMemberProbe{byTenant: map[uint64]*types.TenantMember{}}
	svc := newSwitchTenantTestService(repo, memberSvc)
	user, _ := repo.GetUserByID(ctx, "alice")

	if _, err := svc.SwitchTenant(ctx, user, 42, ""); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("regular non-member: err = %v, want ErrMembershipNotFound", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("UpdateUser calls = %d, want 0", repo.updateCalls)
	}
}

// 保护网：跨空间超管原有放行行为不变（T24 AC5）。
func TestSwitchTenantCrossTenantSuperuserStillAllowed(t *testing.T) {
	ctx := context.Background()
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"super": {ID: "super", TenantID: 7, CanAccessAllTenants: true},
	}}
	memberSvc := &switchTenantMemberProbe{byTenant: map[uint64]*types.TenantMember{}}
	svc := newSwitchTenantTestService(repo, memberSvc)
	user, _ := repo.GetUserByID(ctx, "super")

	if _, err := svc.SwitchTenant(ctx, user, 42, ""); err != nil {
		t.Fatalf("cross-tenant superuser switch: %v", err)
	}
}
