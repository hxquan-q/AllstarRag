package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLastOwner is returned by the atomic demote / remove repo helpers
// when the operation would leave the tenant without an active Owner.
// The service layer maps this to its own ErrLastOwner sentinel (same
// semantic; just kept separate so the repo doesn't import service).
var ErrLastOwner = errors.New("repository: last active owner")

// ErrRoleChanged 由 UpdateRoleGuarded 返回：成员行当前的 role 已与调用方
// 预读的 expectedRole 不一致。服务层据此拒绝“先查角色、再更新”的过期序列
// （T32）。
var ErrRoleChanged = errors.New("repository: membership role changed")

// ErrOwnerDemotionRequiresTransfer 由 UpdateRoleGuarded 返回：试图把 owner
// 直接降为非 owner，但没有走 transfer-owner 交接路径（T32 / AC-13 / AC-20）。
var ErrOwnerDemotionRequiresTransfer = errors.New("repository: owner demotion requires transfer")

// ErrTargetBanned 由 TransferOwnershipTx 返回：交接目标账号已被封禁
// （users.is_active=false）。服务层据此返回 400，而不是把错误当成
// “角色已变化”的 409（T40）。
var ErrTargetBanned = errors.New("repository: transfer target is banned")

// forUpdateClause returns the gorm SELECT ... FOR UPDATE clause. Kept
// in one place so we can swap it out for `clause.Locking{Strength: "UPDATE"}`
// on databases that don't support row-level locking (none in our matrix,
// but keeps the seam if SQLite-lite ever needs a no-op).
func forUpdateClause() clause.Expression {
	return clause.Locking{Strength: "UPDATE"}
}

// tenantMemberRepository implements interfaces.TenantMemberRepository.
type tenantMemberRepository struct {
	db *gorm.DB
}

// NewTenantMemberRepository creates a new tenant member repository.
func NewTenantMemberRepository(db *gorm.DB) interfaces.TenantMemberRepository {
	return &tenantMemberRepository{db: db}
}

// Create inserts a new active membership row. Status defaults to
// TenantMemberStatusActive when the caller leaves it blank, and JoinedAt
// defaults to the current time, matching service-layer expectations.
func (r *tenantMemberRepository) Create(ctx context.Context, member *types.TenantMember) error {
	if member.Status == "" {
		member.Status = types.TenantMemberStatusActive
	}
	if member.JoinedAt.IsZero() {
		member.JoinedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(member).Error
}

// Get returns the active membership for (userID, tenantID), or (nil, nil)
// if no such row exists. Errors are propagated unchanged for any other case.
func (r *tenantMemberRepository) Get(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	var member types.TenantMember
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		First(&member).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &member, nil
}

// ListByUser returns every active membership owned by the user, ordered
// by joined_at ascending so the home tenant (created at registration)
// naturally appears first.
func (r *tenantMemberRepository) ListByUser(ctx context.Context, userID string) ([]*types.TenantMember, error) {
	var members []*types.TenantMember
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("joined_at ASC, id ASC").
		Find(&members).Error
	if err != nil {
		return nil, err
	}
	return members, nil
}

// ListByTenant returns every active membership inside the tenant.
func (r *tenantMemberRepository) ListByTenant(ctx context.Context, tenantID uint64) ([]*types.TenantMember, error) {
	var members []*types.TenantMember
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("joined_at ASC, id ASC").
		Find(&members).Error
	if err != nil {
		return nil, err
	}
	return members, nil
}

// CountFilteredByTenant counts active tenant membership rows, optionally
// restricted to users whose email or username matches search.
func (r *tenantMemberRepository) CountFilteredByTenant(
	ctx context.Context, tenantID uint64, search string,
) (int64, error) {
	search = strings.TrimSpace(search)
	q := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Where("tenant_members.tenant_id = ?", tenantID)
	var total int64
	var err error
	if search == "" {
		err = q.Count(&total).Error
	} else {
		like := "%" + escapeLikePattern(search) + "%"
		err = q.
			Joins(`INNER JOIN users ON users.id = tenant_members.user_id AND users.deleted_at IS NULL`).
			Where(
				`(LOWER(users.email) LIKE LOWER(?) ESCAPE ? OR LOWER(users.username) LIKE LOWER(?) ESCAPE ?)`,
				like, likeEscapeChar, like, likeEscapeChar).
			Count(&total).Error
	}
	return total, err
}

// ListPagedByTenant lists active memberships with stable sort.
func (r *tenantMemberRepository) ListPagedByTenant(
	ctx context.Context, tenantID uint64, search string, offset, limit int,
) ([]*types.TenantMember, error) {
	search = strings.TrimSpace(search)
	var members []*types.TenantMember
	q := r.db.WithContext(ctx).Model(&types.TenantMember{}).
		Where("tenant_members.tenant_id = ?", tenantID).
		Order("tenant_members.joined_at ASC, tenant_members.id ASC").
		Offset(offset).
		Limit(limit)

	var err error
	if search == "" {
		err = q.Find(&members).Error
	} else {
		like := "%" + escapeLikePattern(search) + "%"
		err = q.
			Joins(`INNER JOIN users ON users.id = tenant_members.user_id AND users.deleted_at IS NULL`).
			Where(
				`(LOWER(users.email) LIKE LOWER(?) ESCAPE ? OR LOWER(users.username) LIKE LOWER(?) ESCAPE ?)`,
				like, likeEscapeChar, like, likeEscapeChar).
			Find(&members).Error
	}
	if err != nil {
		return nil, err
	}
	return members, nil
}

// UpdateRole changes the role of an existing active membership.
func (r *tenantMemberRepository) UpdateRole(ctx context.Context, userID string, tenantID uint64, role types.TenantRole) error {
	res := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		Updates(map[string]any{
			"role":       role,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateRoleGuarded 在单事务内做带比较交换（CAS）的角色变更（T32）：
// 先锁目标成员行，复核当前 role 仍等于 expectedRole，才执行更新。
// 直接把 owner 降为非 owner 需要 allowOwnerDemotion=true，该参数只给
// transfer-owner 交接流程使用；“最后一个 owner”约束也在同一把锁内复核。
func (r *tenantMemberRepository) UpdateRoleGuarded(
	ctx context.Context,
	userID string,
	tenantID uint64,
	expectedRole, newRole types.TenantRole,
	allowOwnerDemotion bool,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.TenantMember
		err := tx.Clauses(forUpdateClause()).
			Where("user_id = ? AND tenant_id = ? AND status = ?",
				userID, tenantID, types.TenantMemberStatusActive).
			First(&current).Error
		if err != nil {
			return err
		}
		if current.Role != expectedRole {
			return ErrRoleChanged
		}
		if current.Role == types.TenantRoleOwner && newRole != types.TenantRoleOwner {
			if !allowOwnerDemotion {
				return ErrOwnerDemotionRequiresTransfer
			}
			var otherOwners []types.TenantMember
			err := tx.Clauses(forUpdateClause()).
				Where("tenant_id = ? AND user_id <> ? AND role = ? AND status = ?",
					tenantID, userID, types.TenantRoleOwner, types.TenantMemberStatusActive).
				Find(&otherOwners).Error
			if err != nil {
				return err
			}
			if len(otherOwners) == 0 {
				return ErrLastOwner
			}
		}
		res := tx.Model(&types.TenantMember{}).
			Where("user_id = ? AND tenant_id = ? AND role = ?", userID, tenantID, expectedRole).
			Updates(map[string]any{
				"role":       newRole,
				"updated_at": time.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrRoleChanged
		}
		return nil
	})
}

// TransferOwnershipTx 在单个事务内完成空间 owner 交接（T40）。
//
// 锁序：先把 from/to 两个 user_id 排序，再按升序逐行 SELECT ... FOR UPDATE。
// 并发交接因此按相同顺序加锁，避免旧实现“先锁源行、再锁其他 owner”的
// 交叉等待死锁（A→C 与 B→D）。SQLite 没有行锁，先对两条成员行做一次
// 空更新拿到库级写锁，让并发交接串行化，避免读后升级写时的 SQLITE_BUSY。
//
// 事务内复核（不依赖服务层预检）：from 必须是 active owner；to 必须是
// active 成员；to 用户必须存在且 is_active=true。任一不满足即返回 error
// 且零改动。随后先把 to 提升为 owner，再把 from 降为 admin；任一 UPDATE
// 未命中返回 ErrRoleChanged，整单回滚，不做跨事务补偿。
func (r *tenantMemberRepository) TransferOwnershipTx(
	ctx context.Context,
	tenantID uint64,
	fromUserID, toUserID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockIDs := []string{fromUserID, toUserID}
		sort.Strings(lockIDs)
		if tx.Name() == "sqlite" {
			if err := tx.Model(&types.TenantMember{}).
				Where("tenant_id = ? AND user_id IN ?", tenantID, lockIDs).
				UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}

		locked := make(map[string]*types.TenantMember, len(lockIDs))
		for _, userID := range lockIDs {
			var member types.TenantMember
			err := tx.Clauses(forUpdateClause()).
				Where("tenant_id = ? AND user_id = ?", tenantID, userID).
				First(&member).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			locked[userID] = &member
		}

		from := locked[fromUserID]
		if from == nil || from.Status != types.TenantMemberStatusActive ||
			from.Role != types.TenantRoleOwner {
			return ErrRoleChanged
		}
		to := locked[toUserID]
		if to == nil || to.Status != types.TenantMemberStatusActive {
			return ErrRoleChanged
		}

		var targetUser types.User
		if err := lockUserRows(tx).
			Where("id = ?", toUserID).
			First(&targetUser).Error; err != nil {
			return err
		}
		if !targetUser.IsActive {
			return ErrTargetBanned
		}

		now := time.Now()
		res := tx.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ? AND status = ?",
				tenantID, toUserID, types.TenantMemberStatusActive).
			Updates(map[string]any{
				"role":       types.TenantRoleOwner,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrRoleChanged
		}

		res = tx.Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ? AND status = ? AND role = ?",
				tenantID, fromUserID, types.TenantMemberStatusActive, types.TenantRoleOwner).
			Updates(map[string]any{
				"role":       types.TenantRoleAdmin,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrRoleChanged
		}
		return nil
	})
}

// SoftDelete marks the membership row as deleted. GORM's soft-delete
// support populates DeletedAt automatically.
func (r *tenantMemberRepository) SoftDelete(ctx context.Context, userID string, tenantID uint64) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND tenant_id = ?", userID, tenantID).
		Delete(&types.TenantMember{}).Error
}

// CountActiveOwners reports the number of active owner rows in the tenant.
func (r *tenantMemberRepository) CountActiveOwners(ctx context.Context, tenantID uint64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Where("tenant_id = ? AND role = ? AND status = ?",
			tenantID, types.TenantRoleOwner, types.TenantMemberStatusActive).
		Count(&count).Error
	return count, err
}

// DemoteOwnerAtomically transitions an Owner row to a non-Owner role
// while holding an UPDATE lock on the tenant's other Owner rows. This
// closes the TOCTOU window in the old "Get → CountActiveOwners → Update"
// sequence where two concurrent demotions of two different Owners could
// each read count=2, then both commit, leaving the tenant ownerless.
//
// Returns:
//   - ErrLastOwner when there is no other active Owner.
//   - gorm.ErrRecordNotFound when the row isn't there anymore (race
//     between concurrent removes); callers map this to ErrMembershipNotFound.
//   - any other error verbatim for the caller to log / surface.
//
// The caller is responsible for verifying the *current* role is Owner
// before invoking this; the method is purposely narrow (it only handles
// the dangerous demotion path) so other UpdateRole transitions can keep
// using the cheap single-statement UpdateRole above.
func (r *tenantMemberRepository) DemoteOwnerAtomically(
	ctx context.Context,
	userID string,
	tenantID uint64,
	newRole types.TenantRole,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock every other active Owner row in the tenant. Locking ONLY
		// owners (not the row being demoted) is enough: a concurrent
		// demote of the locked row will block on this same SELECT.
		var locked []types.TenantMember
		err := tx.
			Clauses(forUpdateClause()).
			Where("tenant_id = ? AND user_id <> ? AND role = ? AND status = ?",
				tenantID, userID, types.TenantRoleOwner, types.TenantMemberStatusActive).
			Find(&locked).Error
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return ErrLastOwner
		}
		res := tx.
			Model(&types.TenantMember{}).
			Where("user_id = ? AND tenant_id = ?", userID, tenantID).
			Updates(map[string]any{
				"role":       newRole,
				"updated_at": time.Now(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// RemoveOwnerAtomically soft-deletes an Owner row under the same lock
// as DemoteOwnerAtomically. Same return semantics.
func (r *tenantMemberRepository) RemoveOwnerAtomically(
	ctx context.Context,
	userID string,
	tenantID uint64,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked []types.TenantMember
		err := tx.
			Clauses(forUpdateClause()).
			Where("tenant_id = ? AND user_id <> ? AND role = ? AND status = ?",
				tenantID, userID, types.TenantRoleOwner, types.TenantMemberStatusActive).
			Find(&locked).Error
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return ErrLastOwner
		}
		res := tx.
			Where("user_id = ? AND tenant_id = ?", userID, tenantID).
			Delete(&types.TenantMember{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// CountDeletedByUser 统计该用户「所属空间仍然存在」的软删成员行。
// 必须 Unscoped:普通查询会被 GORM 软删过滤掉这些行。还要 JOIN tenants
// 并排除已软删的空间——删除空间会级联软删全部成员,那些行不代表用户被
// 管理员移出,不能据此判定 access_revoked。
func (r *tenantMemberRepository) CountDeletedByUser(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Unscoped().
		Model(&types.TenantMember{}).
		Joins("JOIN tenants ON tenants.id = tenant_members.tenant_id").
		Where("tenant_members.user_id = ? AND tenant_members.deleted_at IS NOT NULL "+
			"AND tenants.deleted_at IS NULL", userID).
		Count(&count).Error
	return count, err
}

// HasAnyMembers reports whether the tenant has at least one active
// membership row. Uses a LIMIT 1 SELECT (instead of COUNT(*)) so the query
// short-circuits after the first match — important because this is on the
// auth middleware's hot path for users without a cached membership.
func (r *tenantMemberRepository) HasAnyMembers(ctx context.Context, tenantID uint64) (bool, error) {
	var probe struct {
		ID uint64
	}
	err := r.db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Select("id").
		Where("tenant_id = ? AND status = ?", tenantID, types.TenantMemberStatusActive).
		Limit(1).
		Take(&probe).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
