package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// UserManagementListScope 是用户管理列表的空间筛选口径：
//   - current：仅本空间（与旧 ListUsers 一致）；
//   - other：在本空间之外任一空间持有 active 成员关系的用户；
//   - all：全平台全部非墓碑用户。
type UserManagementListScope string

const (
	UserManagementScopeCurrent UserManagementListScope = "current"
	UserManagementScopeOther   UserManagementListScope = "other"
	UserManagementScopeAll     UserManagementListScope = "all"
)

// UserManagementMembership 是多空间视图（scope=other/all）中一条成员
// 关系的投影：用户在哪个空间、以什么角色存在。
type UserManagementMembership struct {
	TenantID   uint64           `json:"tenant_id"`
	TenantName string           `json:"tenant_name"`
	Role       types.TenantRole `json:"role"`
}

// UserManagementListItem 是用户管理列表的一行。只暴露前端渲染需要的
// 字段；密码哈希、token 等敏感字段不进入该 DTO（AC-32）。
// Memberships 只在多空间视图（scope=other/all）填充，按 tenant_id
// 升序；current 视图为空。
type UserManagementListItem struct {
	UserID      string                     `json:"user_id"`
	Email       string                     `json:"email"`
	Username    string                     `json:"username"`
	Role        types.TenantRole           `json:"role"`
	Status      string                     `json:"status"`
	IsActive    bool                       `json:"is_active"`
	CreatedAt   time.Time                  `json:"created_at"`
	Memberships []UserManagementMembership `json:"memberships,omitempty"`
}

// UserManagementListResult 是用户管理列表接口的返回体。RoleCounts 是整空间
// 目录计数，始终包含 viewer/contributor/admin/owner 四个键（无成员为 0），
// 不随 role 过滤缩小。
type UserManagementListResult struct {
	Items      []UserManagementListItem
	Total      int64
	RoleCounts map[types.TenantRole]int64
}

// OwnerInviteResult 是一次 owner 邀请创建结果。Token 是明文分享 token，
// handler 用它拼出注册链接；成员关系在接受邀请时才创建。
type OwnerInviteResult struct {
	TenantID     uint64
	InvitationID uint64
	Token        string
	ExpiresAt    time.Time
}

// CreateCoOwnerRequest 是 owner 新建 co-owner 账号的请求体。
// Password 为空时由服务层拒绝（400），不会生成随机密码。
type CreateCoOwnerRequest struct {
	Username string
	Email    string
	Password *string
}

// UserManagementService 是用户管理域（封禁 / 解封 / 墓碑删除 /
// owner 空间 / 空间交接）的服务契约。目标保护、权限矩阵与跨租户规则
// 都在服务层完成；handler 只负责把同一份 error 映射成 HTTP 状态码。
type UserManagementService interface {
	// BanUser 封禁目标账号：users.is_active=false + 吊销全部 token，
	// tenant_members 关系保留。重复调用幂等。
	BanUser(ctx context.Context, tenantID uint64, targetUserID, reason string) (*types.User, error)
	// UnbanUser 解封目标账号：users.is_active=true，不恢复历史 token。
	// 普通 owner 只能解封在最近一次在本租户被封禁的账号；系统管理员不受此限。
	UnbanUser(ctx context.Context, tenantID uint64, targetUserID, reason string) (*types.User, error)
	// PurgeUser 彻底删除（墓碑化）目标账号：清空 PII、匿名化邮箱/用户名、
	// 清理个人数据并写审计；资产保留，用户 id 继续供资产/审计引用。
	// confirmEmail 必须与目标当前邮箱一致，否则返回 400。
	PurgeUser(
		ctx context.Context, tenantID uint64, targetUserID, confirmEmail, reason string,
	) error

	// TransferOwner 把空间 owner 交给目标成员：先把目标提为 owner，
	// 再把源 owner 降为 admin。owner 只能交出自己的身份；系统管理员
	// 可交接任意 owner。目标必须是本空间 active 成员。
	TransferOwner(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error

	// ProvisionOwnerSpace 由系统管理员把已有账号设为 owner：若目标已有
	// active owner 空间则复用（幂等），否则创建独立空间并把目标设为唯一
	// owner。系统管理员本身不会成为该空间成员。
	ProvisionOwnerSpace(ctx context.Context, targetUserID, spaceName string) (*types.Tenant, error)

	// CreateOwnerInvite 由系统管理员创建 pending 空间 + role=owner 的
	// 分享链接。邀请时只建租户与 token，不预建成员关系。
	CreateOwnerInvite(
		ctx context.Context, email, spaceName string, expiresDays int,
	) (*OwnerInviteResult, error)

	// CreateCoOwnerAccount 由本空间 owner（或系统管理员）新建 co-owner
	// 账号：账号 tenant_id=0，仅通过成员关系属于当前空间。入组失败时
	// 回滚刚建的账号。
	CreateCoOwnerAccount(
		ctx context.Context, tenantID uint64, req *CreateCoOwnerRequest,
	) (*types.User, error)

	// ListUsers 返回用户管理页的成员列表：role 为空表示全部，非法值
	// 400；只含 active 成员关系且非墓碑用户，封禁用户出现在列表中并标记
	// status="banned"；RoleCounts 为整空间目录计数。
	ListUsers(
		ctx context.Context, tenantID uint64, role types.TenantRole,
		query string, page, pageSize int,
	) (*UserManagementListResult, error)

	// ListUsersScoped 返回带空间筛选的用户列表。scope=current 与
	// ListUsers 完全一致；scope=other/all 是系统管理员专属（非系统管理
	// 员 403）：other = 在本空间之外任一空间持有 active 成员关系的去重
	// 用户（memberships 只含其它空间），all = 全平台非墓碑用户（含无
	// 任何成员关系的账号，memberships 聚合全部 active 关系并按
	// tenant_id 升序）。role 过滤匹配视图内任意成员关系的角色，
	// RoleCounts 按视图内"有该角色的去重用户数"计。scope 非法值 400。
	ListUsersScoped(
		ctx context.Context, tenantID uint64, scope UserManagementListScope,
		role types.TenantRole, query string, page, pageSize int,
	) (*UserManagementListResult, error)
}
