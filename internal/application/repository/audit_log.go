package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// auditLogRepository persists audit events to the audit_logs table.
// Rows are append-only — there is no Update, no soft-delete, no
// Modify hook. The cursor pagination uses the monotonic id column,
// not created_at, so duplicate timestamps don't break iteration.
type auditLogRepository struct {
	db *gorm.DB
}

// NewAuditLogRepository constructs the production audit log repository
// backed by the shared GORM connection.
func NewAuditLogRepository(db *gorm.DB) interfaces.AuditLogRepository {
	return &auditLogRepository{db: db}
}

// Create inserts a single audit row. Validation is light — Action is
// the only required field at the schema level; CreatedAt is filled by
// the database default if zero. Service-layer Log() fills both before
// calling here so this is mostly a pass-through.
func (r *auditLogRepository) Create(ctx context.Context, entry *types.AuditLog) error {
	return r.db.WithContext(ctx).Create(entry).Error
}

// auditLogListLimitMax is the hard ceiling regardless of caller input.
// Keeps a misconfigured client from triggering an unbounded scan; the
// frontend's default page size is 50 so this is comfortable headroom.
const auditLogListLimitMax = 100

// escapeAuditLikePattern 转义 LIKE 通配符，使关键词中的 % 和 _ 按字面匹配。
func escapeAuditLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// List returns audit log rows for a tenant, newest-first, applying the
// optional cursor and filters from AuditLogQuery. Both Action and
// Outcome are exact-match — the filter set is small enough that we
// don't need substring matching yet.
func (r *auditLogRepository) List(
	ctx context.Context,
	tenantID uint64,
	q *interfaces.AuditLogQuery,
) ([]*types.AuditLog, error) {
	limit := 50
	if q != nil && q.Limit > 0 {
		limit = q.Limit
	}
	if limit > auditLogListLimitMax {
		limit = auditLogListLimitMax
	}

	tx := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID)
	if q != nil {
		if q.AfterID > 0 {
			tx = tx.Where("id < ?", q.AfterID)
		}
		if q.Action != "" {
			tx = tx.Where("action = ?", q.Action)
		}
		if q.Outcome != "" {
			tx = tx.Where("outcome = ?", q.Outcome)
		}
		if q.ActorUserID != "" {
			tx = tx.Where("actor_user_id = ?", q.ActorUserID)
		}
		if q.ScopeType != "" {
			tx = tx.Where("scope_type = ?", q.ScopeType)
		}
		if q.ScopeID != "" {
			tx = tx.Where("scope_id = ?", q.ScopeID)
		}
		if q.UnscopedOnly {
			tx = tx.Where("scope_type = ''")
		}
		if q.Search != "" {
			pattern := "%" + escapeAuditLikePattern(q.Search) + "%"
			// 关键词同时匹配 action 和操作人/目标的用户名/邮箱；LIKE 用
			// ESCAPE 让 % 和 _ 保持字面语义。用户名/邮箱通过 EXISTS 子查询
			// 关联，避免 JOIN 影响游标分页的排序与去重。
			tx = tx.Where(
				"LOWER(action) LIKE LOWER(?) ESCAPE '\\' "+
					"OR EXISTS (SELECT 1 FROM users u WHERE u.id = audit_logs.actor_user_id "+
					"AND (LOWER(u.username) LIKE LOWER(?) ESCAPE '\\' OR LOWER(u.email) LIKE LOWER(?) ESCAPE '\\')) "+
					"OR EXISTS (SELECT 1 FROM users u2 WHERE u2.id = audit_logs.target_user_id "+
					"AND (LOWER(u2.username) LIKE LOWER(?) ESCAPE '\\' OR LOWER(u2.email) LIKE LOWER(?) ESCAPE '\\'))",
				pattern, pattern, pattern, pattern, pattern,
			)
		}
	}

	var entries []*types.AuditLog
	if err := tx.Order("id DESC").Limit(limit).Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// LatestBanAuditForTarget 返回目标用户全平台最近一条成功封禁审计。
//
// 这里刻意不按 tenant_id 过滤：解封规则要判断的是"最近一次 ban 发生在
// 哪个租户"，而不是"某个租户是否 ban 过该账号"。排序用自增 id 而非
// created_at，同一毫秒内的多条审计也能得到确定的先后。
func (r *auditLogRepository) LatestBanAuditForTarget(
	ctx context.Context,
	targetUserID string,
) (*types.AuditLog, error) {
	var entry types.AuditLog
	err := r.db.WithContext(ctx).
		Where("action = ?", types.AuditActionMemberBanned).
		Where("outcome = ?", types.AuditOutcomeSuccess).
		Where("target_user_id = ?", targetUserID).
		Order("id DESC").
		Limit(1).
		Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// CountSinceForDedup powers AuditLogService.LogDenied's sliding-window
// dedup. The (tenant_id, action) index makes the count cheap; the
// remaining filters are applied as additional WHERE clauses on the
// indexed result set.
//
// Returning early on the first match would be slightly cheaper, but
// gorm's Count works on the same plan and the constant overhead is
// negligible compared to the dedup benefit.
func (r *auditLogRepository) CountSinceForDedup(
	ctx context.Context,
	tenantID uint64,
	actorUserID string,
	action types.AuditAction,
	requestPath string,
	since time.Time,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.AuditLog{}).
		Where("tenant_id = ?", tenantID).
		Where("actor_user_id = ?", actorUserID).
		Where("action = ?", action).
		Where("request_path = ?", requestPath).
		Where("created_at >= ?", since).
		Count(&count).Error
	return count, err
}

// DeleteOlderThan purges rows strictly older than cutoff in a single
// DELETE. The retention sweep (driven by the audit log service) calls
// it once a day with cutoff = now - retention_days.
//
// Tenant scope is intentionally not part of this signature: retention
// is a global ops policy, not a per-tenant choice. If we ever need
// per-tenant retention, we'd add a separate DeleteOlderThanForTenant
// rather than overload this primitive.
//
// Returns the number of rows affected so the caller can log the sweep
// outcome at INFO. Errors propagate verbatim — the caller decides
// whether they're terminal or transient.
func (r *auditLogRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("created_at < ?", cutoff).
		Delete(&types.AuditLog{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
