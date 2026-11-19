package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ReparseQuotaGate 是失败文档受限重试的限次闸（T04，AC-12/14/15）：同一文档
// 24h 滚动窗口内 reparse ≤3 次（不分入口、不分配置），计数源 = audit_logs 的
// knowledge.reparse_started 行（每次成功提交各记一条），系统管理员豁免。
//
// fail-open 边界（票条件 9，刻意不写防御兜底）：audit 写入是 best-effort
// （recordKBActivity 失败不回滚业务提交）。某次放行的审计行丢失时，该次重试
// 不被计数，结果是「少拒一次」而非误拒——这是审计通道的既有语义，闸照单
// 接受，不做补偿写或兜底计数。
//
// 并发口径：单文档入口在「查计数 → 服务提交（内部写审计行）」全程持有文档级
// advisory lock（D-3 先例：PG pg_advisory_xact_lock / MySQL GET_LOCK / SQLite
// 进程内互斥），并发请求串行判定，不会同看旧计数而超卖。批量入口只做无锁
// 预检：审计行由异步 worker 稍后才写，此刻加锁封不住窗口，预检按快照剔除
// 已超限文档即可。
type ReparseQuotaGate struct {
	db    *gorm.DB
	audit interfaces.AuditLogRepository
}

const (
	// reparseQuotaLimit 是用户拍板的每文档 24h 上限（裁定⑤）。
	reparseQuotaLimit = 3
	// reparseQuotaWindow 是滚动计数窗口。
	reparseQuotaWindow = 24 * time.Hour
	// ReparseQuotaErrorCode 是 429 details 里的机器可读错误码，前端据此出人话卡片。
	ReparseQuotaErrorCode = "RETRY_LIMIT_EXHAUSTED"
	// reparseQuotaTarget 与审计行 target_type 列对齐（knowledgeID 在 target_id）。
	reparseQuotaTarget = "knowledge"
)

// NewReparseQuotaGate 基于共享 GORM 连接构造限次闸。
func NewReparseQuotaGate(db *gorm.DB) *ReparseQuotaGate {
	return &ReparseQuotaGate{db: db, audit: repository.NewAuditLogRepository(db)}
}

// Acquire 判定单文档重试额度并占住文档级锁：额度未尽返回 release（调用方
// defer 释放，锁需覆盖服务提交与审计行写入全程）；已耗尽返回 429 AppError
// （code=1006 + details{error_code, recover_at}）；系统管理员豁免，release 为
// 无操作 no-op。返回的错误恒为 *apperrors.AppError：429=限次耗尽，5xx=计数/
// 锁故障。
func (g *ReparseQuotaGate) Acquire(ctx context.Context, tenantID uint64, knowledgeID string) (func(), error) {
	if types.IsSystemAdminFromContext(ctx) {
		return func() {}, nil
	}
	release, err := g.lockDocument(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	count, earliest, err := g.countWindow(ctx, tenantID, knowledgeID)
	if err != nil {
		release()
		return nil, err
	}
	if count >= reparseQuotaLimit {
		release()
		return nil, reparseQuotaExhaustedError(earliest)
	}
	return release, nil
}

// ReparseQuotaSkip 是批量预检剔除的一条文档记录（响应 data.skipped 元素）。
type ReparseQuotaSkip struct {
	KnowledgeID string    `json:"knowledge_id"`
	Reason      string    `json:"reason"`
	RecoverAt   time.Time `json:"recover_at"`
}

// FilterBatch 对批量入口做逐文档额度预检（不加锁，见文件头并发口径）：
// 有额度的文档保持原序返回，超限的逐条点名剔除。系统管理员在逐文档
// 判定中豁免，全量原样通过。
func (g *ReparseQuotaGate) FilterBatch(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) ([]string, []ReparseQuotaSkip, error) {
	allowed := make([]string, 0, len(knowledgeIDs))
	var skipped []ReparseQuotaSkip
	for _, id := range knowledgeIDs {
		ok, recoverAt, err := g.verdict(ctx, tenantID, id)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			allowed = append(allowed, id)
			continue
		}
		skipped = append(skipped, ReparseQuotaSkip{
			KnowledgeID: id, Reason: ReparseQuotaErrorCode, RecoverAt: recoverAt,
		})
	}
	return allowed, skipped, nil
}

// verdict 报告单文档当前是否仍有额度，顺带返回额度恢复时间（最早计数+24h）。
func (g *ReparseQuotaGate) verdict(ctx context.Context, tenantID uint64, knowledgeID string) (bool, time.Time, error) {
	if types.IsSystemAdminFromContext(ctx) {
		return true, time.Time{}, nil
	}
	count, earliest, err := g.countWindow(ctx, tenantID, knowledgeID)
	if err != nil {
		return false, time.Time{}, err
	}
	return count < reparseQuotaLimit, earliest, nil
}

func (g *ReparseQuotaGate) countWindow(ctx context.Context, tenantID uint64, knowledgeID string) (int64, time.Time, error) {
	count, earliest, err := g.audit.CountRecentByAction(ctx, tenantID,
		types.AuditActionKnowledgeReparseStarted, reparseQuotaTarget, knowledgeID,
		time.Now().Add(-reparseQuotaWindow))
	if err != nil {
		logger.Errorf(ctx, "reparse quota count failed, tenant: %d, knowledge_id: %s, err: %v",
			tenantID, knowledgeID, err)
		return 0, time.Time{}, apperrors.NewInternalServerError("failed to check reparse retry quota")
	}
	return count, earliest, nil
}

// reparseQuotaExhaustedError 构造 429 信封：code=1006（ErrTooManyRequests，
// quota 类守卫既有先例）+ 人话 message + details{error_code, recover_at}；
// recover_at = 窗口内最早一次计数 + 24h，即额度开始恢复的时间（RFC3339）。
func reparseQuotaExhaustedError(earliest time.Time) error {
	recoverAt := earliest.Add(reparseQuotaWindow)
	return apperrors.NewTooManyRequestsError(fmt.Sprintf(
		"该文档 24 小时内重试次数已达上限（%d 次），请于 %s 后再试或联系系统管理员",
		reparseQuotaLimit, recoverAt.Local().Format("2006-01-02 15:04"))).
		WithDetails(map[string]any{
			"error_code": ReparseQuotaErrorCode,
			"recover_at": recoverAt,
		})
}

// ── 文档级 advisory lock（D-3 先例，与 repository/user_row_lock.go 同型）──

// reparseQuotaLocks 是 SQLite/Lite 单进程形态的按文档互斥锁；条目数量与
// 「被重试的文档数」同阶，保留不删除以避免释放/获取竞态。
var reparseQuotaLocks sync.Map

func reparseQuotaLockName(tenantID uint64, knowledgeID string) string {
	return fmt.Sprintf("reparse-quota:%d:%s", tenantID, knowledgeID)
}

func (g *ReparseQuotaGate) lockDocument(ctx context.Context, tenantID uint64, knowledgeID string) (func(), error) {
	name := reparseQuotaLockName(tenantID, knowledgeID)
	switch g.db.Name() {
	case "postgres":
		tx := g.db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, apperrors.NewInternalServerError("failed to open reparse quota lock")
		}
		// 事务级 advisory lock：回滚即释放。不能锁 audit_logs 行——审计写入
		// 在另一条连接上进行，行锁会把自己锁死（同 user_row_lock 的取舍）。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", name).Error; err != nil {
			_ = tx.Rollback()
			logger.Errorf(ctx, "reparse quota pg advisory lock failed: %v", err)
			return nil, apperrors.NewInternalServerError("failed to acquire reparse quota lock")
		}
		return func() { _ = tx.Rollback() }, nil
	case "mysql":
		tx := g.db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, apperrors.NewInternalServerError("failed to open reparse quota lock")
		}
		var got int
		if err := tx.Raw("SELECT GET_LOCK(?, 30)", name).Scan(&got).Error; err != nil {
			_ = tx.Rollback()
			logger.Errorf(ctx, "reparse quota mysql lock failed: %v", err)
			return nil, apperrors.NewInternalServerError("failed to acquire reparse quota lock")
		}
		if got != 1 {
			_ = tx.Rollback()
			return nil, apperrors.NewInternalServerError("timed out acquiring reparse quota lock")
		}
		return func() {
			// GET_LOCK 是连接级锁，必须显式释放后再回滚连接。
			_ = tx.Exec("SELECT RELEASE_LOCK(?)", name).Error
			_ = tx.Rollback()
		}, nil
	default:
		value, _ := reparseQuotaLocks.LoadOrStore(name, &sync.Mutex{})
		mu := value.(*sync.Mutex)
		mu.Lock()
		return mu.Unlock, nil
	}
}
