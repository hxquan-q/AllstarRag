package handler

// T04 TDD 红阶段：两条管理端重置入口对员工账号都必须拒绝。
//
// owner 重置成员（TenantMemberHandler.ResetMemberPassword）与超管重置
// （SystemHandler.ResetUserPassword）最终都调用 userService.AdminResetPassword；
// 这里用真实 userService + 员工 lookup 替身，验证两条 HTTP 入口都不会写密码。

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	yzjT04HandlerEmployeeID = "u-emp-0001"
	yzjT04HandlerEmail      = "emp-placeholder@example.com"
)

type yzjT04HandlerLookup struct {
	employee bool
}

func (l *yzjT04HandlerLookup) IsYunzhijiaEmployee(context.Context, string) (bool, error) {
	return l.employee, nil
}

type yzjT04HandlerUserRepo struct {
	interfaces.UserRepository
	users   map[string]*types.User
	updates int
	resets  int
}

func (r *yzjT04HandlerUserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *yzjT04HandlerUserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	for _, user := range r.users {
		if user != nil && user.Email == email {
			return user, nil
		}
	}
	return nil, errors.New("user not found")
}

func (r *yzjT04HandlerUserRepo) UpdateUser(context.Context, *types.User) error {
	r.updates++
	return nil
}

// ResetPasswordTx 是 service 内部的事务写入窄接口；实现它是为了让
// 「未拒绝时会写密码」在测试里可观测。
func (r *yzjT04HandlerUserRepo) ResetPasswordTx(
	context.Context, string, uint64, bool, bool, string,
) (*types.User, error) {
	r.resets++
	return nil, nil
}

type yzjT04HandlerTokenRepo struct {
	interfaces.AuthTokenRepository
	revoked []string
}

func (r *yzjT04HandlerTokenRepo) RevokeTokensByUserID(_ context.Context, userID string) error {
	r.revoked = append(r.revoked, userID)
	return nil
}

func yzjT04HandlerRepo() *yzjT04HandlerUserRepo {
	return &yzjT04HandlerUserRepo{
		users: map[string]*types.User{
			yzjT04HandlerEmployeeID: {
				ID:           yzjT04HandlerEmployeeID,
				Username:     yzjT04HandlerEmployeeID,
				Email:        yzjT04HandlerEmail,
				PasswordHash: "placeholder-hash",
				IsActive:     true,
			},
		},
	}
}

func TestYunzhijiaT04OwnerResetMemberPasswordRejectsEmployee(t *testing.T) {
	repo := yzjT04HandlerRepo()
	tokenRepo := &yzjT04HandlerTokenRepo{}
	userSvc := service.NewUserService(
		nil, repo, tokenRepo, nil, nil, nil, &yzjT04HandlerLookup{employee: true},
	)

	memberSvc := &accountTestMemberService{
		getMembership: func(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
			return &types.TenantMember{
				UserID:   userID,
				TenantID: tenantID,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusActive,
			}, nil
		},
	}
	h := NewTenantMemberHandler(memberSvc, userSvc)

	w := doAccountJSON(t, accountTestRouter(h), http.MethodPost,
		"/tenants/1/members/"+yzjT04HandlerEmployeeID+"/reset-password",
		map[string]any{"new_password": "FreshPass9"},
		accountCtxOpts{userID: "u-owner", role: types.TenantRoleOwner})

	if w.Code == http.StatusOK {
		t.Fatalf("owner reset of employee must be rejected, got 200 body=%s", w.Body.String())
	}
	if repo.updates != 0 || repo.resets != 0 {
		t.Fatalf("owner reset of employee must not write a password, updates=%d resets=%d",
			repo.updates, repo.resets)
	}
	if len(tokenRepo.revoked) != 0 {
		t.Fatalf("owner reset of employee must not revoke tokens, revoked=%v", tokenRepo.revoked)
	}
}

func TestYunzhijiaT04SystemAdminResetPasswordRejectsEmployee(t *testing.T) {
	repo := yzjT04HandlerRepo()
	tokenRepo := &yzjT04HandlerTokenRepo{}
	userSvc := service.NewUserService(
		nil, repo, tokenRepo, nil, nil, nil, &yzjT04HandlerLookup{employee: true},
	)
	h := &SystemHandler{userSvc: userSvc}

	w := performPasswordReset(t, passwordResetRouter(h, "admin-user"), map[string]string{
		"email":        yzjT04HandlerEmail,
		"new_password": "FreshPass9",
	})

	if w.Code == http.StatusOK {
		t.Fatalf("system admin reset of employee must be rejected, got 200 body=%s", w.Body.String())
	}
	if repo.updates != 0 || repo.resets != 0 {
		t.Fatalf("system admin reset of employee must not write a password, updates=%d resets=%d",
			repo.updates, repo.resets)
	}
	if len(tokenRepo.revoked) != 0 {
		t.Fatalf("system admin reset of employee must not revoke tokens, revoked=%v", tokenRepo.revoked)
	}
}
