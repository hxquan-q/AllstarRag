package service

// 云之家员工同步调度：只负责按时触发 YunzhijiaSyncService.Run，
// 不做抓取/收敛；手动同步与预检由管理端直接调用服务，不经过这里。
// 自动触发只在 cfg.Enabled=true 时注册，enabled=false 时 Start 直接返回。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// yunzhijiaStartupLookback 是启动补跑判定回看的运行记录条数。
	// ListRecent 只提供条数上限，取足够大的窗口以找到最近一次成功记录；
	// 窗口内都是失败时按「从未成功」补跑，多跑一次是安全方向。
	yunzhijiaStartupLookback = 100
	// yunzhijiaShutdownTimeout 是 ctx 取消后等待在途同步退出的上限。
	yunzhijiaShutdownTimeout = 30 * time.Second
)

// YunzhijiaScheduler 注册 cron 并触发启动补跑。
//
// now 可在包内测试中替换，用来计算补跑阈值与下次执行时间；生产默认 time.Now。
// 时区来自配置（默认 Asia/Shanghai），不依赖 time.Local。
type YunzhijiaScheduler struct {
	cfg  *config.YunzhijiaSSOConfig
	sync *YunzhijiaSyncService
	runs interfaces.YunzhijiaSyncRunRepository
	now  func() time.Time

	mu      sync.Mutex
	started bool
	cron    *cron.Cron
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewYunzhijiaScheduler 构造同步调度器。
func NewYunzhijiaScheduler(
	cfg *config.YunzhijiaSSOConfig,
	syncSvc *YunzhijiaSyncService,
	runs interfaces.YunzhijiaSyncRunRepository,
) *YunzhijiaScheduler {
	return &YunzhijiaScheduler{
		cfg:  cfg,
		sync: syncSvc,
		runs: runs,
		now:  time.Now,
	}
}

// NextRun 计算 from 之后的下一次执行时间，结果带配置时区。
// 时间计算与 cron 注册共用同一份表达式解析，便于测试直接断言 03:00。
func (s *YunzhijiaScheduler) NextRun(from time.Time) (time.Time, error) {
	schedule, loc, err := s.parseSchedule()
	if err != nil {
		return time.Time{}, err
	}
	// 先转到配置时区再计算，避免调用方传入 UTC/其他时区时按该时区的 03:00 计算。
	return schedule.Next(from.In(loc)).In(loc), nil
}

// parseSchedule 读取并校验 cron 表达式与时区；非法时 fail-closed。
func (s *YunzhijiaScheduler) parseSchedule() (cron.Schedule, *time.Location, error) {
	if s.cfg == nil {
		return nil, nil, errors.New("yunzhijia: 配置段缺失")
	}
	timezone := strings.TrimSpace(s.cfg.Timezone)
	if timezone == "" {
		return nil, nil, errors.New("yunzhijia: timezone 为空")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("yunzhijia: timezone 非法: %w", err)
	}
	expr := strings.TrimSpace(s.cfg.SyncCron)
	if expr == "" {
		return nil, nil, errors.New("yunzhijia: sync_cron 为空")
	}
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, nil, fmt.Errorf("yunzhijia: sync_cron 非法: %w", err)
	}
	return schedule, loc, nil
}

// Start 仅在 enabled=true 时注册 cron，并在后台判定是否需要启动补跑。
// 补跑与同步失败只记日志，不阻塞、不中断服务启动。
func (s *YunzhijiaScheduler) Start(ctx context.Context) {
	if s.cfg == nil || !s.cfg.Enabled {
		logger.Debugf(ctx, "[Yunzhijia] 调度未启用（enabled=false）")
		return
	}
	schedule, loc, err := s.parseSchedule()
	if err != nil {
		logger.Errorf(ctx, "[Yunzhijia] 调度未启动: %v", err)
		return
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c := cron.New(cron.WithLocation(loc))
	c.Schedule(schedule, cron.FuncJob(func() {
		s.trigger(runCtx, types.YunzhijiaSyncTriggerCron)
	}))
	s.started = true
	s.cron = c
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	c.Start()
	if next, nextErr := s.NextRun(s.now()); nextErr == nil {
		logger.Infof(ctx, "[Yunzhijia] 调度已启动，下次执行: %s", next.Format(time.RFC3339))
	}

	go func() {
		defer s.wg.Done()
		s.startupCatchup(ctx, runCtx)
	}()
	go func() {
		<-ctx.Done()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), yunzhijiaShutdownTimeout)
		defer stopCancel()
		s.Stop(stopCtx)
	}()
}

// Stop 停止 cron 并等待在途任务退出；ctx 到期时取消在途同步后返回。
// 可重复调用；未启动或 enabled=false 时为空操作。
func (s *YunzhijiaScheduler) Stop(ctx context.Context) {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	c := s.cron
	cancel := s.cancel
	s.cron = nil
	s.cancel = nil
	s.mu.Unlock()

	if c != nil {
		c.Stop()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return
	}
	if cancel != nil {
		cancel()
	}
}

// startupCatchup 在启动时判定并触发一次补跑；查询失败只记日志。
func (s *YunzhijiaScheduler) startupCatchup(ctx, runCtx context.Context) {
	need, last, err := s.shouldCatchUp(ctx)
	if err != nil {
		logger.Warnf(ctx, "[Yunzhijia] 启动补跑判定失败: %v", err)
		return
	}
	if !need {
		logger.Infof(ctx, "[Yunzhijia] 启动补跑跳过：最近成功=%s", formatYunzhijiaRunTime(last))
		return
	}
	logger.Infof(ctx, "[Yunzhijia] 启动补跑触发：最近成功=%s", formatYunzhijiaRunTime(last))
	s.trigger(runCtx, types.YunzhijiaSyncTriggerStartup)
}

// shouldCatchUp 按 AC-8 判定：从未成功必补；配置小时数为 0 时只补首次；
// 大于 0 时距上次成功超过该小时数才补。
func (s *YunzhijiaScheduler) shouldCatchUp(
	ctx context.Context,
) (bool, *types.YunzhijiaSyncRun, error) {
	last, err := s.latestSuccess(ctx)
	if err != nil {
		return false, nil, err
	}
	if last == nil {
		return true, nil, nil
	}
	hours := s.cfg.StartupCatchupHours
	if hours <= 0 {
		return false, last, nil
	}
	since := last.StartedAt
	if last.FinishedAt != nil {
		since = *last.FinishedAt
	}
	return s.now().Sub(since) > time.Duration(hours)*time.Hour, last, nil
}

// latestSuccess 取最近一条成功记录；预检记录不算。
func (s *YunzhijiaScheduler) latestSuccess(
	ctx context.Context,
) (*types.YunzhijiaSyncRun, error) {
	runs, err := s.runs.ListRecent(ctx, yunzhijiaStartupLookback)
	if err != nil {
		return nil, fmt.Errorf("yunzhijia: 读取同步运行记录失败: %w", err)
	}
	for _, run := range runs {
		if run.Trigger == types.YunzhijiaSyncTriggerPreview {
			continue
		}
		if run.Status == types.YunzhijiaSyncStatusSuccess {
			return run, nil
		}
	}
	return nil, nil
}

// trigger 异步启动一轮同步；与手动/预检的互斥由 Run 自己保证。
// 已停止后不再接受新任务，避免 Stop 与 WaitGroup 复用冲突。
func (s *YunzhijiaScheduler) trigger(ctx context.Context, trigger types.YunzhijiaSyncTrigger) {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		run, err := s.sync.Run(ctx, trigger)
		switch {
		case errors.Is(err, ErrSyncRunning):
			logger.Infof(ctx, "[Yunzhijia] 同步跳过：已有任务运行中（trigger=%s）", trigger)
		case err != nil:
			logger.Warnf(ctx, "[Yunzhijia] 同步失败（trigger=%s）: %v", trigger, err)
		default:
			// AC-20：完成日志带计数与耗时，排障不必再翻运行记录。
			logger.Infof(ctx,
				"[Yunzhijia] 同步完成（trigger=%s, run=%d）：active=%d inactive=%d created=%d "+
					"updated=%d disabled=%d migrated=%d anomalies=%d 耗时=%s",
				trigger, run.ID, run.ActiveCount, run.InactiveCount, run.CreatedCount,
				run.UpdatedCount, run.DisabledCount, run.MigratedCount, run.AnomalyCount,
				yunzhijiaRunDuration(run))
		}
	}()
}

// yunzhijiaRunDuration 返回运行记录的实际耗时；running 记录返回 0。
func yunzhijiaRunDuration(run *types.YunzhijiaSyncRun) time.Duration {
	if run == nil || run.FinishedAt == nil {
		return 0
	}
	return run.FinishedAt.Sub(run.StartedAt)
}

// formatYunzhijiaRunTime 只输出时间，不输出员工字段。
func formatYunzhijiaRunTime(run *types.YunzhijiaSyncRun) string {
	if run == nil {
		return "无"
	}
	at := run.StartedAt
	if run.FinishedAt != nil {
		at = *run.FinishedAt
	}
	return at.Format(time.RFC3339)
}
