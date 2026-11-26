package service

// T08 测试：云之家管理端服务（AC-14 / AC-16 / C4）。
//
// 覆盖：状态响应形状与脱敏、预检只读、enabled=false 仍可预检/手动同步、
// 手动同步审计、运行中互斥、解除 blocked 清状态与重复解除语义。
// 复用 T05 的替身（客户端/员工仓储/运行记录/账号存储/生命周期），
// 所有数据均为占位符，不使用真实员工信息。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// yzjT08Audit 捕获管理端写入的审计条目。
type yzjT08Audit struct {
	interfaces.AuditLogService
	entries []*types.AuditLog
}

func (a *yzjT08Audit) Log(_ context.Context, entry *types.AuditLog) error {
	a.entries = append(a.entries, entry)
	return nil
}

// yzjT08NewAdmin 组装一个管理端服务；sync 用真实同步服务 + T05 替身。
func yzjT08NewAdmin(
	cfgEnabled bool,
	client yunzhijia.Client,
	repo *yzjT05EmployeeRepo,
	runs *yzjT05RunRepo,
	life *yzjT05Lifecycle,
	audit *yzjT08Audit,
) *YunzhijiaAdminService {
	cfg := yzjT05Config(10, cfgEnabled)
	syncSvc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)
	return NewYunzhijiaAdminService(cfg, syncSvc, runs, repo, audit)
}

func yzjT08SetPage(client *yzjT05Client, status yunzhijia.Status, page *yunzhijia.EmployeePage) {
	client.setPage(status, 1, page)
}

// TestYunzhijiaT08StatusShapeAndRedaction 验证状态响应只含
// enabled + 最近运行 + blocked，且不含手机号 / user_id / 全量员工。
func TestYunzhijiaT08StatusShapeAndRedaction(t *testing.T) {
	ctx := context.Background()
	phone := "13800000000"
	blockedUserID := "user-blocked-placeholder"
	blockedAt := time.Now().Add(-time.Hour)
	blockedBy := "actor-admin"
	repo := newYZJT05EmployeeRepo(
		&types.YunzhijiaEmployee{
			OpenID:   "open-created-placeholder",
			Name:     "在职占位员工甲",
			Status:   types.YunzhijiaEmployeeStatusActive,
			TenantID: yzjT05TenantDefault,
			Mobile:   &phone,
		},
		&types.YunzhijiaEmployee{
			OpenID:     "open-blocked-placeholder",
			Name:       "禁建占位员工乙",
			Department: "金蝶社区",
			Status:     types.YunzhijiaEmployeeStatusActive,
			TenantID:   yzjT05TenantDefault,
			UserID:     &blockedUserID,
			Mobile:     &phone,
			Blocked:    true,
			BlockedAt:  &blockedAt,
			BlockedBy:  &blockedBy,
		},
	)
	runs := &yzjT05RunRepo{}
	finished := time.Now()
	if err := runs.CreateRunning(ctx, &types.YunzhijiaSyncRun{
		Trigger: types.YunzhijiaSyncTriggerCron,
	}); err != nil {
		t.Fatalf("写入运行记录失败: %v", err)
	}
	if err := runs.Finish(ctx, &types.YunzhijiaSyncRun{
		ID: 1, Trigger: types.YunzhijiaSyncTriggerCron,
		Status: types.YunzhijiaSyncStatusSuccess, StartedAt: finished.Add(-time.Second),
		FinishedAt: &finished, ActiveCount: 2, InactiveCount: 0,
		CreatedCount: 1, UpdatedCount: 1, DisabledCount: 0, MigratedCount: 0,
		AnomalyCount: 1,
		Anomalies:    types.JSON(`[{"kind":"placeholder_anomaly","open_id":"open-blocked-placeholder"}]`),
	}); err != nil {
		t.Fatalf("结束运行记录失败: %v", err)
	}
	svc := yzjT08NewAdmin(true, newYZJT05Client(), repo, runs, newYZJT05Lifecycle(), &yzjT08Audit{})

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status 返回错误: %v", err)
	}
	if !status.Enabled {
		t.Fatal("enabled 应为 true")
	}
	if status.Latest == nil {
		t.Fatal("latest 不应为空")
	}
	if status.Latest.CreatedCount != 1 || status.Latest.UpdatedCount != 1 {
		t.Fatalf("latest 计数不符: %+v", status.Latest)
	}
	if status.Latest.AnomalyCount != 1 || len(status.Latest.Anomalies) != 1 {
		t.Fatalf("latest 异常清单不符: %+v", status.Latest)
	}
	if status.Latest.Anomalies[0].Kind != "placeholder_anomaly" {
		t.Fatalf("异常类型不符: %+v", status.Latest.Anomalies[0])
	}
	if len(status.Blocked) != 1 || status.Blocked[0].OpenID != "open-blocked-placeholder" {
		t.Fatalf("blocked 清单不符: %+v", status.Blocked)
	}

	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("序列化状态失败: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		phone, "user_id", "mobile", "ticket", "token",
		"open-created-placeholder", "在职占位员工甲",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("状态响应不应包含 %q，实际: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "open-blocked-placeholder") {
		t.Fatalf("blocked 项应保留 open_id，实际: %s", body)
	}
}

// TestYunzhijiaT08PreviewDoesNotWriteAccounts 验证预检只读：员工表、
// 用户与成员关系都不发生变化，也不调用生命周期。
func TestYunzhijiaT08PreviewDoesNotWriteAccounts(t *testing.T) {
	ctx := context.Background()
	repo, accounts := yzjT05SeedRepoWithAccount()
	client := newYZJT05Client()
	yzjT08SetPage(client, yunzhijia.StatusActive, yzjT05Page(1, 10, 2,
		yzjT05Employee("open-existing", "既有员工", "金蝶社区"),
		yzjT05Employee("open-new-0001", "占位新员工", "人力资源部"),
	))
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	runs := &yzjT05RunRepo{}
	svc := yzjT08NewAdmin(true, client, repo, runs, life, &yzjT08Audit{})

	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()
	result, err := svc.Preview(ctx)
	if err != nil {
		t.Fatalf("Preview 返回错误: %v", err)
	}
	if result.ActiveCount != 2 || result.TenantHRCount != 1 || result.TenantDefaultCount != 1 {
		t.Fatalf("预检计数不符: %+v", result)
	}
	yzjT05AssertAccountsUnchanged(t, repo, accounts, rowsBefore, usersBefore, membersBefore, "预检")
	if repo.upserts != 0 {
		t.Fatalf("预检不得 upsert 员工记录，实际 %d 次", repo.upserts)
	}
	if len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("预检不得调用账号生命周期")
	}
}

// TestYunzhijiaT08EnabledFalseAllowsPreviewAndManualSync 验证登录未启用时
// 首次导入仍可用，且手动同步写审计。
func TestYunzhijiaT08EnabledFalseAllowsPreviewAndManualSync(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.UserIDContextKey, "actor-t08")
	repo := newYZJT05EmployeeRepo()
	client := newYZJT05Client()
	yzjT08SetPage(client, yunzhijia.StatusActive, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-first-0001", "占位员工丙", "金蝶社区"),
	))
	life := newYZJT05Lifecycle()
	life.setEmployeeResult("open-first-0001", types.YunzhijiaAccountResultCreated)
	runs := &yzjT05RunRepo{}
	audit := &yzjT08Audit{}
	svc := yzjT08NewAdmin(false, client, repo, runs, life, audit)

	preview, err := svc.Preview(ctx)
	if err != nil {
		t.Fatalf("enabled=false 时预检应可用，实际错误: %v", err)
	}
	if preview.ActiveCount != 1 {
		t.Fatalf("预检计数=%d，期望 1", preview.ActiveCount)
	}

	run, err := svc.ManualSync(ctx)
	if err != nil {
		t.Fatalf("enabled=false 时手动同步应可用，实际错误: %v", err)
	}
	if run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("手动同步状态=%s，期望 success", run.Status)
	}
	if run.CreatedCount != 1 {
		t.Fatalf("created_count=%d，期望 1", run.CreatedCount)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("手动同步应写 1 条审计，实际 %d", len(audit.entries))
	}
	entry := audit.entries[0]
	if entry.Action != types.AuditActionSystemYunzhijiaSyncTriggered {
		t.Fatalf("审计动作=%s，期望 %s", entry.Action, types.AuditActionSystemYunzhijiaSyncTriggered)
	}
	if entry.Outcome != types.AuditOutcomeSuccess {
		t.Fatalf("审计结果=%s，期望 success", entry.Outcome)
	}
	if len(entry.Details) == 0 {
		t.Fatal("审计应带运行摘要 details")
	}
}

// TestYunzhijiaT08ManualSyncLogsCompletionWithoutEmployeeIdentity 覆盖 F9/AC-20：
// 手动同步成功时输出与调度器一致的完成日志（trigger/run/计数/耗时），
// 且不含员工姓名、open_id、ticket 或本地 user_id。
func TestYunzhijiaT08ManualSyncLogsCompletionWithoutEmployeeIdentity(t *testing.T) {
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetLogLevel(logger.LevelInfo)
	t.Cleanup(func() { logger.ConfigureFromEnv() })

	const employeeName = "敏感员工姓名占位T08"
	const openID = "open-sensitive-t08-placeholder"

	repo := newYZJT05EmployeeRepo()
	client := newYZJT05Client()
	yzjT08SetPage(client, yunzhijia.StatusActive, yzjT05Page(1, 10, 1,
		yzjT05Employee(openID, employeeName, "金蝶社区"),
	))
	life := newYZJT05Lifecycle()
	life.setEmployeeResult(openID, types.YunzhijiaAccountResultCreated)
	runs := &yzjT05RunRepo{}
	svc := yzjT08NewAdmin(false, client, repo, runs, life, &yzjT08Audit{})

	run, err := svc.ManualSync(context.Background())
	if err != nil {
		t.Fatalf("手动同步返回错误: %v", err)
	}
	if run == nil {
		t.Fatal("手动同步未返回运行记录")
	}

	out := logs.String()
	for _, want := range []string{
		"同步完成",
		"trigger=manual",
		fmt.Sprintf("run=%d", run.ID),
		"active=1",
		"inactive=0",
		"created=1",
		"anomalies=0",
		"耗时=",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("手动同步完成日志缺少 %q；日志=%q", want, out)
		}
	}
	for _, forbidden := range []string{employeeName, openID, "ticket", "user_id"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("手动同步完成日志不得包含 %q；日志=%q", forbidden, out)
		}
	}
}

// TestYunzhijiaT08ManualSyncRunningReturnsConflict 验证运行中再次手动同步
// 返回 ErrSyncRunning，由 handler 映射为 409，不产生第二个运行。
func TestYunzhijiaT08ManualSyncRunningReturnsConflict(t *testing.T) {
	ctx := context.Background()
	repo := newYZJT05EmployeeRepo()
	client := newYZJT05Client()
	yzjT08SetPage(client, yunzhijia.StatusActive, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-block-0001", "占位员工丁", "金蝶社区"),
	))
	client.block = make(chan struct{})
	client.entered = make(chan struct{})
	runs := &yzjT05RunRepo{}
	svc := yzjT08NewAdmin(true, client, repo, runs, newYZJT05Lifecycle(), &yzjT08Audit{})

	done := make(chan error, 1)
	go func() {
		_, err := svc.ManualSync(ctx)
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("首个手动同步未进入抓取阶段")
	}

	if _, err := svc.ManualSync(ctx); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("运行中再次同步应返回 ErrSyncRunning，实际 %v", err)
	}

	close(client.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首个手动同步失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("首个手动同步未结束")
	}
	if runs.createCount() != 1 {
		t.Fatalf("不应创建第二个运行，实际 create=%d", runs.createCount())
	}
}

// TestYunzhijiaT08UnblockClearsStateAndAudits 验证解除 blocked 清空
// blocked/user_id/blocked_at/blocked_by 并写审计，重复解除返回哨兵错误。
func TestYunzhijiaT08UnblockClearsStateAndAudits(t *testing.T) {
	ctx := context.Background()
	userID := "user-unblock-placeholder"
	blockedAt := time.Now().Add(-time.Hour)
	blockedBy := "actor-prior"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:         "open-unblock-placeholder",
		Name:           "禁建占位员工戊",
		Department:     "金蝶社区",
		Status:         types.YunzhijiaEmployeeStatusActive,
		TenantID:       yzjT05TenantDefault,
		UserID:         &userID,
		DisabledBySync: true, // 离职停用遗留标记：解除时必须一并清掉
		Blocked:        true,
		BlockedAt:      &blockedAt,
		BlockedBy:      &blockedBy,
	})
	audit := &yzjT08Audit{}
	svc := yzjT08NewAdmin(
		true, newYZJT05Client(), repo, &yzjT05RunRepo{}, newYZJT05Lifecycle(), audit,
	)

	if err := svc.Unblock(ctx, "open-unblock-placeholder", "actor-t08"); err != nil {
		t.Fatalf("解除 blocked 失败: %v", err)
	}
	row, err := repo.GetByOpenID(ctx, "open-unblock-placeholder")
	if err != nil {
		t.Fatalf("读取员工失败: %v", err)
	}
	if row == nil {
		t.Fatal("员工记录不应消失")
	}
	if row.Blocked || row.UserID != nil || row.BlockedAt != nil || row.BlockedBy != nil {
		t.Fatalf("解除后状态未清空: %+v", row)
	}
	if row.DisabledBySync {
		t.Fatalf("解除后必须清 disabled_by_sync，否则登录仍拒绝: %+v", row)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("解除 blocked 应写 1 条审计，实际 %d", len(audit.entries))
	}
	entry := audit.entries[0]
	if entry.Action != types.AuditActionSystemYunzhijiaUnblocked {
		t.Fatalf("审计动作=%s，期望 %s", entry.Action, types.AuditActionSystemYunzhijiaUnblocked)
	}
	if entry.ActorUserID != "actor-t08" {
		t.Fatalf("审计操作人=%q，期望 actor-t08", entry.ActorUserID)
	}

	if err := svc.Unblock(ctx, "open-unblock-placeholder", "actor-t08"); !errors.Is(
		err, ErrYunzhijiaEmployeeNotFound,
	) {
		t.Fatalf("重复解除应返回 ErrYunzhijiaEmployeeNotFound，实际 %v", err)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("重复解除不应再写审计，实际 %d 条", len(audit.entries))
	}
}

// yzjT08CancelAwareClient 在每次抓取前检查 ctx 是否已取消。
// 只有 ManualSync/Preview 用 context.WithoutCancel 脱离请求取消才能通过；
// 若实现回退为直接透传请求 ctx，本替身会让抓取失败。
type yzjT08CancelAwareClient struct {
	*yzjT05Client
}

func (c *yzjT08CancelAwareClient) ListEmployees(
	ctx context.Context, status yunzhijia.Status, page, pageSize int,
) (*yunzhijia.EmployeePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.yzjT05Client.ListEmployees(ctx, status, page, pageSize)
}

// TestYunzhijiaT08ManualSyncSurvivesRequestCancel 验证浏览器断开（请求 ctx
// 已取消）后手动同步仍跑完整轮：运行记录落 success、审计照写。
// 对应 H2 的 context.WithoutCancel。
func TestYunzhijiaT08ManualSyncSurvivesRequestCancel(t *testing.T) {
	base := context.WithValue(context.Background(), types.UserIDContextKey, "actor-t08-cancel")
	ctx, cancel := context.WithCancel(base)
	cancel() // 模拟请求链路已断开

	baseClient := newYZJT05Client()
	yzjT08SetPage(baseClient, yunzhijia.StatusActive, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-cancel-0001", "占位员工己", "金蝶社区"),
	))
	client := &yzjT08CancelAwareClient{yzjT05Client: baseClient}
	repo := newYZJT05EmployeeRepo()
	life := newYZJT05Lifecycle()
	life.setEmployeeResult("open-cancel-0001", types.YunzhijiaAccountResultCreated)
	runs := &yzjT05RunRepo{}
	audit := &yzjT08Audit{}
	svc := yzjT08NewAdmin(true, client, repo, runs, life, audit)

	run, err := svc.ManualSync(ctx)
	if err != nil {
		t.Fatalf("请求取消后手动同步仍应完成，实际错误: %v", err)
	}
	if run == nil || run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("运行记录应为 success，实际 %+v", run)
	}
	latest, err := runs.Latest(context.Background())
	if err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if latest == nil || latest.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("运行记录未落 success，实际 %+v", latest)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("手动同步应写 1 条审计，实际 %d 条", len(audit.entries))
	}
	if audit.entries[0].Action != types.AuditActionSystemYunzhijiaSyncTriggered {
		t.Fatalf("审计动作=%s，期望 %s", audit.entries[0].Action,
			types.AuditActionSystemYunzhijiaSyncTriggered)
	}
	if audit.entries[0].ActorUserID != "actor-t08-cancel" {
		t.Fatalf("审计操作人=%q，期望保留 ctx 里的 actor", audit.entries[0].ActorUserID)
	}
}
