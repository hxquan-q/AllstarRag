package service

// T05 TDD 红阶段：同步核心（抓取 / 校验 / 预检 / upsert / 异常 / 互斥）。
//
// 本轮只锁 T05 的行为，不实现也不测试 T06 的离职/调岗/再入职账号细节。
// 所有姓名、open_id 均为占位符。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
)

func yzjT05Employee(openID, name, department string) yunzhijia.Employee {
	return yunzhijia.Employee{OpenID: openID, Name: name, Department: department}
}

func yzjT05Page(page, pageSize, total int, items ...yunzhijia.Employee) *yunzhijia.EmployeePage {
	return &yunzhijia.EmployeePage{List: items, Page: page, PageSize: pageSize, Total: total}
}

func yzjT05RunAnomalies(t *testing.T, run *types.YunzhijiaSyncRun) []types.YunzhijiaSyncAnomaly {
	t.Helper()
	if run == nil {
		t.Fatal("运行记录为 nil")
	}
	if len(run.Anomalies) == 0 {
		return nil
	}
	var out []types.YunzhijiaSyncAnomaly
	if err := json.Unmarshal(run.Anomalies, &out); err != nil {
		t.Fatalf("解析 run.Anomalies 失败: %v（原始 %s）", err, string(run.Anomalies))
	}
	return out
}

func yzjT05HasAnomaly(
	anomalies []types.YunzhijiaSyncAnomaly, kind, openID string,
) bool {
	return yzjT05AnomalyCount(anomalies, kind, openID) > 0
}

func yzjT05AnomalyCount(
	anomalies []types.YunzhijiaSyncAnomaly, kind, openID string,
) int {
	n := 0
	for _, a := range anomalies {
		if a.Kind == kind && (openID == "" || a.OpenID == openID) {
			n++
		}
	}
	return n
}

// yzjT05SeedRepoWithAccount 预置一条员工行与对应本地账号，用于验证
// 预检/整轮失败前后「员工表 + 用户 + 成员关系」均无变化。
func yzjT05SeedRepoWithAccount() (*yzjT05EmployeeRepo, *yzjT05AccountStore) {
	uid := "user-existing"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-existing",
		Name:       "既有员工",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &uid,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})
	accounts := newYZJT05AccountStore()
	accounts.seed("open-existing", uid, yzjT05TenantDefault)
	return repo, accounts
}

func yzjT05AssertAccountsUnchanged(
	t *testing.T,
	repo *yzjT05EmployeeRepo,
	accounts *yzjT05AccountStore,
	rowsBefore map[string]*types.YunzhijiaEmployee,
	usersBefore map[string]yzjT05User,
	membersBefore map[string]uint64,
	label string,
) {
	t.Helper()
	if rowsAfter := repo.snapshotAll(); !reflect.DeepEqual(rowsBefore, rowsAfter) {
		t.Fatalf("%s前后员工表发生变化\n前: %+v\n后: %+v", label, rowsBefore, rowsAfter)
	}
	usersAfter, membersAfter := accounts.snapshot()
	if !reflect.DeepEqual(usersBefore, usersAfter) {
		t.Fatalf("%s前后用户表发生变化\n前: %+v\n后: %+v", label, usersBefore, usersAfter)
	}
	if !reflect.DeepEqual(membersBefore, membersAfter) {
		t.Fatalf("%s前后成员关系发生变化\n前: %+v\n后: %+v", label, membersBefore, membersAfter)
	}
	if uw, mw := accounts.writes(); uw != 0 || mw != 0 {
		t.Fatalf("%s不得写用户/成员关系，实际 user=%d member=%d", label, uw, mw)
	}
}

// ---------------------------------------------------------------------------
// 抓取完整性（AC-9）
// ---------------------------------------------------------------------------

func TestYunzhijiaT05RunFetchMultiPageAndExplicitPageSize(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(2, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 2, 3,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
		yzjT05Employee("open-b", "员工乙", `研发中心\人力资源部`),
	))
	client.setPage(yunzhijia.StatusActive, 2, yzjT05Page(2, 2, 3,
		yzjT05Employee("open-c", "员工丙", ""),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 2, 1,
		yzjT05Employee("open-d", "员工丁", "金蝶社区"),
	))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if run == nil {
		t.Fatal("Run 返回 nil 运行记录")
	}
	if run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("状态=%s，期望 success", run.Status)
	}
	if run.ActiveCount != 3 || run.InactiveCount != 1 {
		t.Fatalf("计数 active=%d inactive=%d，期望 3/1", run.ActiveCount, run.InactiveCount)
	}

	for _, call := range client.callSnapshot() {
		if call.PageSize != cfg.PageSize {
			t.Fatalf("ListEmployees(%s,page=%d) pageSize=%d，期望显式传 cfg.PageSize=%d",
				call.Status, call.Page, call.PageSize, cfg.PageSize)
		}
	}
	calls := map[string]bool{}
	for _, call := range client.callSnapshot() {
		calls[fmt.Sprintf("%s/%d", call.Status, call.Page)] = true
	}
	for _, key := range []string{"active/1", "active/2", "inactive/1"} {
		if !calls[key] {
			t.Fatalf("缺少抓取调用 %s；实际调用 %v", key, calls)
		}
	}

	activeCalls := life.employeeSnapshot()
	if len(activeCalls) != 3 {
		t.Fatalf("ApplyEmployee 调用 %d 次，期望 3", len(activeCalls))
	}
	inactiveCalls := life.inactiveSnapshot()
	if len(inactiveCalls) != 1 || inactiveCalls[0].OpenID != "open-d" {
		t.Fatalf("ApplyInactive 调用=%+v，期望仅 open-d", inactiveCalls)
	}

	tenant := map[string]uint64{}
	for _, e := range activeCalls {
		tenant[e.OpenID] = e.TenantID
	}
	if tenant["open-b"] != yzjT05TenantHR {
		t.Fatalf("open-b tenant=%d，期望人力空间 %d", tenant["open-b"], yzjT05TenantHR)
	}
	if tenant["open-a"] != yzjT05TenantDefault || tenant["open-c"] != yzjT05TenantDefault {
		t.Fatalf("默认空间映射错误: open-a=%d open-c=%d", tenant["open-a"], tenant["open-c"])
	}

	anomalies := yzjT05RunAnomalies(t, run)
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyUnknownDepartment, "open-c") {
		t.Fatalf("空部门应记 unknown_department 异常，实际 %+v", anomalies)
	}
	if run.AnomalyCount != len(anomalies) {
		t.Fatalf("AnomalyCount=%d，但 anomalies 有 %d 条", run.AnomalyCount, len(anomalies))
	}

	created := runs.createdStatuses()
	if len(created) != 1 || created[0] != types.YunzhijiaSyncStatusRunning {
		t.Fatalf("开始时应写入 running 运行记录，实际 %v", created)
	}
	if runs.finishCount() != 1 {
		t.Fatalf("结束时应写入 1 次运行记录，实际 %d", runs.finishCount())
	}
}

func TestYunzhijiaT05RunFailsOnTotalMismatch(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(2, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 2, 3,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusActive, 2, yzjT05Page(2, 2, 3)) // 累计 1 != total 3
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 2, 0))
	repo, accounts := yzjT05SeedRepoWithAccount()
	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err == nil {
		t.Fatal("总数不一致时 Run 应返回整轮错误")
	}
	latest, err := runs.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil || latest.Status != types.YunzhijiaSyncStatusFailed {
		t.Fatalf("整轮失败应写入 failed 运行记录，实际 %+v", latest)
	}
	if latest.ErrorSummary == "" {
		t.Fatal("整轮失败应写入脱敏 ErrorSummary")
	}
	if repo.upsertCount() != 0 {
		t.Fatalf("抓取不完整时不得 upsert 员工记录，实际 %d 次", repo.upsertCount())
	}
	if len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("抓取不完整时不得调用账号生命周期")
	}
	yzjT05AssertAccountsUnchanged(t, repo, accounts, rowsBefore, usersBefore, membersBefore, "总数不一致")
	if got := len(client.callsFor(yunzhijia.StatusActive)); got > 2 {
		t.Fatalf("active 抓取 %d 次，疑似未按 total/pageSize 收敛", got)
	}
}

func TestYunzhijiaT05RunFailsOnPageError(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(2, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 2, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setErr(yunzhijia.StatusInactive, 1, yunzhijia.ErrUpstream)
	repo, accounts := yzjT05SeedRepoWithAccount()
	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err == nil {
		t.Fatal("某页失败时 Run 应返回整轮错误")
	}
	latest, _ := runs.Latest(ctx)
	if latest == nil || latest.Status != types.YunzhijiaSyncStatusFailed {
		t.Fatalf("整轮失败应写入 failed 运行记录，实际 %+v", latest)
	}
	if repo.upsertCount() != 0 || len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("某页失败时不得改任何账号")
	}
	yzjT05AssertAccountsUnchanged(t, repo, accounts, rowsBefore, usersBefore, membersBefore, "某页失败")
}

func TestYunzhijiaT05RunFailsOnMissingOpenID(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	// 真实客户端遇到缺 open_id 的条目会返回 ErrContract；同步层不再自己做
	// 缺号检查，因此这里由客户端替身按真实行为注入契约错误。
	client.setErr(yunzhijia.StatusActive, 1, yunzhijia.ErrContract)
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	repo, accounts := yzjT05SeedRepoWithAccount()
	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err == nil {
		t.Fatal("客户端返回契约错误时 Run 应整轮失败")
	} else if !errors.Is(err, yunzhijia.ErrContract) {
		t.Fatalf("Run 错误应包裹 ErrContract，实际 %v", err)
	}
	latest, _ := runs.Latest(ctx)
	if latest == nil || latest.Status != types.YunzhijiaSyncStatusFailed {
		t.Fatalf("缺 open_id 应整轮失败，实际 %+v", latest)
	}
	if repo.upsertCount() != 0 || len(life.employeeSnapshot()) != 0 {
		t.Fatal("缺 open_id 时不得改任何账号")
	}
	yzjT05AssertAccountsUnchanged(t, repo, accounts, rowsBefore, usersBefore, membersBefore, "缺 open_id")
}

// H2：真实上游可能回显自己的 page_size（例如请求 500、回显 200），
// 只要 page/total/累计一致就应继续；累计不一致仍整轮失败。
func TestYunzhijiaT05RunToleratesUpstreamPageSizeEcho(t *testing.T) {
	ctx := context.Background()

	t.Run("回显 page_size 与请求不同仍成功", func(t *testing.T) {
		cfg := yzjT05Config(500, true)
		client := newYZJT05Client()
		client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 200, 2,
			yzjT05Employee("open-a", "员工甲", "金蝶社区"),
			yzjT05Employee("open-b", "员工乙", "金蝶社区"),
		))
		client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 200, 0))
		runs := &yzjT05RunRepo{}
		life := newYZJT05Lifecycle()
		svc := NewYunzhijiaSyncService(cfg, client, newYZJT05EmployeeRepo(), runs, life)

		run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
		if err != nil {
			t.Fatalf("上游回显 page_size 不同不应导致整轮失败: %v", err)
		}
		if run.Status != types.YunzhijiaSyncStatusSuccess {
			t.Fatalf("状态=%s，期望 success", run.Status)
		}
		if run.ActiveCount != 2 {
			t.Fatalf("ActiveCount=%d，期望 2", run.ActiveCount)
		}
		for _, call := range client.callSnapshot() {
			if call.PageSize != 500 {
				t.Fatalf("ListEmployees 请求 pageSize=%d，期望显式传 cfg.PageSize=500", call.PageSize)
			}
		}
		if len(life.employeeSnapshot()) != 2 {
			t.Fatalf("回显 page_size 不同时仍应处理 2 人，实际 %d", len(life.employeeSnapshot()))
		}
	})

	t.Run("回显 page_size 不同时累计不一致仍整轮失败", func(t *testing.T) {
		cfg := yzjT05Config(500, true)
		client := newYZJT05Client()
		client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 200, 3,
			yzjT05Employee("open-a", "员工甲", "金蝶社区"),
			yzjT05Employee("open-b", "员工乙", "金蝶社区"),
		))
		client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 200, 0))
		repo, accounts := yzjT05SeedRepoWithAccount()
		rowsBefore := repo.snapshotAll()
		usersBefore, membersBefore := accounts.snapshot()
		runs := &yzjT05RunRepo{}
		life := newYZJT05Lifecycle()
		life.accounts = accounts
		svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

		if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err == nil {
			t.Fatal("回显 page_size 不同不豁免累计校验，累计≠total 时应整轮失败")
		}
		latest, _ := runs.Latest(ctx)
		if latest == nil || latest.Status != types.YunzhijiaSyncStatusFailed {
			t.Fatalf("累计不一致应写入 failed 运行记录，实际 %+v", latest)
		}
		if repo.upsertCount() != 0 || len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
			t.Fatal("累计不一致时不得改任何账号")
		}
		yzjT05AssertAccountsUnchanged(t, repo, accounts, rowsBefore, usersBefore, membersBefore, "累计不一致")
	})
}

// H1：两批完整抓取后，本地 active 但不在任一上游名单的员工按离职收敛；
// 已封禁的员工整条跳过（F5：不 upsert、行保持原状），只记 blocked_skipped。
func TestYunzhijiaT05RunReconcilesLocalActiveMissing(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	missingUID := "user-missing"
	blockedUID := "user-blocked"
	blockedAt := time.Now().Add(-time.Hour)
	repo := newYZJT05EmployeeRepo(
		&types.YunzhijiaEmployee{
			OpenID:     "open-missing",
			Name:       "缺席员工",
			Department: "金蝶社区",
			TenantID:   yzjT05TenantDefault,
			UserID:     &missingUID,
			Status:     types.YunzhijiaEmployeeStatusActive,
		},
		&types.YunzhijiaEmployee{
			OpenID:     "open-blocked",
			Name:       "封禁员工",
			Department: "金蝶社区",
			TenantID:   yzjT05TenantDefault,
			UserID:     &blockedUID,
			Status:     types.YunzhijiaEmployeeStatusActive,
			Blocked:    true,
			BlockedAt:  &blockedAt,
		},
	)
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyLocalActiveMissing, "open-missing") {
		t.Fatalf("缺少 open-missing 的 local_active_missing 异常；实际 %+v", anomalies)
	}
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyBlockedSkipped, "open-blocked") {
		t.Fatalf("封禁缺席员工应记 blocked_skipped；实际 %+v", anomalies)
	}

	// F8：blocked 缺席行不再同时产生 local_active_missing，每轮只留一条
	// blocked_skipped，避免 anomaly_count 虚高。
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyLocalActiveMissing, "open-blocked"); got != 0 {
		t.Fatalf("F8：blocked 缺席行不得记 local_active_missing，实际 %d 条；全部异常 %+v",
			got, anomalies)
	}
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyBlockedSkipped, "open-blocked"); got != 1 {
		t.Fatalf("F8：blocked 缺席行应恰好记 1 条 blocked_skipped，实际 %d 条", got)
	}
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyLocalActiveMissing, "open-missing"); got != 1 {
		t.Fatalf("非 blocked 缺席行仍应记 1 条 local_active_missing，实际 %d 条", got)
	}
	// 预期异常总数：open-missing 的 local_active_missing + open-blocked 的
	// blocked_skipped，各一条。
	const wantAnomalies = 2
	if len(anomalies) != wantAnomalies {
		t.Fatalf("F8：异常总数=%d，期望 %d；实际 %+v", len(anomalies), wantAnomalies, anomalies)
	}
	if run.AnomalyCount != wantAnomalies {
		t.Fatalf("F8：run.AnomalyCount=%d，期望 %d", run.AnomalyCount, wantAnomalies)
	}

	row := repo.snapshot("open-missing")
	if row == nil || row.Status != types.YunzhijiaEmployeeStatusInactive {
		t.Fatalf("缺席员工应收敛为 inactive，实际 %+v", row)
	}
	blockedRow := repo.snapshot("open-blocked")
	if blockedRow == nil {
		t.Fatal("封禁员工记录必须保留")
	}
	if !blockedRow.Blocked {
		t.Fatal("同步不得清除 blocked")
	}
	if blockedRow.Status != types.YunzhijiaEmployeeStatusActive {
		t.Fatalf("F5：blocked 的 missing-active 行不得被收敛改写，实际 status=%s",
			blockedRow.Status)
	}
	if blockedRow.Name != "封禁员工" {
		t.Fatalf("F5：blocked 行字段不得被 upsert 覆盖，实际 name=%q", blockedRow.Name)
	}
	if got := repo.upsertCountFor("open-blocked"); got != 0 {
		t.Fatalf("F5：blocked 员工不得 upsert，实际 %d 次", got)
	}
	if got := repo.upsertCountFor("open-missing"); got != 1 {
		t.Fatalf("缺席的非 blocked 员工应 upsert 收敛一次，实际 %d 次", got)
	}

	inactive := life.inactiveSnapshot()
	foundMissing := false
	for _, employee := range inactive {
		if employee.OpenID == "open-blocked" {
			t.Fatal("封禁员工不应调用 ApplyInactive")
		}
		if employee.OpenID == "open-missing" {
			foundMissing = true
			if employee.UserID == nil || *employee.UserID != missingUID {
				t.Fatalf("ApplyInactive 应收到库内 user_id=%q，实际 %v", missingUID, employee.UserID)
			}
		}
	}
	if !foundMissing {
		t.Fatalf("缺席员工应调用 ApplyInactive；实际 %+v", inactive)
	}
}

// H1 负向：本轮 active 内部出现重复 open_id 时，去重后条数仍可能等于 total，
// 实际却漏了人；此时不得执行缺席收敛，避免把本地在职员工误判离职。
func TestYunzhijiaT05RunSkipsMissingReconcileOnDuplicateActiveOpenID(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 2,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
		yzjT05Employee("open-a", "员工甲重复", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	missingUID := "user-missing"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-missing",
		Name:       "缺席员工",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &missingUID,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("重复 open_id 只记异常，不应整轮失败: %v", err)
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyLocalActiveMissing, "") {
		t.Fatalf("active 内部重复 open_id 时不得执行缺席收敛；实际 %+v", anomalies)
	}
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyDuplicateOpenID, "open-a"); got != 1 {
		t.Fatalf("duplicate_open_id 异常 %d 条，期望 1；实际 %+v", got, anomalies)
	}
	row := repo.snapshot("open-missing")
	if row == nil || row.Status != types.YunzhijiaEmployeeStatusActive {
		t.Fatalf("缺席员工应保持 active（本轮不收敛），实际 %+v", row)
	}
	for _, employee := range life.inactiveSnapshot() {
		if employee.OpenID == "open-missing" {
			t.Fatal("active 重复 open_id 时不应调用 ApplyInactive 处理缺席员工")
		}
	}
}

func TestYunzhijiaT05RunSkipsDuplicateOpenID(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 2,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
		yzjT05Employee("open-a", "员工甲二", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("重复 open_id 只记异常，不应整轮失败: %v", err)
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyDuplicateOpenID, "open-a"); got != 1 {
		t.Fatalf("duplicate_open_id 异常 %d 条，期望 1；实际 %+v", got, anomalies)
	}
	calls := life.employeeSnapshot()
	if len(calls) != 1 {
		t.Fatalf("重复 open_id 应只保留首条，ApplyEmployee 实际 %d 次", len(calls))
	}
	if calls[0].Name != "员工甲" {
		t.Fatalf("重复 open_id 首条生效，实际姓名 %q", calls[0].Name)
	}
	if got := repo.upsertCountFor("open-a"); got != 1 {
		t.Fatalf("重复 open_id 只 upsert 一次，实际 %d 次", got)
	}
}

func TestYunzhijiaT05RunFailsWhenActiveTotalZero(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 0))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-d", "员工丁", "金蝶社区"),
	))
	existingUID := "user-existing"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-existing",
		Name:       "既有员工",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &existingUID,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})
	accounts := newYZJT05AccountStore()
	accounts.seed("open-existing", existingUID, yzjT05TenantDefault)
	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err == nil {
		t.Fatal("active 总数为 0 时 Run 应整轮失败，避免空列表把全员判离职")
	}
	latest, _ := runs.Latest(ctx)
	if latest == nil || latest.Status != types.YunzhijiaSyncStatusFailed {
		t.Fatalf("active total=0 应写入 failed 运行记录，实际 %+v", latest)
	}
	if repo.upsertCount() != 0 {
		t.Fatalf("active total=0 时不得 upsert，实际 %d 次", repo.upsertCount())
	}
	if len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("active total=0 时不得调用账号生命周期（尤其不得停用离职）")
	}
	if rowsAfter := repo.snapshotAll(); !reflect.DeepEqual(rowsBefore, rowsAfter) {
		t.Fatalf("active total=0 前后员工表发生变化\n前: %+v\n后: %+v", rowsBefore, rowsAfter)
	}
	usersAfter, membersAfter := accounts.snapshot()
	if !reflect.DeepEqual(usersBefore, usersAfter) ||
		!reflect.DeepEqual(membersBefore, membersAfter) {
		t.Fatalf("active total=0 不得写用户/成员关系\n前用户: %+v 后用户: %+v\n前成员: %+v 后成员: %+v",
			usersBefore, usersAfter, membersBefore, membersAfter)
	}
	if uw, mw := accounts.writes(); uw != 0 || mw != 0 {
		t.Fatalf("active total=0 不得写用户/成员关系，实际 user=%d member=%d", uw, mw)
	}
}

// ---------------------------------------------------------------------------
// 预检（AC-8 / §10）
// ---------------------------------------------------------------------------

func TestYunzhijiaT05PreviewDoesNotWriteAndReportsDistribution(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, false) // enabled=false 仍可预检
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 3,
		yzjT05Employee("open-a", "员工甲", "人力资源部"),
		yzjT05Employee("open-b", "员工乙", "金蝶社区"),
		yzjT05Employee("open-c", "员工丙", ""),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-d", "员工丁", "金蝶社区"),
	))
	// 预检前预置员工行与本地账号，断言前后完全不变化。
	existingUID := "user-existing"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-existing",
		Name:       "既有员工",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &existingUID,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})
	accounts := newYZJT05AccountStore()
	accounts.seed("open-existing", existingUID, yzjT05TenantDefault)
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.accounts = accounts
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	rowsBefore := repo.snapshotAll()
	usersBefore, membersBefore := accounts.snapshot()

	res, err := svc.Preview(ctx)
	if err != nil {
		t.Fatalf("enabled=false 时预检应可用: %v", err)
	}
	if res == nil {
		t.Fatal("Preview 返回 nil 结果")
	}
	if res.ActiveCount != 3 || res.InactiveCount != 1 {
		t.Fatalf("预检计数 active=%d inactive=%d，期望 3/1", res.ActiveCount, res.InactiveCount)
	}
	if res.TenantHRCount != 1 || res.TenantDefaultCount != 2 {
		t.Fatalf("预检空间分布 hr=%d default=%d，期望 1/2",
			res.TenantHRCount, res.TenantDefaultCount)
	}
	if !yzjT05HasAnomaly(res.Anomalies, types.YunzhijiaAnomalyUnknownDepartment, "open-c") {
		t.Fatalf("预检异常清单应含空部门，实际 %+v", res.Anomalies)
	}

	if repo.upsertCount() != 0 {
		t.Fatalf("预检不得写员工记录，实际 upsert %d 次", repo.upsertCount())
	}
	if len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("预检不得调用账号生命周期")
	}
	if rowsAfter := repo.snapshotAll(); !reflect.DeepEqual(rowsBefore, rowsAfter) {
		t.Fatalf("预检前后员工表发生变化\n前: %+v\n后: %+v", rowsBefore, rowsAfter)
	}
	usersAfter, membersAfter := accounts.snapshot()
	if !reflect.DeepEqual(usersBefore, usersAfter) {
		t.Fatalf("预检前后用户表发生变化\n前: %+v\n后: %+v", usersBefore, usersAfter)
	}
	if !reflect.DeepEqual(membersBefore, membersAfter) {
		t.Fatalf("预检前后成员关系发生变化\n前: %+v\n后: %+v", membersBefore, membersAfter)
	}
	if uw, mw := accounts.writes(); uw != 0 || mw != 0 {
		t.Fatalf("预检不得写用户/成员关系，实际 user=%d member=%d", uw, mw)
	}

	if runs.createCount() != 1 {
		t.Fatalf("预检应写 1 条运行记录，实际 %d", runs.createCount())
	}
	allRuns := runs.allRuns()
	if len(allRuns) != 1 {
		t.Fatalf("预检只应写 1 条运行记录，实际 %d", len(allRuns))
	}
	if allRuns[0].Trigger != types.YunzhijiaSyncTriggerPreview {
		t.Fatalf("预检写入的运行记录 trigger=%q，期望 preview", allRuns[0].Trigger)
	}
	preview := runs.latestPreview()
	if preview == nil {
		t.Fatal("预检应写入 trigger=preview 运行记录")
	}
	if preview.Trigger != types.YunzhijiaSyncTriggerPreview {
		t.Fatalf("预检运行记录 trigger=%q，期望 %q",
			preview.Trigger, types.YunzhijiaSyncTriggerPreview)
	}
	if preview.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("预检成功后运行记录状态=%s", preview.Status)
	}
	if preview.ActiveCount != 3 || preview.InactiveCount != 1 {
		t.Fatalf("预检运行记录计数 active=%d inactive=%d，期望 3/1",
			preview.ActiveCount, preview.InactiveCount)
	}
	if !yzjT05HasAnomaly(
		yzjT05RunAnomalies(t, preview), types.YunzhijiaAnomalyUnknownDepartment, "open-c",
	) {
		t.Fatal("预检运行记录应保存异常 JSON")
	}

	latest, _ := runs.Latest(ctx)
	if latest != nil {
		t.Fatalf("Latest 不应返回预检记录，实际 trigger=%s", latest.Trigger)
	}
}

func TestYunzhijiaT05LatestExcludesPreviewAfterRealRun(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err != nil {
		t.Fatalf("首次手动同步失败: %v", err)
	}
	if _, err := svc.Preview(ctx); err != nil {
		t.Fatalf("预检失败: %v", err)
	}

	latest, err := runs.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("Latest 返回 nil：已有真实同步记录")
	}
	if latest.Trigger != types.YunzhijiaSyncTriggerManual {
		t.Fatalf("Latest 返回 trigger=%q，预检不得覆盖真实同步状态", latest.Trigger)
	}
}

func TestYunzhijiaT05PreviewFailsOnUpstreamError(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setErr(yunzhijia.StatusActive, 1, yunzhijia.ErrUpstream)
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Preview(ctx); err == nil {
		t.Fatal("预检抓取失败应返回错误")
	}
	if repo.upsertCount() != 0 {
		t.Fatalf("预检失败不得写员工记录，实际 %d 次", repo.upsertCount())
	}
	if len(life.employeeSnapshot()) != 0 || len(life.inactiveSnapshot()) != 0 {
		t.Fatal("预检失败不得调用账号生命周期")
	}
	preview := runs.latestPreview()
	if preview == nil {
		t.Fatal("预检失败也应写入 trigger=preview 运行记录")
	}
	if preview.Status != types.YunzhijiaSyncStatusFailed {
		t.Fatalf("预检失败运行记录状态=%s，期望 failed", preview.Status)
	}
}

// ---------------------------------------------------------------------------
// upsert 后用库内行 / blocked / 封禁 / 关联用户缺失 / 异常（AC-10、AC-13）
// ---------------------------------------------------------------------------

func TestYunzhijiaT05RunUsesPersistedRowForBlockedSkip(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "新名", "人力资源部"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	blockedUID := "user-blocked"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-a",
		Name:       "旧名",
		Department: "旧部门",
		TenantID:   yzjT05TenantDefault,
		UserID:     &blockedUID,
		Status:     types.YunzhijiaEmployeeStatusActive,
		Blocked:    true,
	})
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("blocked 跳过不应整轮失败，状态=%s", run.Status)
	}
	if len(life.employeeSnapshot()) != 0 {
		t.Fatal("blocked 员工不应进入账号生命周期（必须用库内行判断）")
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyBlockedSkipped, "open-a") {
		t.Fatalf("应记录 blocked_skipped 异常，实际 %+v", anomalies)
	}

	row := repo.snapshot("open-a")
	if row == nil {
		t.Fatal("blocked 员工记录应仍存在")
	}
	if !row.Blocked {
		t.Fatal("同步不得清除 blocked")
	}
	if row.UserID == nil || *row.UserID != blockedUID {
		t.Fatalf("同步不得改写 user_id，实际 %v", row.UserID)
	}
	// F5：blocked 行在 upsert 之前整条跳过，同步字段不得被覆盖。
	if row.Name != "旧名" || row.Department != "旧部门" {
		t.Fatalf("F5：blocked 行不得 upsert，实际 name=%q department=%q",
			row.Name, row.Department)
	}
	if got := repo.upsertCountFor("open-a"); got != 0 {
		t.Fatalf("F5：blocked 员工不得调用 UpsertByOpenID，实际 %d 次", got)
	}
}

// F4（AC-20）：Run 开始日志只允许 trigger + run id，不得出现员工姓名、
// open_id/名单或 ticket。
func TestYunzhijiaR4RunStartLogIsFieldWhitelisted(t *testing.T) {
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetLogLevel(logger.LevelInfo)
	t.Cleanup(func() { logger.ConfigureFromEnv() })

	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-log-f4-0001", "占位员工日志甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, newYZJT05EmployeeRepo(), runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "同步开始") {
		t.Fatalf("F4：必须记录同步开始日志，实际 %q", out)
	}
	if !strings.Contains(out, "trigger=manual") {
		t.Fatalf("F4：开始日志必须含触发方式，实际 %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("run=%d", run.ID)) {
		t.Fatalf("F4：开始日志必须含 run id=%d，实际 %q", run.ID, out)
	}
	for _, forbidden := range []string{"占位员工日志甲", "open-log-f4-0001", "ticket"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("F4：日志不得包含 %q，实际 %q", forbidden, out)
		}
	}
}

func TestYunzhijiaT05RunPassesPersistedLocalStateToLifecycle(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "人力资源部"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	uid := "user-1"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:         "open-a",
		Name:           "旧名",
		Department:     "旧部门",
		TenantID:       yzjT05TenantDefault,
		UserID:         &uid,
		Status:         types.YunzhijiaEmployeeStatusInactive,
		DisabledBySync: true,
	})
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err != nil {
		t.Fatalf("Run: %v", err)
	}
	call, ok := life.employeeCall("open-a")
	if !ok {
		t.Fatal("在职员工应调用 ApplyEmployee")
	}
	if call.UserID == nil || *call.UserID != uid {
		t.Fatalf("生命周期应收到库内 user_id=%q，实际 %v（不能用入参结构体）", uid, call.UserID)
	}
	if !call.DisabledBySync {
		t.Fatal("生命周期应收到库内 disabled_by_sync=true，不能用来参结构体覆盖")
	}
	if call.Status != types.YunzhijiaEmployeeStatusActive {
		t.Fatalf("生命周期收到 status=%s，期望 active", call.Status)
	}
	if call.TenantID != yzjT05TenantHR {
		t.Fatalf("生命周期收到 tenant_id=%d，期望人力空间 %d", call.TenantID, yzjT05TenantHR)
	}
}

func TestYunzhijiaT05InactiveReconciliationCallsApplyInactive(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-d", "员工丁", "金蝶社区"),
	))

	uid := "user-d"
	repo := newYZJT05EmployeeRepo(&types.YunzhijiaEmployee{
		OpenID:     "open-d",
		Name:       "员工丁",
		Department: "金蝶社区",
		TenantID:   yzjT05TenantDefault,
		UserID:     &uid,
		Status:     types.YunzhijiaEmployeeStatusActive,
	})
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	if _, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(life.employeeSnapshot()) != 1 || life.employeeSnapshot()[0].OpenID != "open-a" {
		t.Fatalf("ApplyEmployee 应只处理在职列表，实际 %+v", life.employeeSnapshot())
	}
	inactive := life.inactiveSnapshot()
	if len(inactive) != 1 || inactive[0].OpenID != "open-d" {
		t.Fatalf("ApplyInactive 应处理离职列表，实际 %+v", inactive)
	}
	if inactive[0].UserID == nil || *inactive[0].UserID != uid {
		t.Fatalf("ApplyInactive 应收到库内 user_id=%q，实际 %v", uid, inactive[0].UserID)
	}
	if inactive[0].Status != types.YunzhijiaEmployeeStatusInactive {
		t.Fatalf("ApplyInactive 收到 status=%s，期望 inactive", inactive[0].Status)
	}

	row := repo.snapshot("open-d")
	if row == nil || row.Status != types.YunzhijiaEmployeeStatusInactive {
		t.Fatalf("离职员工记录应更新为 inactive，实际 %+v", row)
	}
	if row.UserID == nil || *row.UserID != uid {
		t.Fatalf("离职收敛不得丢 user_id，实际 %v", row.UserID)
	}
}

func TestYunzhijiaT05RunRecordsAnomaliesForBannedAndLocalUserMissing(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 3,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
		yzjT05Employee("open-b", "员工乙", "金蝶社区"),
		yzjT05Employee("open-c", "员工丙", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))

	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.setEmployeeErr("open-a", ErrYunzhijiaAccountBanned)
	life.setEmployeeErr("open-b", ErrYunzhijiaLocalUserMissing)
	life.setEmployeeErr("open-c", errors.New("开号失败占位"))
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("单条账号异常不应整轮失败: %v", err)
	}
	if run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("单条异常应继续其他人，状态=%s", run.Status)
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyBannedSkipped, "open-a") {
		t.Fatalf("应记录 banned_skipped，实际 %+v", anomalies)
	}
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyLocalUserMissing, "open-b") {
		t.Fatalf("应记录 local_user_missing，实际 %+v", anomalies)
	}
	if !yzjT05HasAnomaly(anomalies, types.YunzhijiaAnomalyProvisionFailed, "open-c") {
		t.Fatalf("应记录 provision_failed，实际 %+v", anomalies)
	}
	if len(life.employeeSnapshot()) != 3 {
		t.Fatalf("三人各应调用一次 ApplyEmployee，实际 %d", len(life.employeeSnapshot()))
	}
	if repo.upsertCount() != 3 {
		t.Fatalf("单条失败也应在 upsert 后继续，实际 upsert %d 次", repo.upsertCount())
	}
}

func TestYunzhijiaT05RunDuplicateNameAnomaly(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 2,
		yzjT05Employee("open-a", "同名员工", "金蝶社区"),
		yzjT05Employee("open-b", "同名员工", "人力资源部"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("同名只记异常，不应整轮失败: %v", err)
	}
	anomalies := yzjT05RunAnomalies(t, run)
	if got := yzjT05AnomalyCount(anomalies, types.YunzhijiaAnomalyDuplicateName, ""); got != 1 {
		t.Fatalf("duplicate_name 异常 %d 条，期望 1；实际 %+v", got, anomalies)
	}
	foundName := false
	for _, a := range anomalies {
		if a.Kind == types.YunzhijiaAnomalyDuplicateName && a.Name == "同名员工" {
			foundName = true
		}
	}
	if !foundName {
		t.Fatalf("duplicate_name 异常应带姓名，实际 %+v", anomalies)
	}
	if len(life.employeeSnapshot()) != 2 {
		t.Fatalf("同名不阻止同步，两人都应处理，实际 %d", len(life.employeeSnapshot()))
	}
}

// ---------------------------------------------------------------------------
// enabled / 单运行互斥（AC-8）
// ---------------------------------------------------------------------------

func TestYunzhijiaT05ManualRunAllowedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, false)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("enabled=false 时手动同步应可用: %v", err)
	}
	if run.Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("状态=%s，期望 success", run.Status)
	}
}

func TestYunzhijiaT05RunBlocksConcurrentPreview(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	client.block = make(chan struct{})
	client.entered = make(chan struct{})
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Run 未进入抓取阶段（可能未获取单运行锁）")
	}

	second := make(chan error, 1)
	go func() {
		_, err := svc.Preview(ctx)
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, ErrSyncRunning) {
			t.Fatalf("并发 Preview 应返回 ErrSyncRunning，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		close(client.block)
		t.Fatal("并发 Preview 阻塞未返回，单运行互斥未生效")
	}

	close(client.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首个 Run 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Run 未结束")
	}
}

func TestYunzhijiaT05PreviewBlocksConcurrentRun(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	client.block = make(chan struct{})
	client.entered = make(chan struct{})
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Preview(ctx)
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Preview 未进入抓取阶段（可能未获取单运行锁）")
	}

	second := make(chan error, 1)
	go func() {
		_, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, ErrSyncRunning) {
			t.Fatalf("并发 Run 应返回 ErrSyncRunning，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		close(client.block)
		t.Fatal("并发 Run 阻塞未返回，单运行互斥未生效")
	}

	close(client.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首个 Preview 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Preview 未结束")
	}
}

func TestYunzhijiaT05RunBlocksConcurrentRun(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-a", "员工甲", "金蝶社区"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	client.block = make(chan struct{})
	client.entered = make(chan struct{})
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Run 未进入抓取阶段（可能未获取单运行锁）")
	}

	second := make(chan error, 1)
	go func() {
		_, err := svc.Run(ctx, types.YunzhijiaSyncTriggerCron)
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, ErrSyncRunning) {
			t.Fatalf("并发 Run 应返回 ErrSyncRunning，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		close(client.block)
		t.Fatal("并发 Run 阻塞未返回，单运行互斥未生效")
	}

	close(client.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首个 Run 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("首个 Run 未结束")
	}
}

// T08 补充：Run 必须把生命周期返回的动作换算成 AC-14 计数。
// 3 名在职分别 created/updated/migrated，1 名离职 disabled，none 不计。
func TestYunzhijiaT05RunCountsLifecycleResults(t *testing.T) {
	ctx := context.Background()
	cfg := yzjT05Config(10, true)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1, yzjT05Page(1, 10, 3,
		yzjT05Employee("open-created", "占位员工甲", "金蝶社区"),
		yzjT05Employee("open-updated", "占位员工乙", "金蝶社区"),
		yzjT05Employee("open-migrated", "占位员工丙", "人力资源部"),
	))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 1,
		yzjT05Employee("open-disabled", "占位员工丁", "金蝶社区"),
	))
	repo := newYZJT05EmployeeRepo()
	runs := &yzjT05RunRepo{}
	life := newYZJT05Lifecycle()
	life.setEmployeeResult("open-created", types.YunzhijiaAccountResultCreated)
	life.setEmployeeResult("open-updated", types.YunzhijiaAccountResultUpdated)
	life.setEmployeeResult("open-migrated", types.YunzhijiaAccountResultMigrated)
	life.setInactiveResult("open-disabled", types.YunzhijiaAccountResultDisabled)
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)

	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if run.CreatedCount != 1 || run.UpdatedCount != 1 ||
		run.DisabledCount != 1 || run.MigratedCount != 1 {
		t.Fatalf("计数 created=%d updated=%d disabled=%d migrated=%d，期望 1/1/1/1",
			run.CreatedCount, run.UpdatedCount, run.DisabledCount, run.MigratedCount)
	}
}
