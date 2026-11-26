package service

// T07 补测（非 TDD，实现后补齐）：云之家同步调度器。
//
// 覆盖 AC-8（03:00 Asia/Shanghai、启动补跑、不阻塞启动）、
// AC-19（cron/timezone 非法 fail-closed）、AC-20（日志脱敏）。
//
// 用例复用 T05 的同包替身（yzjT05Client / yzjT05RunRepo / yzjT05Lifecycle），
// 不新增生产代码、不使用真实员工数据；时间一律用固定时钟或条件轮询，
// 不 sleep 真实的同步周期。

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
)

// yzjT07FixedNow 是调度用例统一使用的“当前时间”（北京时间正午）。
func yzjT07FixedNow(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载 Asia/Shanghai 失败: %v", err)
	}
	return time.Date(2026, 11, 24, 12, 0, 0, 0, loc)
}

// yzjT07Config 基于 T05 的合法基础配置派生 T07 场景配置。
func yzjT07Config(enabled bool, hours int) *config.YunzhijiaSSOConfig {
	cfg := yzjT05Config(10, enabled)
	cfg.StartupCatchupHours = hours
	return cfg
}

// yzjT07NewScheduler 组装真实 SyncService + 同一批测试替身；
// 上游默认给 1 名在职员工（active 总数为 0 会被抓取阶段整轮拒绝）。
func yzjT07NewScheduler(
	cfg *config.YunzhijiaSSOConfig,
) (*YunzhijiaScheduler, *YunzhijiaSyncService, *yzjT05Client, *yzjT05RunRepo) {
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1,
		yzjT05Page(1, 10, 1, yzjT05Employee("open-sched", "调度占位员工", "金蝶社区")))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	runs := &yzjT05RunRepo{}
	repo := newYZJT05EmployeeRepo()
	life := newYZJT05Lifecycle()
	svc := NewYunzhijiaSyncService(cfg, client, repo, runs, life)
	return NewYunzhijiaScheduler(cfg, svc, runs), svc, client, runs
}

// yzjT07SeedSuccess 写入一条历史成功运行记录。
func yzjT07SeedSuccess(
	t *testing.T,
	runs *yzjT05RunRepo,
	trigger types.YunzhijiaSyncTrigger,
	started time.Time,
	finished *time.Time,
) {
	t.Helper()
	ctx := context.Background()
	run := &types.YunzhijiaSyncRun{Trigger: trigger, StartedAt: started}
	if err := runs.CreateRunning(ctx, run); err != nil {
		t.Fatalf("写入历史运行记录失败: %v", err)
	}
	run.Status = types.YunzhijiaSyncStatusSuccess
	run.FinishedAt = finished
	if err := runs.Finish(ctx, run); err != nil {
		t.Fatalf("结束历史运行记录失败: %v", err)
	}
}

func yzjT07Started(s *YunzhijiaScheduler) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// yzjT07HasCron 返回是否已注册 cron。
func yzjT07HasCron(s *YunzhijiaScheduler) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cron != nil
}

// yzjT07WaitFor 用条件轮询替代真实等待；超时即失败。
func yzjT07WaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时（3s）：%s", what)
}

// YunzhijiaSSOConfig 的 timezone/cron 校验在 parseSchedule 内部完成；
// NextRun 直接复用同一解析，断言失败即 fail-closed。
func TestYunzhijiaT07NextRunAsiaShanghai(t *testing.T) {
	cfg := yzjT07Config(true, 24)
	s, _, _, _ := yzjT07NewScheduler(cfg)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载 Asia/Shanghai 失败: %v", err)
	}

	cases := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			name: "凌晨两点后的当次三点",
			from: time.Date(2026, 11, 24, 2, 0, 0, 0, loc),
			want: time.Date(2026, 11, 24, 3, 0, 0, 0, loc),
		},
		{
			name: "恰好三点后顺延到次日",
			from: time.Date(2026, 11, 24, 3, 0, 0, 0, loc),
			want: time.Date(2026, 11, 25, 3, 0, 0, 0, loc),
		},
		{
			name: "三点过 1 毫秒也顺延",
			from: time.Date(2026, 11, 24, 3, 0, 0, 1, loc),
			want: time.Date(2026, 11, 25, 3, 0, 0, 0, loc),
		},
		{
			name: "传入东九区时间按北京时间折算",
			from: time.Date(2026, 11, 24, 23, 30, 0, 0, time.FixedZone("UTC+9", 9*3600)),
			want: time.Date(2026, 11, 25, 3, 0, 0, 0, loc),
		},
		{
			name: "传入 UTC 时间按北京时间折算",
			from: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 25, 3, 0, 0, 0, loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.NextRun(tc.from)
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("NextRun = %s，期望 %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
			if got.Location().String() != "Asia/Shanghai" {
				t.Fatalf("NextRun 时区 = %s，期望 Asia/Shanghai", got.Location().String())
			}
			if got.Hour() != 3 || got.Minute() != 0 || got.Second() != 0 {
				t.Fatalf("NextRun = %s，期望每天 03:00:00", got.Format(time.RFC3339))
			}
		})
	}
}

func TestYunzhijiaT07NextRunInvalidConfigFailsClosed(t *testing.T) {
	now := yzjT07FixedNow(t)
	base := yzjT07Config(true, 24)

	clone := func(mut func(*config.YunzhijiaSSOConfig)) *config.YunzhijiaSSOConfig {
		cp := *base
		mut(&cp)
		return &cp
	}

	cases := []struct {
		name string
		s    *YunzhijiaScheduler
	}{
		{name: "配置段缺失", s: NewYunzhijiaScheduler(nil, nil, nil)},
		{name: "时区为空", s: NewYunzhijiaScheduler(clone(func(c *config.YunzhijiaSSOConfig) {
			c.Timezone = ""
		}), nil, nil)},
		{name: "时区非法", s: NewYunzhijiaScheduler(clone(func(c *config.YunzhijiaSSOConfig) {
			c.Timezone = "Mars/Olympus"
		}), nil, nil)},
		{name: "cron 为空", s: NewYunzhijiaScheduler(clone(func(c *config.YunzhijiaSSOConfig) {
			c.SyncCron = ""
		}), nil, nil)},
		{name: "cron 非法", s: NewYunzhijiaScheduler(clone(func(c *config.YunzhijiaSSOConfig) {
			c.SyncCron = "every day at 3"
		}), nil, nil)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.s.NextRun(now)
			if err == nil {
				t.Fatalf("非法配置 NextRun 应返回错误，实际得到 %s", got)
			}
			if !got.IsZero() {
				t.Fatalf("非法配置 NextRun 应返回零值，实际 %s", got)
			}
		})
	}
}

// shouldCatchUp 是启动补跑判定的可测核心：0=仅首次，>0=超过阈值才补。
func TestYunzhijiaT07ShouldCatchUpDecision(t *testing.T) {
	now := yzjT07FixedNow(t)
	hours := func(h int) time.Duration { return time.Duration(h) * time.Hour }

	cases := []struct {
		name      string
		hours     int
		seed      func(runs *yzjT05RunRepo)
		wantNeed  bool
		wantErr   bool
		wantLastN bool
	}{
		{
			name:     "从未成功必补",
			hours:    0,
			wantNeed: true,
		},
		{
			name:  "只有失败记录按从未成功处理",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				ctx := context.Background()
				run := &types.YunzhijiaSyncRun{
					Trigger:   types.YunzhijiaSyncTriggerCron,
					StartedAt: now.Add(-time.Hour),
				}
				if err := runs.CreateRunning(ctx, run); err != nil {
					t.Fatalf("seed: %v", err)
				}
				run.Status = types.YunzhijiaSyncStatusFailed
				if err := runs.Finish(ctx, run); err != nil {
					t.Fatalf("seed finish: %v", err)
				}
			},
			wantNeed: true,
		},
		{
			name:  "预检成功不算成功同步",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				finished := now.Add(-time.Minute)
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerPreview,
					now.Add(-time.Hour), &finished)
			},
			wantNeed: true,
		},
		{
			name:  "阈值 0 时已有成功不补",
			hours: 0,
			seed: func(runs *yzjT05RunRepo) {
				finished := now.Add(-100 * time.Hour)
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
					now.Add(-101*time.Hour), &finished)
			},
			wantNeed:  false,
			wantLastN: true,
		},
		{
			name:  "阈值内不补",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				finished := now.Add(-hours(23))
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
					now.Add(-hours(24)), &finished)
			},
			wantNeed:  false,
			wantLastN: true,
		},
		{
			name:  "恰好达到阈值不补（严格大于才补）",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				finished := now.Add(-hours(24))
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
					now.Add(-hours(25)), &finished)
			},
			wantNeed:  false,
			wantLastN: true,
		},
		{
			name:  "超过阈值补跑",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				finished := now.Add(-hours(25))
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
					now.Add(-hours(26)), &finished)
			},
			wantNeed:  true,
			wantLastN: true,
		},
		{
			name:  "无结束时间时用开始时间判断",
			hours: 24,
			seed: func(runs *yzjT05RunRepo) {
				yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerStartup,
					now.Add(-hours(25)), nil)
			},
			wantNeed:  true,
			wantLastN: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := yzjT07Config(true, tc.hours)
			runs := &yzjT05RunRepo{}
			if tc.seed != nil {
				tc.seed(runs)
			}
			_, svc, _, _ := yzjT07NewScheduler(cfg)
			s := NewYunzhijiaScheduler(cfg, svc, runs)
			s.now = func() time.Time { return now }

			need, last, err := s.shouldCatchUp(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望返回错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("shouldCatchUp: %v", err)
			}
			if need != tc.wantNeed {
				t.Fatalf("need = %v，期望 %v", need, tc.wantNeed)
			}
			if tc.wantLastN != (last != nil) {
				t.Fatalf("last 是否为 nil = %v，期望 %v", last != nil, tc.wantLastN)
			}
		})
	}
}

// listRecent 失败只记 Warn，不应触发补跑、也不应阻塞启动。
type yzjT07ErrRunRepo struct {
	createCalls int
}

func (r *yzjT07ErrRunRepo) CreateRunning(context.Context, *types.YunzhijiaSyncRun) error {
	r.createCalls++
	return errors.New("运行记录仓储不可用")
}

func (r *yzjT07ErrRunRepo) Finish(context.Context, *types.YunzhijiaSyncRun) error {
	return errors.New("运行记录仓储不可用")
}

func (r *yzjT07ErrRunRepo) Latest(context.Context) (*types.YunzhijiaSyncRun, error) {
	return nil, errors.New("运行记录仓储不可用")
}

func (r *yzjT07ErrRunRepo) ListRecent(context.Context, int) ([]*types.YunzhijiaSyncRun, error) {
	return nil, errors.New("运行记录仓储不可用")
}

func TestYunzhijiaT07ShouldCatchUpRepoErrorDoesNotPanic(t *testing.T) {
	cfg := yzjT07Config(true, 24)
	_, svc, client, _ := yzjT07NewScheduler(cfg)
	runs := &yzjT07ErrRunRepo{}
	s := NewYunzhijiaScheduler(cfg, svc, runs)
	s.now = func() time.Time { return yzjT07FixedNow(t) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	if !yzjT07Started(s) {
		t.Fatal("合法配置 Start 后应处于已启动状态")
	}
	// Stop 会等待启动补跑 goroutine；查询失败时它只记 Warn 后返回。
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	s.Stop(stopCtx)

	if runs.createCalls != 0 || len(client.callSnapshot()) != 0 {
		t.Fatalf("运行记录查询失败时不得触发同步，create=%d clientCalls=%d",
			runs.createCalls, len(client.callSnapshot()))
	}
}

// enabled=false：不注册 cron、不跑启动补跑，但手动同步仍可用。
func TestYunzhijiaT07StartDisabledSkipsScheduling(t *testing.T) {
	cfg := yzjT07Config(false, 24)
	s, svc, client, runs := yzjT07NewScheduler(cfg)
	ctx := context.Background()

	s.Start(ctx)
	if yzjT07Started(s) || yzjT07HasCron(s) {
		t.Fatal("enabled=false 时不得注册 cron/标记启动")
	}
	if runs.createCount() != 0 || len(client.callSnapshot()) != 0 {
		t.Fatalf("enabled=false 时不得跑启动补跑，create=%d clientCalls=%d",
			runs.createCount(), len(client.callSnapshot()))
	}

	// 管理员手动同步不受 enabled 门控影响。
	run, err := svc.Run(ctx, types.YunzhijiaSyncTriggerManual)
	if err != nil {
		t.Fatalf("enabled=false 时手动同步应可用: %v", err)
	}
	if run == nil || run.Trigger != types.YunzhijiaSyncTriggerManual {
		t.Fatalf("手动同步运行记录异常: %+v", run)
	}
	if yzjT07Started(s) || yzjT07HasCron(s) {
		t.Fatal("手动同步不得反向启动调度器")
	}
	s.Stop(ctx)
}

func TestYunzhijiaT07StartInvalidConfigFailsClosed(t *testing.T) {
	base := yzjT07Config(true, 24)
	clone := func(mut func(*config.YunzhijiaSSOConfig)) *config.YunzhijiaSSOConfig {
		cp := *base
		mut(&cp)
		return &cp
	}

	cases := []struct {
		name string
		cfg  *config.YunzhijiaSSOConfig
	}{
		{name: "cron 为空", cfg: clone(func(c *config.YunzhijiaSSOConfig) { c.SyncCron = "" })},
		{name: "cron 非法", cfg: clone(func(c *config.YunzhijiaSSOConfig) { c.SyncCron = "61 * * * *" })},
		{name: "时区为空", cfg: clone(func(c *config.YunzhijiaSSOConfig) { c.Timezone = "" })},
		{name: "时区非法", cfg: clone(func(c *config.YunzhijiaSSOConfig) { c.Timezone = "Mars/Olympus" })},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 用一份独立的合法配置建服务，再把非法配置交给调度器，
			// 模拟“同步服务可用但调度配置坏”的部署形态。
			_, svc, client, runs := yzjT07NewScheduler(yzjT07Config(true, 24))
			s := NewYunzhijiaScheduler(tc.cfg, svc, runs)
			s.Start(context.Background())

			if yzjT07Started(s) || yzjT07HasCron(s) {
				t.Fatal("非法调度配置必须 fail-closed：不得注册 cron/标记启动")
			}
			if runs.createCount() != 0 || len(client.callSnapshot()) != 0 {
				t.Fatalf("非法配置不得触发同步，create=%d clientCalls=%d",
					runs.createCount(), len(client.callSnapshot()))
			}
		})
	}
}

func TestYunzhijiaT07StartRegistersCronAndCatchup(t *testing.T) {
	cfg := yzjT07Config(true, 0)
	s, _, _, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return yzjT07FixedNow(t) }
	ctx := context.Background()

	s.Start(ctx)
	if !yzjT07Started(s) || !yzjT07HasCron(s) {
		t.Fatal("enabled=true 合法配置 Start 后应注册 cron")
	}
	if entries := s.cron.Entries(); len(entries) != 1 {
		t.Fatalf("cron 注册条目数 = %d，期望 1", len(entries))
	}
	// 下次执行与 NextRun 一致（Asia/Shanghai 03:00）。
	next, err := s.NextRun(yzjT07FixedNow(t))
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if next.Hour() != 3 || next.Minute() != 0 {
		t.Fatalf("cron 下次执行 = %s，期望 03:00", next.Format(time.RFC3339))
	}

	yzjT07WaitFor(t, "从未成功时启动补跑应写入并结束运行记录", func() bool {
		return runs.createCount() >= 1 && runs.finishCount() >= 1
	})

	all := runs.allRuns()
	if len(all) != 1 {
		t.Fatalf("启动补跑运行记录数 = %d，期望 1", len(all))
	}
	if all[0].Trigger != types.YunzhijiaSyncTriggerStartup {
		t.Fatalf("启动补跑 trigger = %s，期望 startup", all[0].Trigger)
	}
	if all[0].Status != types.YunzhijiaSyncStatusSuccess {
		t.Fatalf("启动补跑状态 = %s，期望 success（%s）", all[0].Status, all[0].ErrorSummary)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)
	if yzjT07Started(s) || yzjT07HasCron(s) {
		t.Fatal("Stop 后应清空 cron/启动标记")
	}
}

func TestYunzhijiaT07StartCatchupWhenStale(t *testing.T) {
	now := yzjT07FixedNow(t)
	cfg := yzjT07Config(true, 24)
	s, _, _, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return now }

	finished := now.Add(-25 * time.Hour)
	yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
		now.Add(-26*time.Hour), &finished)
	before := runs.createCount()

	ctx := context.Background()
	s.Start(ctx)
	yzjT07WaitFor(t, "上次成功超过 24h 应触发启动补跑", func() bool {
		return runs.createCount() > before && runs.finishCount() >= 2
	})
	all := runs.allRuns()
	if len(all) != 2 {
		t.Fatalf("运行记录数 = %d，期望 2", len(all))
	}
	if all[1].Trigger != types.YunzhijiaSyncTriggerStartup {
		t.Fatalf("新运行 trigger = %s，期望 startup", all[1].Trigger)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)
}

func TestYunzhijiaT07StartSkipsCatchupWhenRecent(t *testing.T) {
	now := yzjT07FixedNow(t)
	cfg := yzjT07Config(true, 24)
	s, _, client, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return now }

	finished := now.Add(-1 * time.Hour)
	yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron,
		now.Add(-2*time.Hour), &finished)
	before := runs.createCount()

	ctx := context.Background()
	s.Start(ctx)
	// Stop 会等待启动补跑 goroutine 结束：判定为“不补”后无任何新运行。
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)

	if runs.createCount() != before {
		t.Fatalf("阈值内不应补跑，运行记录从 %d 增至 %d", before, runs.createCount())
	}
	if len(client.callSnapshot()) != 0 {
		t.Fatalf("阈值内不应抓取员工，client 调用 %d 次", len(client.callSnapshot()))
	}
}

// Start 不阻塞：客户端卡住时 Start 仍应立即返回，补跑在后台进行。
func TestYunzhijiaT07StartupNonBlocking(t *testing.T) {
	cfg := yzjT07Config(true, 0)
	s, _, client, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return yzjT07FixedNow(t) }
	client.block = make(chan struct{})
	client.entered = make(chan struct{})

	ctx := context.Background()
	s.Start(ctx) // 若补跑同步执行，这里会因客户端阻塞而永远不返回。
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		close(client.block)
		t.Fatal("启动补跑未在后台启动（客户端未进入抓取）")
	}

	close(client.block)
	yzjT07WaitFor(t, "解除阻塞后补跑应完成", func() bool {
		return runs.finishCount() >= 1
	})

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)
}

// Stop 等待在途任务退出，不会提前返回丢掉 goroutine。
func TestYunzhijiaT07StopWaitsForInflight(t *testing.T) {
	cfg := yzjT07Config(true, 0)
	s, _, client, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return yzjT07FixedNow(t) }
	client.block = make(chan struct{})
	client.entered = make(chan struct{})

	ctx := context.Background()
	s.Start(ctx)
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		close(client.block)
		t.Fatal("启动补跑未进入抓取")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Stop(context.Background())
	}()
	select {
	case <-done:
		close(client.block)
		t.Fatal("在途同步未完成时 Stop 不应返回")
	case <-time.After(200 * time.Millisecond):
	}

	close(client.block)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 等待在途同步超时")
	}
	if runs.finishCount() < 1 {
		t.Fatal("Stop 返回时在途运行记录应已结束")
	}
	if yzjT07Started(s) || yzjT07HasCron(s) {
		t.Fatal("Stop 后应清空 cron/启动标记")
	}
}

func TestYunzhijiaT07StopPreventsNewTriggerAndIsIdempotent(t *testing.T) {
	now := yzjT07FixedNow(t)
	cfg := yzjT07Config(true, 24)
	s, _, client, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return now }
	finished := now.Add(-1 * time.Hour)
	yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron, now.Add(-2*time.Hour), &finished)
	before := runs.createCount()

	ctx := context.Background()
	s.Start(ctx)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)
	s.Stop(stopCtx) // 重复 Stop 应为空操作。

	// 已停止后任何触发都应被忽略（trigger 同步检查 started 后直接返回）。
	s.trigger(ctx, types.YunzhijiaSyncTriggerManual)

	if runs.createCount() != before {
		t.Fatalf("Stop 后不得再触发同步，运行记录从 %d 增至 %d", before, runs.createCount())
	}
	if len(client.callSnapshot()) != 0 {
		t.Fatalf("Stop 后不得再抓取员工，client 调用 %d 次", len(client.callSnapshot()))
	}
}

func TestYunzhijiaT07ContextCancelStopsScheduler(t *testing.T) {
	now := yzjT07FixedNow(t)
	cfg := yzjT07Config(true, 24)
	s, _, _, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return now }
	finished := now.Add(-1 * time.Hour)
	yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron, now.Add(-2*time.Hour), &finished)
	before := runs.createCount()

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	if !yzjT07Started(s) {
		t.Fatal("Start 后应为已启动")
	}
	cancel()

	yzjT07WaitFor(t, "ctx 取消后调度器应自动 Stop", func() bool {
		return !yzjT07Started(s) && !yzjT07HasCron(s)
	})

	s.trigger(context.Background(), types.YunzhijiaSyncTriggerCron)
	if runs.createCount() != before {
		t.Fatalf("ctx 取消后不得再触发同步，运行记录从 %d 增至 %d", before, runs.createCount())
	}
}

// 运行中触发：调度器把 ErrSyncRunning 当作“跳过”，不产生第二条运行记录。
// 真实并发互斥由 T05 的 Run/Preview 阻塞用例覆盖，这里锁定调度器跳过分支。
func TestYunzhijiaT07RunningTriggerSkips(t *testing.T) {
	now := yzjT07FixedNow(t)
	cfg := yzjT07Config(true, 24)
	s, svc, client, runs := yzjT07NewScheduler(cfg)
	s.now = func() time.Time { return now }
	finished := now.Add(-1 * time.Hour)
	yzjT07SeedSuccess(t, runs, types.YunzhijiaSyncTriggerCron, now.Add(-2*time.Hour), &finished)
	before := runs.createCount()

	ctx := context.Background()
	s.Start(ctx)
	svc.running.Store(true) // 模拟已有一轮同步在途。
	t.Cleanup(func() { svc.running.Store(false) })

	s.trigger(ctx, types.YunzhijiaSyncTriggerCron)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	s.Stop(stopCtx) // 等待被跳过的触发 goroutine 结束。

	if runs.createCount() != before {
		t.Fatalf("运行中触发应跳过，运行记录从 %d 增至 %d", before, runs.createCount())
	}
	if len(client.callSnapshot()) != 0 {
		t.Fatalf("运行中触发不应抓取员工，client 调用 %d 次", len(client.callSnapshot()))
	}
}

// AC-20：调度器日志只含触发方式/运行 id/时间，不含员工身份信息。
func TestYunzhijiaT07LogsExcludeEmployeeIdentity(t *testing.T) {
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetLogLevel(logger.LevelInfo)
	t.Cleanup(func() { logger.ConfigureFromEnv() })

	const employeeName = "敏感员工姓名占位"
	const openID = "open-sensitive-placeholder"

	cfg := yzjT07Config(true, 0)
	client := newYZJT05Client()
	client.setPage(yunzhijia.StatusActive, 1,
		yzjT05Page(1, 10, 1, yzjT05Employee(openID, employeeName, "金蝶社区")))
	client.setPage(yunzhijia.StatusInactive, 1, yzjT05Page(1, 10, 0))
	runs := &yzjT05RunRepo{}
	svc := NewYunzhijiaSyncService(cfg, client, newYZJT05EmployeeRepo(), runs, newYZJT05Lifecycle())
	s := NewYunzhijiaScheduler(cfg, svc, runs)
	s.now = func() time.Time { return yzjT07FixedNow(t) }

	ctx := context.Background()
	s.Start(ctx)
	yzjT07WaitFor(t, "补跑应完成", func() bool { return runs.finishCount() >= 1 })
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Stop(stopCtx)

	out := logs.String()
	if !strings.Contains(out, "[Yunzhijia]") {
		t.Fatalf("未捕获到调度器日志，输出=%q", out)
	}
	if strings.Contains(out, employeeName) {
		t.Fatalf("调度日志不得包含员工姓名: %q", out)
	}
	if strings.Contains(out, openID) {
		t.Fatalf("调度日志不得包含员工 open_id: %q", out)
	}
}
