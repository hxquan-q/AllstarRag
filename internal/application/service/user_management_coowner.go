package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// CreateCoOwnerAccount 由本空间 owner（或系统管理员）新建 co-owner 账号。
//
// 账号以 tenantless 模式创建（users.tenant_id=0），只通过 tenant_members
// 属于当前空间（AC-10）；角色直接是 owner，因此同一空间可以有多个 owner。
// 入组失败必须回滚刚建的账号，避免留下没有任何空间的孤儿（AC-9/T04-3）。
func (s *userManagementService) CreateCoOwnerAccount(
	ctx context.Context,
	tenantID uint64,
	req *interfaces.CreateCoOwnerRequest,
) (*types.User, error) {
	if err := s.requireOwnerOrSystemAdmin(ctx, tenantID); err != nil {
		return nil, err
	}
	if err := s.ownerDepsReady(); err != nil {
		return nil, err
	}
	username, email, password, err := validateCoOwnerRequest(req)
	if err != nil {
		return nil, err
	}
	// 与注册/重置密码走同一套密码策略；真实 userService 实现了这个窄接口，
	// 测试替身未实现时跳过（生产接线一定注入真实实现）。
	if validator, ok := s.userService.(interface {
		ValidatePasswordAgainstPolicy(context.Context, string) error
	}); ok {
		if err := validator.ValidatePasswordAgainstPolicy(ctx, password); err != nil {
			return nil, apperrors.NewValidationError(err.Error())
		}
	}

	user, err := s.userService.Register(ctx, &types.RegisterRequest{
		Username:           username,
		Email:              email,
		Password:           password,
		TenantProvisioning: types.TenantProvisioningTenantless,
	})
	if err != nil {
		return nil, mapUserIdentityError(err)
	}

	invitedBy := invitedByFromContext(ctx)
	if _, err := s.memberService.AddMember(
		ctx, user.ID, tenantID, types.TenantRoleOwner, invitedBy,
	); err != nil {
		s.rollbackNewUser(ctx, user.ID)
		return nil, mapMembershipError(err)
	}
	// rbac.member_added 已由 TenantMemberService.AddMember 写入，不重复。
	return user, nil
}

// validateCoOwnerRequest 校验必填字段。密码不做 trim：与 Register 一致，
// 按原字节哈希；空密码直接拒绝（此接口不生成随机密码）。
func validateCoOwnerRequest(req *interfaces.CreateCoOwnerRequest) (string, string, string, error) {
	if req == nil {
		return "", "", "", apperrors.NewValidationError("request body is required")
	}
	username := strings.TrimSpace(req.Username)
	email := strings.TrimSpace(req.Email)
	if username == "" {
		return "", "", "", apperrors.NewValidationError("username is required")
	}
	if email == "" {
		return "", "", "", apperrors.NewValidationError("email is required")
	}
	if req.Password == nil || *req.Password == "" {
		return "", "", "", apperrors.NewValidationError("password is required")
	}
	return username, email, *req.Password, nil
}

// invitedByFromContext 只记录真实人类调用者；API key 的合成用户留空，
// 与既有 AddMember 调用保持一致。
func invitedByFromContext(ctx context.Context) *string {
	caller, _ := types.UserIDFromContext(ctx)
	if caller == "" || types.IsSyntheticUserID(caller) {
		return nil
	}
	return &caller
}

// rollbackNewUser 物理删除刚建但未入组的账号。用不受请求取消影响的 ctx：
// 入组失败的常见原因就是客户端断连，拿原 ctx 做补偿删除必然失败。
func (s *userManagementService) rollbackNewUser(ctx context.Context, userID string) {
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if remover, ok := s.userService.(interfaces.NewlyCreatedUserRemover); ok {
		_ = remover.RemoveNewlyCreatedUser(rbCtx, userID)
		return
	}
	// 拿不到窄接口（测试替身）时退回软删，保持既有行为。
	_ = s.userService.DeleteUser(rbCtx, userID)
}
