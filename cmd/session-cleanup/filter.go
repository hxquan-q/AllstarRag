package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// nowFunc 便于单测固定时间；生产环境就是 time.Now。
var nowFunc = time.Now

// ownerKey 标识「工作区 + 会话属主」这一组。BatchDeleteSessions 的可见性
// 按 (tenant, owner) 判定，因此扫描、分组、上下文构造都用它做键。
type ownerKey struct {
	TenantID uint64
	UserID   string
}

func (k ownerKey) String() string {
	return strconv.FormatUint(k.TenantID, 10) + "/" + k.UserID
}

// scanData 是 dry-run 与 --apply 共用的扫描结果。
type scanData struct {
	// Members 是指定范围内的 viewer 活跃成员（去重前，可能一人多工作区）。
	Members []*types.TenantMember
	// Bindings 按 (工作区, 用户) 给出该工作区的绑定 agent id 列表。
	// 键一定覆盖 Members 里的每一项；空切片表示零绑定。
	Bindings map[ownerKey][]string
	// Sessions 是 viewer 成员名下的会话（未按产线过滤）。
	Sessions []*types.Session
}

// plan 是一次清理的候选清单。
type plan struct {
	Sessions []*types.Session
	ByOwner  map[ownerKey][]*types.Session
	// ZeroBindingOwners 是零绑定（bound_agent_ids_by_tenant 无该工作区键）的
	// viewer 成员，包含没有会话的用户。
	ZeroBindingOwners []ownerKey
	// ZeroBindingSessionCount 是零绑定用户名下的候选会话数。
	ZeroBindingSessionCount int
}

// SessionCount 返回待删会话总数。
func (p *plan) SessionCount() int {
	return len(p.Sessions)
}

// loadScanData 扫出指定工作区的 viewer 成员、绑定与候选会话。
// tenantID == 0 表示扫全部工作区（只允许 dry-run）。
func loadScanData(ctx context.Context, db *gorm.DB, tenantID uint64) (*scanData, error) {
	members, err := loadViewerMembers(ctx, db, tenantID)
	if err != nil {
		return nil, fmt.Errorf("读取 viewer 成员失败: %w", err)
	}
	bindings, err := loadBindings(ctx, db, members)
	if err != nil {
		return nil, fmt.Errorf("读取用户绑定失败: %w", err)
	}
	sessions, err := loadSessions(ctx, db, members)
	if err != nil {
		return nil, fmt.Errorf("读取会话失败: %w", err)
	}
	return &scanData{Members: members, Bindings: bindings, Sessions: sessions}, nil
}

// loadViewerMembers 只取 role='viewer' 且 status='active' 的成员。
// GORM 会自动排除 tenant_members.deleted_at 非空的行。
func loadViewerMembers(ctx context.Context, db *gorm.DB, tenantID uint64) ([]*types.TenantMember, error) {
	query := db.WithContext(ctx).
		Model(&types.TenantMember{}).
		Where("role = ? AND status = ?", types.TenantRoleViewer, types.TenantMemberStatusActive)
	if tenantID != 0 {
		query = query.Where("tenant_id = ?", tenantID)
	}
	var members []*types.TenantMember
	if err := query.Order("tenant_id ASC, user_id ASC").Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// loadBindings 读取每个成员在其工作区下的绑定列表。
// 用户行不存在或从未绑定过都按零绑定处理，避免漏扫。
func loadBindings(
	ctx context.Context,
	db *gorm.DB,
	members []*types.TenantMember,
) (map[ownerKey][]string, error) {
	out := make(map[ownerKey][]string, len(members))
	if len(members) == 0 {
		return out, nil
	}

	idSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		idSet[member.UserID] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var users []*types.User
	if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	usersByID := make(map[string]*types.User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}

	for _, member := range members {
		key := ownerKey{TenantID: member.TenantID, UserID: member.UserID}
		user := usersByID[member.UserID]
		if user == nil {
			out[key] = nil
			continue
		}
		tenantKey := strconv.FormatUint(member.TenantID, 10)
		out[key] = user.Preferences.BoundAgentIDsByTenant[tenantKey]
	}
	return out, nil
}

// loadSessions 按 (工作区, 属主) 精确取普通网页会话。
// 注意：不能用 SessionRepository.GetByTenantID —— 它会把 user_id 为空的
// 历史/渠道会话也算进每个用户的可见范围。这里只取 user_id 精确等于
// viewer 成员的行，且 GORM 自动排除 deleted_at 非空的行。
// kind 只取空字符串：迁移 000121/000039 将该列定义为非空、默认空字符串，
// 助手抽屉历史（assistant）和委派子会话转录（agent_delegated）不属于
// AC-16 的「客户无标记会话」，绝不能删（见 T05 复核意见）。
func loadSessions(
	ctx context.Context,
	db *gorm.DB,
	members []*types.TenantMember,
) ([]*types.Session, error) {
	userIDsByTenant := make(map[uint64][]string)
	for _, member := range members {
		userIDsByTenant[member.TenantID] = append(userIDsByTenant[member.TenantID], member.UserID)
	}
	tenants := make([]uint64, 0, len(userIDsByTenant))
	for tenantID := range userIDsByTenant {
		tenants = append(tenants, tenantID)
	}
	sort.Slice(tenants, func(i, j int) bool { return tenants[i] < tenants[j] })

	var all []*types.Session
	for _, tenantID := range tenants {
		var sessions []*types.Session
		err := db.WithContext(ctx).
			Where("tenant_id = ? AND user_id IN ? AND kind = ?", tenantID, userIDsByTenant[tenantID], "").
			Find(&sessions).Error
		if err != nil {
			return nil, err
		}
		all = append(all, sessions...)
	}
	return all, nil
}

// buildPlan 应用筛选规则：
//   - 会话属主必须是 scan 出来的 viewer 成员（管理员/owner/contributor 天然不在 map 里）；
//   - product_line 为空，或不在该用户该工作区的绑定列表里。
//
// 会话本身已由 loadSessions 保证 deleted_at IS NULL 且属主精确匹配。
func buildPlan(data *scanData) *plan {
	p := &plan{ByOwner: make(map[ownerKey][]*types.Session)}
	zeroBinding := make(map[ownerKey]bool)

	for _, member := range data.Members {
		key := ownerKey{TenantID: member.TenantID, UserID: member.UserID}
		if len(data.Bindings[key]) > 0 {
			continue
		}
		if zeroBinding[key] {
			continue
		}
		zeroBinding[key] = true
		p.ZeroBindingOwners = append(p.ZeroBindingOwners, key)
	}
	sort.Slice(p.ZeroBindingOwners, func(i, j int) bool {
		return p.ZeroBindingOwners[i].String() < p.ZeroBindingOwners[j].String()
	})

	for _, session := range data.Sessions {
		if session == nil || session.ID == "" {
			continue
		}
		key := ownerKey{TenantID: session.TenantID, UserID: session.UserID}
		bound, isViewer := data.Bindings[key]
		if !isViewer || !needsCleanup(session, bound) {
			continue
		}
		p.Sessions = append(p.Sessions, session)
		p.ByOwner[key] = append(p.ByOwner[key], session)
		if zeroBinding[key] {
			p.ZeroBindingSessionCount++
		}
	}

	sort.Slice(p.Sessions, func(i, j int) bool {
		return sessionSortKey(p.Sessions[i]) < sessionSortKey(p.Sessions[j])
	})
	for key := range p.ByOwner {
		group := p.ByOwner[key]
		sort.Slice(group, func(i, j int) bool {
			return sessionSortKey(group[i]) < sessionSortKey(group[j])
		})
	}
	return p
}

func sessionSortKey(session *types.Session) string {
	return strconv.FormatUint(session.TenantID, 10) + "/" + session.UserID + "/" + session.ID
}

// needsCleanup 判断单条会话是否需要清理。
func needsCleanup(session *types.Session, boundAgentIDs []string) bool {
	if session.ProductLine == "" {
		return true
	}
	for _, agentID := range boundAgentIDs {
		if agentID == session.ProductLine {
			return false
		}
	}
	return true
}

// ownerContext 构造以会话属主为用户身份的上下文。
// BatchDeleteSessions 先按 sessionRepo.Get(tenant, ctxUser, id) 判可见性，
// 普通 web 会话没有 admin 兜底；用管理员上下文跑会全部 ErrSessionNotFound
// 被静默跳过，最后删 0 条还报成功。因此必须逐组换成 owner 身份。
func ownerContext(parent context.Context, tenantID uint64, userID string) context.Context {
	ctx := context.WithValue(parent, types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.UserIDContextKey, userID)
}

// batchDeleteFunc 与 interfaces.SessionService.BatchDeleteSessions 同形，
// 单独抽出来便于在单测里替换。
type batchDeleteFunc func(ctx context.Context, ids []string) error

// applyPlan 按 (tenant, owner) 分组，逐组构造属主上下文并调用删除函数。
// 返回每组失败的描述；实际删掉多少条由调用方再查库核对。
func applyPlan(ctx context.Context, p *plan, deleteBatch batchDeleteFunc) []string {
	keys := make([]ownerKey, 0, len(p.ByOwner))
	for key := range p.ByOwner {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	var errs []string
	for _, key := range keys {
		ids := make([]string, 0, len(p.ByOwner[key]))
		for _, session := range p.ByOwner[key] {
			ids = append(ids, session.ID)
		}
		if err := deleteBatch(ownerContext(ctx, key.TenantID, key.UserID), ids); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key.String(), err))
		}
	}
	return errs
}

// verifyDeletion 用「会话行是否还存在」核对真实删除数。
// 删除是软删，GORM 默认过滤 deleted_at 非空的行，因此 count == 0 即已删。
func verifyDeletion(
	ctx context.Context,
	db *gorm.DB,
	sessions []*types.Session,
) (deletedIDs, failedIDs, errs []string) {
	for _, session := range sessions {
		var count int64
		err := db.WithContext(ctx).
			Model(&types.Session{}).
			Where("tenant_id = ? AND id = ?", session.TenantID, session.ID).
			Count(&count).Error
		if err != nil {
			failedIDs = append(failedIDs, session.ID)
			errs = append(errs, fmt.Sprintf("%s: %v", session.ID, err))
			continue
		}
		if count == 0 {
			deletedIDs = append(deletedIDs, session.ID)
			continue
		}
		failedIDs = append(failedIDs, session.ID)
	}
	return deletedIDs, failedIDs, errs
}

// checkConfirm 保证 --confirm 与本次筛选出的总数一致。
func checkConfirm(confirmed, total int) error {
	if confirmed != total {
		return fmt.Errorf("--confirm %d 与本次筛选出的会话总数 %d 不一致，拒绝执行", confirmed, total)
	}
	return nil
}

// auditRecord 是落盘的审计文件内容（一次性迁移的凭证）。
type auditRecord struct {
	TenantID                uint64         `json:"tenant_id"`
	GeneratedAt             string         `json:"generated_at"`
	TotalCandidates         int            `json:"total_candidates"`
	DeletedCount            int            `json:"deleted_count"`
	FailedCount             int            `json:"failed_count"`
	DeletedSessionIDs       []string       `json:"deleted_session_ids"`
	FailedSessionIDs        []string       `json:"failed_session_ids"`
	ByUser                  map[string]int `json:"by_user"`
	ZeroBindingUsers        []string       `json:"zero_binding_users"`
	ZeroBindingSessionCount int            `json:"zero_binding_session_count"`
	Errors                  []string       `json:"errors,omitempty"`
}

// auditFilePattern 是「同一工作区已有审计文件」的判定模式。
func auditFilePattern(dir string, tenantID uint64) string {
	return filepath.Join(dir, fmt.Sprintf("session-cleanup-%d-*.json", tenantID))
}

// findAuditFile 返回该工作区已存在的审计文件；没有则返回空串。
func findAuditFile(dir string, tenantID uint64) (string, error) {
	matches, err := filepath.Glob(auditFilePattern(dir, tenantID))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Strings(matches)
	return matches[0], nil
}

// auditFileName 生成审计文件名：session-cleanup-<tenant>-<timestamp>.json。
func auditFileName(tenantID uint64, now time.Time) string {
	return fmt.Sprintf("session-cleanup-%d-%s.json", tenantID, now.Format("20060102-150405"))
}

// writeAudit 把审计记录写到 dir，返回实际路径。
func writeAudit(dir string, record auditRecord) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// 文件名与 record.GeneratedAt 用同一个 nowFunc：单测固定时间后两者一致（F7）。
	path := filepath.Join(dir, auditFileName(record.TenantID, nowFunc()))
	if err := writeAuditTo(path, record); err != nil {
		return "", err
	}
	return path, nil
}

// createAuditPlaceholder 在删除开始前原子创建占位审计文件（F2）：
// O_CREATE|O_EXCL 保证「已存在即失败」，与 findAuditFile 是同一道一次性闸。
// 之后进程即便在删除中途被杀、或最终审计写盘失败，这个文件也会挡住下一轮重删。
func createAuditPlaceholder(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

// writeAuditTo 把完整审计内容覆盖写到指定路径（占位文件的路径），保持闸在同一文件上。
func writeAuditTo(path string, record auditRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
