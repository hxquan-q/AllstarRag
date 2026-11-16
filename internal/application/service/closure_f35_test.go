package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// F35：纯系统管理员（IsSystemAdmin=true、CanAccessAllTenants=false、无成员关系）
// 切换到空间 42 后，登录/刷新解析必须保留 42，不能回退 home；且不写成员关系/偏好。
func TestResolveLoginTenantIDSystemAdminKeepsSwitchedTenant(t *testing.T) {
	pref := uint64(42)
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"admin": {
			ID:                  "admin",
			TenantID:            7,
			IsSystemAdmin:       true,
			CanAccessAllTenants: false,
			Preferences:         types.UserPreferences{LastActiveTenantID: &pref},
		},
	}}
	svc := newSwitchTenantTestService(repo, nil)

	stored, err := repo.GetUserByID(context.Background(), "admin")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if got := svc.resolveLoginTenantID(context.Background(), stored); got != 42 {
		t.Fatalf("system admin resolveLoginTenantID=%d, want 42", got)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("system admin 解析不应写用户/偏好，updateCalls=%d", repo.updateCalls)
	}
}

// 对照：普通用户无成员关系时仍回退 home，行为不变。
func TestResolveLoginTenantIDPlainUserWithoutMembershipFallsBackHome(t *testing.T) {
	pref := uint64(42)
	repo := &switchTenantUserRepo{users: map[string]types.User{
		"alice": {
			ID:          "alice",
			TenantID:    7,
			Preferences: types.UserPreferences{LastActiveTenantID: &pref},
		},
	}}
	svc := newSwitchTenantTestService(repo, nil)

	stored, err := repo.GetUserByID(context.Background(), "alice")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if got := svc.resolveLoginTenantID(context.Background(), stored); got != 7 {
		t.Fatalf("plain user resolveLoginTenantID=%d, want home 7", got)
	}
}
