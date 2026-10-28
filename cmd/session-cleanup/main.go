// session-cleanup 清理客户（viewer）创建的、无法归入其当前绑定产线的存量会话。
//
// 默认行为是 dry-run：只打印待删清单，不删数据、不写审计文件。
// 只有显式 --apply --tenant <id> --confirm <N> 且审计文件不存在时才会删除。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/container"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const sampleLimit = 20

// options 是命令行参数。
type options struct {
	apply    bool
	tenantID uint64
	confirm  int
	auditDir string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}

// parseOptions 解析参数并做互斥/必填校验：
//   - 不传 --apply 即 dry-run；
//   - --apply 必须同时传 --tenant 与 --confirm；
//   - --apply 与 --dry-run 不能同时出现。
func parseOptions(args []string) (*options, error) {
	fs := flag.NewFlagSet("session-cleanup", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "实际执行删除（必须同时给 --tenant 与 --confirm）")
	dryRun := fs.Bool("dry-run", false, "只打印待删清单（默认行为）")
	tenantID := fs.Uint64("tenant", 0, "工作区 id；--apply 必填，dry-run 省略表示扫全部工作区")
	confirm := fs.Int("confirm", -1, "确认删除的会话总数，必须等于本次筛选结果")
	auditDir := fs.String("audit-dir", ".", "审计文件目录")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *apply && *dryRun {
		return nil, errors.New("--apply 与 --dry-run 不能同时使用")
	}
	if *apply && *tenantID == 0 {
		return nil, errors.New("--apply 必须传 --tenant <id>")
	}
	if *apply && *confirm < 0 {
		return nil, errors.New("--apply 必须传 --confirm <N>")
	}
	return &options{
		apply:    *apply,
		tenantID: *tenantID,
		confirm:  *confirm,
		auditDir: *auditDir,
	}, nil
}

func run(args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}

	// 安全闸之一：同工作区已有审计文件时拒绝执行（一次性迁移语义）。
	if opts.apply {
		existing, err := findAuditFile(opts.auditDir, opts.tenantID)
		if err != nil {
			return fmt.Errorf("检查审计文件失败: %w", err)
		}
		if existing != "" {
			return fmt.Errorf("审计文件已存在（%s），拒绝重复执行；确认要重跑请先手工改名", existing)
		}
	}

	ctx := context.Background()
	digContainer := container.BuildContainer(runtime.GetContainer())
	var db *gorm.DB
	if err := digContainer.Invoke(func(g *gorm.DB) { db = g }); err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}

	data, err := loadScanData(ctx, db, opts.tenantID)
	if err != nil {
		return err
	}
	currentPlan := buildPlan(data)
	printPlan(data, currentPlan)

	if !opts.apply {
		printf("\n这是 dry-run，未删除任何数据、未写审计文件。\n")
		return nil
	}

	// 安全闸之二：--confirm 必须等于本次筛选出的总数。
	if err := checkConfirm(opts.confirm, currentPlan.SessionCount()); err != nil {
		return err
	}

	var sessionService interfaces.SessionService
	if err := digContainer.Invoke(func(service interfaces.SessionService) {
		sessionService = service
	}); err != nil {
		return fmt.Errorf("初始化会话服务失败: %w", err)
	}

	printf("\n开始删除 %d 条会话（按 tenant + owner 分组）...\n", currentPlan.SessionCount())
	groupErrs := applyPlan(ctx, currentPlan, sessionService.BatchDeleteSessions)
	deletedIDs, failedIDs, verifyErrs := verifyDeletion(ctx, db, currentPlan.Sessions)
	allErrs := append(append([]string{}, groupErrs...), verifyErrs...)

	record := auditRecord{
		TenantID:                opts.tenantID,
		GeneratedAt:             nowFunc().Format("2006-01-02T15:04:05Z07:00"),
		TotalCandidates:         currentPlan.SessionCount(),
		DeletedCount:            len(deletedIDs),
		FailedCount:             len(failedIDs),
		DeletedSessionIDs:       nonNilStrings(deletedIDs),
		FailedSessionIDs:        nonNilStrings(failedIDs),
		ByUser:                  planByUser(currentPlan),
		ZeroBindingUsers:        zeroBindingUserKeys(currentPlan),
		ZeroBindingSessionCount: currentPlan.ZeroBindingSessionCount,
		Errors:                  allErrs,
	}
	auditPath, err := writeAudit(opts.auditDir, record)
	if err != nil {
		return fmt.Errorf("写审计文件失败: %w（本次已删除 %d 条，请勿直接重跑）", err, len(deletedIDs))
	}

	printf("\n实际删除: %d\n失败: %d\n审计文件: %s\n", len(deletedIDs), len(failedIDs), auditPath)
	for _, message := range allErrs {
		printf("  失败详情: %s\n", message)
	}
	if len(failedIDs) > 0 {
		return fmt.Errorf("有 %d 条会话未能删除，详见审计文件 %s", len(failedIDs), auditPath)
	}
	return nil
}

// printPlan 输出 dry-run / apply 前的清单：
// 总数、零绑定用户数及其会话数、按用户汇总、前 20 条样本。
func printPlan(data *scanData, currentPlan *plan) {
	printf("待删会话总数: %d\n", currentPlan.SessionCount())
	printf("零绑定用户: %d 人，其会话 %d 条\n",
		len(currentPlan.ZeroBindingOwners), currentPlan.ZeroBindingSessionCount)
	printZeroBindingOwners(currentPlan)

	printf("\n按用户汇总（工作区/用户 绑定数 待删数）:\n")
	keys := make([]ownerKey, 0, len(currentPlan.ByOwner))
	for key := range currentPlan.ByOwner {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	if len(keys) == 0 {
		printf("  （无）\n")
	}
	for _, key := range keys {
		printf("  %s 绑定=%d 待删=%d\n",
			key.String(), len(data.Bindings[key]), len(currentPlan.ByOwner[key]))
	}

	printf("\n样本（最多 %d 条）:\n", sampleLimit)
	if len(currentPlan.Sessions) == 0 {
		printf("  （无）\n")
		return
	}
	for index, session := range currentPlan.Sessions {
		if index >= sampleLimit {
			printf("  ... 其余 %d 条省略\n", len(currentPlan.Sessions)-sampleLimit)
			break
		}
		printf("  id=%s user=%s title=%q product_line=%q\n",
			session.ID, session.UserID, shortenTitle(session.Title), session.ProductLine)
	}
}

func printZeroBindingOwners(currentPlan *plan) {
	if len(currentPlan.ZeroBindingOwners) == 0 {
		return
	}
	limit := len(currentPlan.ZeroBindingOwners)
	if limit > sampleLimit {
		limit = sampleLimit
	}
	for _, key := range currentPlan.ZeroBindingOwners[:limit] {
		printf("  - %s 会话=%d\n", key.String(), len(currentPlan.ByOwner[key]))
	}
	if len(currentPlan.ZeroBindingOwners) > limit {
		printf("  ... 其余 %d 人省略\n", len(currentPlan.ZeroBindingOwners)-limit)
	}
}

// shortenTitle 把标题压成单行并按字符截断，避免输出被换行撑乱。
func shortenTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) <= 40 {
		return title
	}
	return string(runes[:40]) + "..."
}

func planByUser(currentPlan *plan) map[string]int {
	out := make(map[string]int, len(currentPlan.ByOwner))
	for key, group := range currentPlan.ByOwner {
		out[key.String()] = len(group)
	}
	return out
}

func zeroBindingUserKeys(currentPlan *plan) []string {
	out := make([]string, 0, len(currentPlan.ZeroBindingOwners))
	for _, key := range currentPlan.ZeroBindingOwners {
		out = append(out, key.String())
	}
	return out
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// printf 统一走 Fprintf，避免 errcheck 报未检查的 fmt 返回值。
func printf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format, args...)
}
