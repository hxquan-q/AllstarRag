package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 登录业务拒绝文案。均为中文、不含 ticket，也不区分账号是否存在。
const (
	yunzhijiaMsgInvalidTicket  = "登录凭证已失效，请从云之家重新打开"
	yunzhijiaMsgUpstream       = "云之家服务暂时不可用，请稍后再试"
	yunzhijiaMsgProvisioning   = "账号开通中，请明天再试"
	yunzhijiaMsgDuplicateName  = "在职员工存在同名，请联系管理员"
	yunzhijiaMsgAccountBlocked = "账号不可用，请联系管理员"
	yunzhijiaMsgConfig         = "系统暂不可用，请联系管理员"
)

// YunzhijiaLoginService 用云之家 ticket 换取员工姓名并完成免登录。
type YunzhijiaLoginService struct {
	cfg           *config.YunzhijiaSSOConfig
	client        yunzhijia.Client
	employeeRepo  interfaces.YunzhijiaEmployeeRepository
	provisioner   *YunzhijiaProvisioner
	userService   interfaces.UserService
	tenantService interfaces.TenantService
}

// NewYunzhijiaLoginService 构造登录服务。
func NewYunzhijiaLoginService(
	cfg *config.YunzhijiaSSOConfig,
	client yunzhijia.Client,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
	provisioner *YunzhijiaProvisioner,
	userService interfaces.UserService,
	tenantService interfaces.TenantService,
) *YunzhijiaLoginService {
	return &YunzhijiaLoginService{
		cfg:           cfg,
		client:        client,
		employeeRepo:  employeeRepo,
		provisioner:   provisioner,
		userService:   userService,
		tenantService: tenantService,
	}
}

// Login 校验配置 → 换名 → 姓名匹配 → 状态校验 → 开号/复用 → 签发 token。
//
// 业务拒绝返回 Success=false 的 LoginResponse 且 error=nil；状态校验
// （blocked / disabled_by_sync / 关联账号停用）全部发生在任何开号或
// 成员关系写入之前。
func (s *YunzhijiaLoginService) Login(
	ctx context.Context, ticket string,
) (*types.LoginResponse, error) {
	if s.cfg == nil || !s.cfg.Enabled {
		return yunzhijiaRejected(yunzhijiaMsgConfig), nil
	}
	if s.client == nil {
		return yunzhijiaRejected(yunzhijiaMsgConfig), nil
	}
	if err := s.cfg.Validate(); err != nil {
		logger.Warnf(ctx, "[Yunzhijia] 登录配置非法: %v", err)
		return yunzhijiaRejected(yunzhijiaMsgConfig), nil
	}

	name, err := s.client.QueryUserName(ctx, ticket)
	if err != nil {
		if errors.Is(err, yunzhijia.ErrInvalidTicket) {
			return yunzhijiaRejected(yunzhijiaMsgInvalidTicket), nil
		}
		return yunzhijiaRejected(yunzhijiaMsgUpstream), nil
	}
	employees, err := s.employeeRepo.FindActiveByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(employees) == 0 {
		return yunzhijiaRejected(yunzhijiaMsgProvisioning), nil
	}
	if len(employees) > 1 {
		return yunzhijiaRejected(yunzhijiaMsgDuplicateName), nil
	}
	employee := employees[0]
	if employee.Blocked || employee.DisabledBySync {
		return yunzhijiaRejected(yunzhijiaMsgAccountBlocked), nil
	}

	// 目标空间必须在写任何账号数据之前确认存在，避免把员工开进错误空间。
	activeTenant, err := s.tenantService.GetTenantByID(ctx, employee.TenantID)
	if err != nil {
		return nil, err
	}

	// 已有账号先校验状态，停用/封禁的账号不得触发成员关系修复。
	if employee.UserID != nil {
		user, userErr := s.userService.GetUserByID(ctx, *employee.UserID)
		if userErr != nil {
			return nil, userErr
		}
		if !user.IsActive {
			return yunzhijiaRejected(yunzhijiaMsgAccountBlocked), nil
		}
	}

	user, err := s.provisioner.EnsureAccount(ctx, employee)
	if err != nil {
		return nil, err
	}
	if !user.IsActive {
		return yunzhijiaRejected(yunzhijiaMsgAccountBlocked), nil
	}

	accessToken, refreshToken, err := s.userService.GenerateTokens(ctx, user)
	if err != nil {
		return nil, err
	}
	memberships := s.userService.BuildLoginMemberships(ctx, user, activeTenant)

	return &types.LoginResponse{
		Success:      true,
		User:         user,
		ActiveTenant: activeTenant,
		Memberships:  memberships,
		Token:        accessToken,
		RefreshToken: refreshToken,
		Capabilities: &types.LoginCapabilities{
			CanCreateTenant: false,
			AccessRevoked:   false,
		},
	}, nil
}

// yunzhijiaRejected 构造业务拒绝响应（Success=false，无 token）。
func yunzhijiaRejected(message string) *types.LoginResponse {
	return &types.LoginResponse{Success: false, Message: message}
}
