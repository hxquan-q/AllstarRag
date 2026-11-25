package service

// T04：员工账号禁密码（AC-7 / INV-6）。
//
// R4（F1）后的实现契约：
//   - 员工身份只看已加载 users 行的 preferences.yunzhijia_employee 标记，
//     不再查询 yunzhijia_employees 表；
//   - NewUserService 固定六参构造，没有员工目录 lookup 依赖；
//   - Login / ChangePassword / AdminResetPassword 对带标记的账号统一拒绝；
//     普通账号不依赖员工目录，三条路径照旧可用。
//
// 所有账号、邮箱、密码均为占位数据，不使用真实员工信息。

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"golang.org/x/crypto/bcrypt"
)

func init() {
	_ = os.Setenv("JWT_SECRET", "t04-password-guard-test-secret")
}

const (
	yzjT04EmployeeID = "u-emp-0001"
	yzjT04NormalID   = "u-normal-0001"
	yzjT04Email      = "emp-placeholder@example.com"
	yzjT04OldPass    = "OldPass123!"
	yzjT04NewPass    = "FreshPass9"
	yzjT04ResetPass  = "ResetPass9"
)

// yzjT04UserRepo 覆盖员工禁密码用例需要的最小用户仓储行为。
type yzjT04UserRepo struct {
	users       map[string]*types.User
	updateCalls int
}

func (r *yzjT04UserRepo) CreateUser(context.Context, *types.User) error { return nil }

func (r *yzjT04UserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *yzjT04UserRepo) GetUsersByIDs(context.Context, []string) (map[string]*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	for _, user := range r.users {
		if user != nil && user.Email == email {
			return user, nil
		}
	}
	return nil, errors.New("user not found")
}

func (r *yzjT04UserRepo) GetUserByUsername(context.Context, string) (*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) UpdateUser(_ context.Context, _ *types.User) error {
	r.updateCalls++
	return nil
}

func (r *yzjT04UserRepo) SetUserActive(context.Context, string, string, bool) (*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) DeleteUser(context.Context, string) error { return nil }

func (r *yzjT04UserRepo) ListUsers(context.Context, int, int) ([]*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) ListSystemAdmins(context.Context, int, int) ([]*types.User, int64, error) {
	return nil, 0, nil
}

func (r *yzjT04UserRepo) RevokeSystemAdmin(context.Context, string, string) (*types.User, error) {
	return nil, nil
}

func (r *yzjT04UserRepo) SearchUsers(context.Context, string, int) ([]*types.User, error) {
	return nil, nil
}

type yzjT04TokenRepo struct {
	createCalls int
	revoked     []string
}

func (r *yzjT04TokenRepo) CreateToken(context.Context, *types.AuthToken) error {
	r.createCalls++
	return nil
}

func (r *yzjT04TokenRepo) GetTokenByValue(context.Context, string) (*types.AuthToken, error) {
	return nil, errors.New("not implemented")
}

func (r *yzjT04TokenRepo) GetTokenByID(context.Context, string) (*types.AuthToken, error) {
	return nil, errors.New("not implemented")
}

func (r *yzjT04TokenRepo) GetTokensByUserID(context.Context, string) ([]*types.AuthToken, error) {
	return nil, nil
}

func (r *yzjT04TokenRepo) UpdateToken(context.Context, *types.AuthToken) error { return nil }

func (r *yzjT04TokenRepo) DeleteToken(context.Context, string) error { return nil }

func (r *yzjT04TokenRepo) DeleteExpiredTokens(context.Context) error { return nil }

func (r *yzjT04TokenRepo) RevokeTokensByUserID(_ context.Context, userID string) error {
	r.revoked = append(r.revoked, userID)
	return nil
}

func yzjT04Hash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return string(hash)
}

func yzjT04BoolPtr(v bool) *bool { return &v }

// yzjT04User 构造一条普通账号（不带员工标记）。
func yzjT04User(t *testing.T, id, email string, active bool) *types.User {
	t.Helper()
	return &types.User{
		ID:           id,
		Username:     id,
		Email:        email,
		PasswordHash: yzjT04Hash(t, yzjT04OldPass),
		IsActive:     active,
	}
}

// yzjT04EmployeeUser 构造一条由同步开号写入员工标记的账号。
func yzjT04EmployeeUser(t *testing.T, id, email string, active bool) *types.User {
	t.Helper()
	user := yzjT04User(t, id, email, active)
	user.Preferences.YunzhijiaEmployee = yzjT04BoolPtr(true)
	return user
}

func yzjT04RepoWith(users ...*types.User) *yzjT04UserRepo {
	byID := make(map[string]*types.User, len(users))
	for _, user := range users {
		byID[user.ID] = user
	}
	return &yzjT04UserRepo{users: byID}
}

// yzjT04Service 用六参构造；员工判定只依赖 users 行，没有员工目录可注入。
func yzjT04Service(
	t *testing.T,
	repo interfaces.UserRepository,
	tokens interfaces.AuthTokenRepository,
) interfaces.UserService {
	t.Helper()
	return NewUserService(nil, repo, tokens, nil, nil, nil)
}

func TestYunzhijiaT04EmployeePasswordLoginRejected(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04EmployeeUser(t, yzjT04EmployeeID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	resp, err := svc.Login(context.Background(), &types.LoginRequest{
		Email:    yzjT04Email,
		Password: yzjT04OldPass,
	})
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if resp == nil || resp.Success {
		t.Fatalf("employee password login must fail, got %+v", resp)
	}
	if resp.Message != "Invalid email or password" {
		t.Fatalf("employee rejection must use the unified message, got %q", resp.Message)
	}
	if tokens.createCalls != 0 {
		t.Fatalf("employee rejection must not issue tokens, createToken calls=%d", tokens.createCalls)
	}
}

func TestYunzhijiaT04NonEmployeePasswordLoginStillWorks(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04User(t, yzjT04NormalID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	resp, err := svc.Login(context.Background(), &types.LoginRequest{
		Email:    yzjT04Email,
		Password: yzjT04OldPass,
	})
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("non-employee password login must still work, got %+v", resp)
	}
	if tokens.createCalls != 2 {
		t.Fatalf("non-employee login must issue access+refresh tokens, createToken calls=%d", tokens.createCalls)
	}
}

// 显式 false 也是普通账号：标记存在但为 false 时不得被当成员工。
func TestYunzhijiaT04EmployeeMarkerFalseIsOrdinaryAccount(t *testing.T) {
	user := yzjT04User(t, yzjT04NormalID, yzjT04Email, true)
	user.Preferences.YunzhijiaEmployee = yzjT04BoolPtr(false)
	repo := yzjT04RepoWith(user)
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	resp, err := svc.Login(context.Background(), &types.LoginRequest{
		Email:    yzjT04Email,
		Password: yzjT04OldPass,
	})
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("marker=false account must keep password login, got %+v", resp)
	}
}

func TestYunzhijiaT04EmployeeLoginRejectedWhenDisabled(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04EmployeeUser(t, yzjT04EmployeeID, yzjT04Email, false))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	resp, err := svc.Login(context.Background(), &types.LoginRequest{
		Email:    yzjT04Email,
		Password: yzjT04OldPass,
	})
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if resp == nil || resp.Success {
		t.Fatalf("disabled employee password login must fail, got %+v", resp)
	}
	if resp.Message != "Invalid email or password" {
		t.Fatalf("employee password rejection must not be gated by enabled; got %q", resp.Message)
	}
	if tokens.createCalls != 0 {
		t.Fatalf("disabled employee rejection must not issue tokens, createToken calls=%d", tokens.createCalls)
	}
}

func TestYunzhijiaT04EmployeeChangePasswordRejected(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04EmployeeUser(t, yzjT04EmployeeID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	err := svc.ChangePassword(context.Background(), yzjT04EmployeeID, yzjT04OldPass, yzjT04NewPass)
	if err == nil {
		t.Fatal("employee ChangePassword must be rejected")
	}
	if !errors.Is(err, ErrYunzhijiaEmployeePasswordDisabled) {
		t.Fatalf("employee ChangePassword must return the employee sentinel, got %v", err)
	}
	if errors.Is(err, ErrInvalidOldPassword) || errors.Is(err, ErrSamePassword) {
		t.Fatalf("employee guard must reject before password checks, got %v", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("employee ChangePassword must not write the user, updates=%d", repo.updateCalls)
	}
	if len(tokens.revoked) != 0 {
		t.Fatalf("employee ChangePassword must not revoke tokens, revoked=%v", tokens.revoked)
	}
}

func TestYunzhijiaT04NonEmployeeChangePasswordStillWorks(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04User(t, yzjT04NormalID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	err := svc.ChangePassword(context.Background(), yzjT04NormalID, yzjT04OldPass, yzjT04NewPass)
	if err != nil {
		t.Fatalf("non-employee ChangePassword must succeed, got %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("non-employee ChangePassword must persist once, updates=%d", repo.updateCalls)
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != yzjT04NormalID {
		t.Fatalf("non-employee ChangePassword must revoke sessions, revoked=%v", tokens.revoked)
	}
}

func TestYunzhijiaT04EmployeeAdminResetPasswordRejected(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04EmployeeUser(t, yzjT04EmployeeID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	err := svc.AdminResetPassword(context.Background(), yzjT04EmployeeID, yzjT04ResetPass)
	if err == nil {
		t.Fatal("employee AdminResetPassword must be rejected")
	}
	if !errors.Is(err, ErrYunzhijiaEmployeePasswordDisabled) {
		t.Fatalf("employee AdminResetPassword must return the employee sentinel, got %v", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("employee AdminResetPassword must not write the user, updates=%d", repo.updateCalls)
	}
	if len(tokens.revoked) != 0 {
		t.Fatalf("employee AdminResetPassword must not revoke tokens, revoked=%v", tokens.revoked)
	}
}

func TestYunzhijiaT04NonEmployeeAdminResetPasswordStillWorks(t *testing.T) {
	repo := yzjT04RepoWith(yzjT04User(t, yzjT04NormalID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	err := svc.AdminResetPassword(context.Background(), yzjT04NormalID, yzjT04ResetPass)
	if err != nil {
		t.Fatalf("non-employee AdminResetPassword must succeed, got %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("non-employee AdminResetPassword must persist once, updates=%d", repo.updateCalls)
	}
	if len(tokens.revoked) != 1 || tokens.revoked[0] != yzjT04NormalID {
		t.Fatalf("non-employee AdminResetPassword must revoke sessions, revoked=%v", tokens.revoked)
	}
}

// F1 blast radius：普通账号的三条密码路径都不依赖员工目录。这些用例的
// 构造里根本不存在员工仓储/lookup——只要实现还查一次员工表就无法通过。
func TestYunzhijiaT04OrdinaryAccountPasswordPathsDoNotNeedEmployeeDirectory(t *testing.T) {
	ctx := context.Background()
	repo := yzjT04RepoWith(yzjT04User(t, yzjT04NormalID, yzjT04Email, true))
	tokens := &yzjT04TokenRepo{}
	svc := yzjT04Service(t, repo, tokens)

	resp, err := svc.Login(ctx, &types.LoginRequest{Email: yzjT04Email, Password: yzjT04OldPass})
	if err != nil {
		t.Fatalf("ordinary login: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("ordinary login must succeed without employee directory, got %+v", resp)
	}

	if err := svc.ChangePassword(ctx, yzjT04NormalID, yzjT04OldPass, yzjT04NewPass); err != nil {
		t.Fatalf("ordinary ChangePassword must not need employee directory: %v", err)
	}
	if err := svc.AdminResetPassword(ctx, yzjT04NormalID, yzjT04ResetPass); err != nil {
		t.Fatalf("ordinary AdminResetPassword must not need employee directory: %v", err)
	}
	if repo.updateCalls != 2 {
		t.Fatalf("ordinary change+reset must each persist once, updates=%d", repo.updateCalls)
	}
	if len(tokens.revoked) != 2 {
		t.Fatalf("ordinary change+reset must each revoke, revoked=%v", tokens.revoked)
	}
}
