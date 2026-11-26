package handler

// T04：两条管理端重置入口对员工账号都必须拒绝，普通账号保持可用（AC-7）。
//
// R4（F1）后员工身份只看 users 行的 preferences.yunzhijia_employee 标记，
// 不再有 lookup 依赖；这里用真实 userService + 带标记的 users 行验证
// 两条 HTTP 入口都不会写密码。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

const (
	yzjT04HandlerEmployeeID = "u-emp-0001"
	yzjT04HandlerNormalID   = "u-normal-0001"
	yzjT04HandlerEmail      = "emp-placeholder@example.com"
	yzjT04HandlerNormEmail  = "normal-placeholder@example.com"
)

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
	created int
}

func (r *yzjT04HandlerTokenRepo) CreateToken(context.Context, *types.AuthToken) error {
	r.created++
	return nil
}

func (r *yzjT04HandlerTokenRepo) RevokeTokensByUserID(_ context.Context, userID string) error {
	r.revoked = append(r.revoked, userID)
	return nil
}

func yzjT04HandlerRepo() *yzjT04HandlerUserRepo {
	employeeMarker := true
	return &yzjT04HandlerUserRepo{
		users: map[string]*types.User{
			yzjT04HandlerEmployeeID: {
				ID:           yzjT04HandlerEmployeeID,
				Username:     yzjT04HandlerEmployeeID,
				Email:        yzjT04HandlerEmail,
				PasswordHash: "placeholder-hash",
				IsActive:     true,
				Preferences: types.UserPreferences{
					YunzhijiaEmployee: &employeeMarker,
				},
			},
			yzjT04HandlerNormalID: {
				ID:           yzjT04HandlerNormalID,
				Username:     yzjT04HandlerNormalID,
				Email:        yzjT04HandlerNormEmail,
				PasswordHash: "placeholder-hash",
				IsActive:     true,
			},
		},
	}
}

func TestYunzhijiaT04OwnerResetMemberPasswordRejectsEmployee(t *testing.T) {
	repo := yzjT04HandlerRepo()
	tokenRepo := &yzjT04HandlerTokenRepo{}
	userSvc := service.NewUserService(nil, repo, tokenRepo, nil, nil, nil)

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

	if w.Code != http.StatusForbidden {
		t.Fatalf("owner reset of employee must return 403, got %d body=%s", w.Code, w.Body.String())
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
	userSvc := service.NewUserService(nil, repo, tokenRepo, nil, nil, nil)
	h := &SystemHandler{userSvc: userSvc}

	w := performPasswordReset(t, passwordResetRouter(h, "admin-user"), map[string]string{
		"email":        yzjT04HandlerEmail,
		"new_password": "FreshPass9",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("system admin reset of employee must return 403, got %d body=%s", w.Code, w.Body.String())
	}
	if repo.updates != 0 || repo.resets != 0 {
		t.Fatalf("system admin reset of employee must not write a password, updates=%d resets=%d",
			repo.updates, repo.resets)
	}
	if len(tokenRepo.revoked) != 0 {
		t.Fatalf("system admin reset of employee must not revoke tokens, revoked=%v", tokenRepo.revoked)
	}
}

// F1 blast radius：普通账号的管理端重置不受员工标记迁移影响。
func TestYunzhijiaT04SystemAdminResetPasswordAllowsOrdinaryAccount(t *testing.T) {
	repo := yzjT04HandlerRepo()
	tokenRepo := &yzjT04HandlerTokenRepo{}
	userSvc := service.NewUserService(nil, repo, tokenRepo, nil, nil, nil)
	h := &SystemHandler{userSvc: userSvc}

	w := performPasswordReset(t, passwordResetRouter(h, "admin-user"), map[string]string{
		"email":        yzjT04HandlerNormEmail,
		"new_password": "FreshPass9",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("ordinary account reset must succeed, got %d body=%s", w.Code, w.Body.String())
	}
	if repo.resets != 1 {
		t.Fatalf("ordinary account reset must persist once, resets=%d", repo.resets)
	}
	if len(tokenRepo.revoked) != 1 || tokenRepo.revoked[0] != yzjT04HandlerNormalID {
		t.Fatalf("ordinary account reset must revoke sessions, revoked=%v", tokenRepo.revoked)
	}
}

// T04 票级复跑补充：员工账号即使知道合成邮箱与密码，HTTP 登录也必须
// 返回统一的 401 文案且不下发任何 token。
func TestYunzhijiaT04HandlerLoginEmployeeRejectedWithUnifiedMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := yzjT04HandlerRepo()
	tokenRepo := &yzjT04HandlerTokenRepo{}
	userSvc := service.NewUserService(nil, repo, tokenRepo, nil, nil, nil)
	h := NewAuthHandler(&config.Config{Auth: &config.AuthConfig{}},
		userSvc, nil, nil, nil, nil, nil, nil)

	r := gin.New()
	r.POST("/auth/login", h.Login)

	body, err := json.Marshal(map[string]string{
		"email":    yzjT04HandlerEmail,
		"password": "OldPass123!",
	})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("employee password login must return 401, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success      bool   `json:"success"`
		Message      string `json:"message"`
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login response: %v body=%s", err, w.Body.String())
	}
	if resp.Success {
		t.Fatalf("employee password login must not succeed, body=%s", w.Body.String())
	}
	if resp.Message != "Invalid email or password" {
		t.Fatalf("employee rejection must use the unified message, got %q", resp.Message)
	}
	if resp.Token != "" || resp.RefreshToken != "" {
		t.Fatalf("employee rejection must not return tokens, body=%s", w.Body.String())
	}
	if tokenRepo.created != 0 || len(tokenRepo.revoked) != 0 {
		t.Fatalf("employee rejection must not touch tokens, created=%d revoked=%v",
			tokenRepo.created, tokenRepo.revoked)
	}
}
