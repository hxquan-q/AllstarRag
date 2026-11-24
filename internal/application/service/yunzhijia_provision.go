package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// yunzhijiaEmailDomain 是员工账号的合成邮箱后缀；邮箱本身只用于
	// users.email 唯一约束，不接收任何邮件。
	yunzhijiaEmailDomain = "yunzhijia.local"
	// yunzhijiaUsernameMaxRunes 对齐 users.username varchar(100)。
	yunzhijiaUsernameMaxRunes = 100
	// yunzhijiaUsernameSuffixRunes 是唯一化后缀取 open_id 的字符数。
	yunzhijiaUsernameSuffixRunes = 6
	// yunzhijiaCreateAttempts 是用户名唯一冲突时的最大尝试次数。
	yunzhijiaCreateAttempts = 3
)

// NewlyCreatedUserRemover 回滚开号过程中刚创建的本地用户。
//
// 真实实现通过 users 仓储的可选 HardDeleteUser 能力物理删除；
// 测试替身直接移除内存行。不可用时返回错误，由调用方记录。
type NewlyCreatedUserRemover interface {
	RemoveNewlyCreatedUser(ctx context.Context, userID string) error
}

// YunzhijiaProvisioner 负责“员工目录行 → 本地 viewer 账号”的开号与修复。
//
// 它只创建 users 行与目标空间的 viewer 成员关系，不建租户、不调
// Register/ProvisionOwnerSpace、不写密码。登录与后续同步共用。
type YunzhijiaProvisioner struct {
	cfg           *config.YunzhijiaSSOConfig
	userRepo      interfaces.UserRepository
	memberService interfaces.TenantMemberService
	employeeRepo  interfaces.YunzhijiaEmployeeRepository
	remover       NewlyCreatedUserRemover
	// mu 串行化本进程内的开号。open_id 唯一索引仍负责跨实例冲突回读。
	mu sync.Mutex
}

// NewYunzhijiaProvisioner 构造开号服务。
func NewYunzhijiaProvisioner(
	cfg *config.YunzhijiaSSOConfig,
	userRepo interfaces.UserRepository,
	memberService interfaces.TenantMemberService,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
	remover NewlyCreatedUserRemover,
) *YunzhijiaProvisioner {
	return &YunzhijiaProvisioner{
		cfg:           cfg,
		userRepo:      userRepo,
		memberService: memberService,
		employeeRepo:  employeeRepo,
		remover:       remover,
	}
}

// EnsureAccount 返回员工对应的可用本地账号，必要时创建。
//
//   - 已有 user_id：加载用户并补齐目标空间 viewer 成员关系；不改角色/密码。
//     用户不存在或已停用时直接返回（不写成员关系），由调用方拒绝登录。
//   - 无 user_id：创建 users 行 + viewer 成员关系 + 回填 user_id；
//     入组失败时回滚刚建的用户行。
//
// blocked / disabled_by_sync 的员工不进入开号。
func (p *YunzhijiaProvisioner) EnsureAccount(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (*types.User, error) {
	if p == nil || employee == nil {
		return nil, errors.New("yunzhijia: 员工记录为空")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if employee.Blocked || employee.DisabledBySync {
		return nil, fmt.Errorf("yunzhijia: 员工 %s 不可开号", employee.OpenID)
	}
	if employee.UserID != nil {
		return p.ensureExistingAccount(ctx, employee)
	}
	return p.ensureNewAccount(ctx, employee)
}

// ensureExistingAccount 复用已有账号，只在账号可用时补成员关系。
func (p *YunzhijiaProvisioner) ensureExistingAccount(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (*types.User, error) {
	user, err := p.userRepo.GetUserByID(ctx, *employee.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("yunzhijia: 员工 %s 关联的用户不存在", employee.OpenID)
	}
	// 账号已停用时不得做任何修复写入，由登录侧 fail-closed 拒绝。
	if !user.IsActive {
		return user, nil
	}
	if err := p.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		return nil, err
	}
	return user, nil
}

// ensureNewAccount 创建新账号、入组并回填 user_id。
func (p *YunzhijiaProvisioner) ensureNewAccount(
	ctx context.Context, employee *types.YunzhijiaEmployee,
) (*types.User, error) {
	email := fmt.Sprintf("yzj_%s@%s", employee.OpenID, yunzhijiaEmailDomain)

	// 先按合成邮箱回读：同一 open_id 的并发调用已在别处建号时直接复用，
	// 避免重复触发唯一冲突。
	existing, err := p.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := p.ensureViewerMembership(ctx, existing.ID, employee.TenantID); err != nil {
			return nil, err
		}
		if err := p.employeeRepo.SetUserID(ctx, employee.OpenID, existing.ID); err != nil {
			return nil, err
		}
		return existing, nil
	}

	user := &types.User{
		ID:                  uuid.New().String(),
		Username:            yunzhijiaUsername(employee.Name, employee.OpenID, 1),
		Email:               email,
		PasswordHash:        "",
		TenantID:            employee.TenantID,
		IsActive:            true,
		IsSystemAdmin:       false,
		CanAccessAllTenants: false,
	}
	if err := p.createWithUniqueUsername(ctx, user, employee); err != nil {
		// 并发 race 的另一种表现：创建失败但邮箱行已存在。
		if raceUser, readErr := p.userRepo.GetUserByEmail(ctx, email); readErr == nil && raceUser != nil {
			if mErr := p.ensureViewerMembership(ctx, raceUser.ID, employee.TenantID); mErr != nil {
				return nil, mErr
			}
			if sErr := p.employeeRepo.SetUserID(ctx, employee.OpenID, raceUser.ID); sErr != nil {
				return nil, sErr
			}
			return raceUser, nil
		}
		return nil, err
	}

	if err := p.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		p.rollbackNewUser(ctx, user.ID)
		return nil, err
	}
	if err := p.employeeRepo.SetUserID(ctx, employee.OpenID, user.ID); err != nil {
		p.rollbackNewUser(ctx, user.ID)
		return nil, err
	}
	return user, nil
}

// createWithUniqueUsername 插入用户；用户名冲突时追加 open_id 短后缀重试。
func (p *YunzhijiaProvisioner) createWithUniqueUsername(
	ctx context.Context, user *types.User, employee *types.YunzhijiaEmployee,
) error {
	var lastErr error
	for attempt := 1; attempt <= yunzhijiaCreateAttempts; attempt++ {
		user.Username = yunzhijiaUsername(employee.Name, employee.OpenID, attempt)
		lastErr = p.userRepo.CreateUser(ctx, user)
		if lastErr == nil {
			return nil
		}
		if !isUniqueViolation(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// ensureViewerMembership 确保用户是目标空间的 viewer；已有成员关系时
// 不改角色。并发插入冲突视为成功（另一路已完成入组）。
func (p *YunzhijiaProvisioner) ensureViewerMembership(
	ctx context.Context, userID string, tenantID uint64,
) error {
	member, err := p.memberService.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return err
	}
	if member != nil {
		return nil
	}
	_, err = p.memberService.AddMember(ctx, userID, tenantID, types.TenantRoleViewer, nil)
	if err == nil {
		return nil
	}
	if isMembershipConflict(err) {
		return nil
	}
	return err
}

// rollbackNewUser 删除刚创建的用户行，避免占用邮箱/用户名唯一索引。
func (p *YunzhijiaProvisioner) rollbackNewUser(ctx context.Context, userID string) {
	if p.remover == nil {
		return
	}
	// 回滚失败不回传给登录：原始入组/回填错误更能说明问题。
	_ = p.remover.RemoveNewlyCreatedUser(ctx, userID)
}

// NewYunzhijiaUserRemover 返回基于 users 仓储的工作实现。
func NewYunzhijiaUserRemover(userRepo interfaces.UserRepository) NewlyCreatedUserRemover {
	return &yunzhijiaUserRemover{userRepo: userRepo}
}

type yunzhijiaUserRemover struct {
	userRepo interfaces.UserRepository
}

// RemoveNewlyCreatedUser 物理删除刚建的用户行。仓储刻意没把 HardDeleteUser
// 放进宽接口，这里按可选能力探测。
func (r *yunzhijiaUserRemover) RemoveNewlyCreatedUser(ctx context.Context, userID string) error {
	hardDeleter, ok := r.userRepo.(interface {
		HardDeleteUser(ctx context.Context, id string) error
	})
	if !ok {
		return errors.New("yunzhijia: 用户仓储不支持物理删除，无法回滚")
	}
	return hardDeleter.HardDeleteUser(ctx, userID)
}

// yunzhijiaUsername 生成员工用户名：姓名；冲突时追加 open_id 前 6 位，
// 仍冲突时追加尝试序号；整体不超过 users.username 列宽。
func yunzhijiaUsername(name, openID string, attempt int) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "云之家员工"
	}
	if attempt <= 1 {
		return clampYunzhijiaUsername(base, "")
	}
	suffix := openID
	if len(suffix) > yunzhijiaUsernameSuffixRunes {
		suffix = suffix[:yunzhijiaUsernameSuffixRunes]
	}
	if attempt > 2 {
		return clampYunzhijiaUsername(base, fmt.Sprintf("-%s-%d", suffix, attempt))
	}
	return clampYunzhijiaUsername(base, "-"+suffix)
}

// clampYunzhijiaUsername 把姓名截到能容纳后缀的长度。
func clampYunzhijiaUsername(base, suffix string) string {
	limit := yunzhijiaUsernameMaxRunes - utf8.RuneCountInString(suffix)
	if limit < 1 {
		limit = 1
	}
	runes := []rune(base)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + suffix
}

// isMembershipConflict 识别“成员关系已存在”的并发冲突。
func isMembershipConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrMembershipAlreadyExists) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
