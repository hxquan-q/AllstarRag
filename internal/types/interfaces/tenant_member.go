package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantMemberRepository persists (user, tenant) membership rows that
// carry the per-tenant TenantRole.
//
// All methods operate on active rows only (deleted_at IS NULL) unless the
// docstring explicitly says otherwise. Soft deletion is handled by GORM
// via the DeletedAt field on TenantMember.
type TenantMemberRepository interface {
	// Create inserts a new active membership row. Caller is responsible
	// for ensuring no other active row exists for the same (user, tenant)
	// pair; the partial unique index will return a conflict error otherwise.
	Create(ctx context.Context, member *types.TenantMember) error

	// Get returns the active membership for the given (user, tenant) pair,
	// or (nil, nil) if no such row exists.
	Get(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)

	// ListByUser returns every active membership owned by the given user,
	// ordered by joined_at ascending.
	ListByUser(ctx context.Context, userID string) ([]*types.TenantMember, error)

	// ListByTenant returns every active membership inside the given tenant,
	// ordered by joined_at ascending.
	ListByTenant(ctx context.Context, tenantID uint64) ([]*types.TenantMember, error)

	// CountFilteredByTenant counts active memberships in tenant. Optional
	// search matches user email/username (join users table); empty search
	// counts all memberships.
	CountFilteredByTenant(ctx context.Context, tenantID uint64, search string) (int64, error)

	// ListPagedByTenant returns active memberships sorted joined_at ASC, id ASC.
	// search filters by user email/username (join users table); empty
	// search lists all memberships in tenant.
	ListPagedByTenant(ctx context.Context, tenantID uint64, search string, offset, limit int) ([]*types.TenantMember, error)

	// UpdateRole changes the role of an existing active membership. Returns
	// gorm.ErrRecordNotFound if no active row matches.
	UpdateRole(ctx context.Context, userID string, tenantID uint64, role types.TenantRole) error

	// TransferOwnershipTx 在单个事务内完成空间交接：(to) 升为 owner、
	// (from) 降为 admin。事务内按 user_id 升序锁定两条成员行并复核
	// 角色与状态，任一步失败整体回滚，不做跨事务补偿（T40）。
	TransferOwnershipTx(ctx context.Context, tenantID uint64, fromUserID, toUserID string) error

	// SoftDelete marks the active membership as deleted. The user record
	// itself is untouched.
	SoftDelete(ctx context.Context, userID string, tenantID uint64) error

	// CountActiveOwners reports how many active rows in the tenant carry
	// the owner role. Used by service-layer invariant checks ("cannot
	// remove the last owner").
	CountActiveOwners(ctx context.Context, tenantID uint64) (int64, error)

	// HasAnyMembers reports whether the tenant has at least one active
	// membership. Used by the auth middleware to decide whether to
	// auto-promote the first authenticating human in an API-key-only tenant.
	HasAnyMembers(ctx context.Context, tenantID uint64) (bool, error)

	// CountDeletedByUser 统计该用户所属空间仍存在(未软删)的软删成员行。
	// 普通 ListByUser 会被 GORM 软删过滤隐藏这些行,所以这里必须 Unscoped;
	// 与「活跃成员为空」一起用于判定用户是否被移出了所有空间
	// (access revoked)。删除空间导致的级联软删不计入。
	CountDeletedByUser(ctx context.Context, userID string) (int64, error)

	// DemoteOwnerAtomically demotes an Owner to a non-Owner role inside
	// a transaction that holds an UPDATE lock on the tenant's other
	// active Owners, fixing the TOCTOU race where two concurrent
	// demotions could leave the tenant ownerless. Returns the
	// repo-level ErrLastOwner sentinel when no other Owner exists.
	DemoteOwnerAtomically(ctx context.Context, userID string, tenantID uint64, newRole types.TenantRole) error

	// RemoveOwnerAtomically soft-deletes an Owner row under the same
	// lock as DemoteOwnerAtomically.
	RemoveOwnerAtomically(ctx context.Context, userID string, tenantID uint64) error
}
