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
	"github.com/Tencent/WeKnora/internal/logger"
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

// YunzhijiaProvisioner 负责“员工目录行 → 本地 viewer 账号”的开号与修复。
//
// 它只创建 users 行与目标空间的 viewer 成员关系，不建租户、不调
// Register/ProvisionOwnerSpace、不写密码。登录与后续同步共用。
type YunzhijiaProvisioner struct {
	userRepo      interfaces.UserRepository
	memberService interfaces.TenantMemberService
	employeeRepo  interfaces.YunzhijiaEmployeeRepository
	remover       interfaces.NewlyCreatedUserRemover
	// mu 串行化本进程内的开号。open_id 唯一索引仍负责跨实例冲突回读。
	mu sync.Mutex
}

// NewYunzhijiaProvisioner 构造开号服务。cfg 参数为红阶段锁定的
// 构造签名保留位，开号逻辑不读取它。
func NewYunzhijiaProvisioner(
	cfg *config.YunzhijiaSSOConfig,
	userRepo interfaces.UserRepository,
	memberService interfaces.TenantMemberService,
	employeeRepo interfaces.YunzhijiaEmployeeRepository,
	remover interfaces.NewlyCreatedUserRemover,
) *YunzhijiaProvisioner {
	return &YunzhijiaProvisioner{
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
	// 避免重复触发唯一冲突。仓储查不到时返回 ErrUserNotFound。
	existing, err := p.userRepo.GetUserByEmail(ctx, email)
	if err != nil && !isUserLookupNotFound(err) {
		return nil, err
	}
	if existing != nil {
		return p.reuseExistingAccount(ctx, existing, employee)
	}

	isEmployee := true
	user := &types.User{
		ID:                  uuid.New().String(),
		Username:            yunzhijiaUsername(employee.Name, employee.OpenID, 1),
		Email:               email,
		PasswordHash:        "",
		TenantID:            employee.TenantID,
		IsActive:            true,
		IsSystemAdmin:       false,
		CanAccessAllTenants: false,
		// 员工标记只由服务端写入：前端据此隐藏改密入口（AC-7），
		// 密码守卫与 purge 钩子走员工目录，不依赖它。
		Preferences: types.UserPreferences{YunzhijiaEmployee: &isEmployee},
	}
	if err := p.createWithUniqueUsername(ctx, user, employee); err != nil {
		// 并发 race 的另一种表现：创建失败但邮箱行已存在。
		if raceUser, readErr := p.userRepo.GetUserByEmail(ctx, email); readErr == nil {
			return p.reuseExistingAccount(ctx, raceUser, employee)
		}
		return nil, err
	}

	if err := p.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		p.rollbackNewUser(ctx, user.ID)
		return nil, err
	}
	// SetUserID 失败时保留账号与成员关系：AddMember 已成功，回滚用户会
	// 留下指向不存在用户的成员行；下次登录按邮箱回读并重试回填。
	if err := p.employeeRepo.SetUserID(ctx, employee.OpenID, user.ID); err != nil {
		return nil, err
	}
	return user, nil
}

// reuseExistingAccount 复用按合成邮箱命中的账号：停用时原样返回（不写
// 成员关系，由登录侧拒绝），否则补齐成员关系并回填 user_id。
//
// 安全闸：只有从未设置过密码的账号才允许收编。合成邮箱可被猜到，注册
// 模式开放时有人可能抢先用它注册；把有密码的账号拉进员工空间会绕过
// AC-15「不按邮箱匹配已有密码账号」。命中时按开号失败返回。
func (p *YunzhijiaProvisioner) reuseExistingAccount(
	ctx context.Context, user *types.User, employee *types.YunzhijiaEmployee,
) (*types.User, error) {
	if user.PasswordHash != "" {
		return nil, fmt.Errorf("yunzhijia: 合成邮箱已被有密码账号占用，拒绝收编")
	}
	if !user.IsActive {
		return user, nil
	}
	if err := p.ensureViewerMembership(ctx, user.ID, employee.TenantID); err != nil {
		return nil, err
	}
	if err := p.employeeRepo.SetUserID(ctx, employee.OpenID, user.ID); err != nil {
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

// rollbackNewUser 删除刚创建、尚未产生成员关系的用户行，避免占用
// 邮箱/用户名唯一索引。回滚失败只记日志，不覆盖原始入组错误。
func (p *YunzhijiaProvisioner) rollbackNewUser(ctx context.Context, userID string) {
	if err := p.remover.RemoveNewlyCreatedUser(ctx, userID); err != nil {
		logger.Warnf(ctx, "[Yunzhijia] 新建用户回滚失败: %v", err)
	}
}

// NewYunzhijiaUserRemover 返回基于 users 仓储的工作实现。
func NewYunzhijiaUserRemover(
	userRepo interfaces.UserRepository,
) interfaces.NewlyCreatedUserRemover {
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
	runes := []rune(base)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + suffix
}

// isMembershipConflict 识别“成员关系已存在”的并发冲突。
func isMembershipConflict(err error) bool {
	return errors.Is(err, ErrMembershipAlreadyExists)
}
