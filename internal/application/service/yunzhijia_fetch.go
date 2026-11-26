package service

// 云之家同步的抓取与校验：分页拉全、total 一致性，
// 以及同步/预检共用的去重、部门映射与抓取阶段异常。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/sso/yunzhijia"
	"github.com/Tencent/WeKnora/internal/types"
)

// yzjFetched 是一轮抓取的原始结果，active/inactive 都按上游顺序保留。
type yzjFetched struct {
	active   []yunzhijia.Employee
	inactive []yunzhijia.Employee
}

// yzjSyncPlan 是去重后待处理的员工，以及抓取阶段就能确定的异常。
type yzjSyncPlan struct {
	active    []yunzhijia.Employee
	inactive  []yunzhijia.Employee
	anomalies []types.YunzhijiaSyncAnomaly
}

// fetchAll 拉取在职与离职两个完整列表。
// 任一页失败、分页信息不一致、累计数不等于 total，或在职总数为 0，
// 都返回整轮错误；调用方不得在错误后写任何账号。
func (s *YunzhijiaSyncService) fetchAll(ctx context.Context) (*yzjFetched, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	active, err := s.fetchStatus(ctx, yunzhijia.StatusActive)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("yunzhijia: 在职员工总数为 0，拒绝整轮同步")
	}
	inactive, err := s.fetchStatus(ctx, yunzhijia.StatusInactive)
	if err != nil {
		return nil, err
	}
	return &yzjFetched{active: active, inactive: inactive}, nil
}

// fetchStatus 按 cfg.PageSize（显式传参）逐页拉取，直到列表为空或累计
// 达到 total；最后再核对累计条数与 total 完全一致。
// 上游回显的 page_size 不做校验：真实接口可能按自己的上限返回，
// 完整性由 page 回显、total 一致性和去重保证。
func (s *YunzhijiaSyncService) fetchStatus(
	ctx context.Context, status yunzhijia.Status,
) ([]yunzhijia.Employee, error) {
	var out []yunzhijia.Employee
	total := -1
	for page := 1; ; page++ {
		got, err := s.client.ListEmployees(ctx, status, page, s.cfg.PageSize)
		if err != nil {
			return nil, fmt.Errorf("yunzhijia: 拉取 %s 第 %d 页失败: %w", status, page, err)
		}
		if got.Page != page {
			return nil, fmt.Errorf(
				"yunzhijia: 拉取 %s 第 %d 页分页信息不一致（page=%d）",
				status, page, got.Page,
			)
		}
		if total < 0 {
			total = got.Total
		} else if got.Total != total {
			return nil, fmt.Errorf(
				"yunzhijia: 拉取 %s 第 %d 页 total 从 %d 变为 %d",
				status, page, total, got.Total,
			)
		}
		out = append(out, got.List...)
		if len(got.List) == 0 || len(out) >= total {
			break
		}
	}
	if len(out) != total {
		return nil, fmt.Errorf(
			"yunzhijia: 拉取 %s 累计 %d 条与 total %d 不一致", status, len(out), total,
		)
	}
	return out, nil
}

// departmentTenant 把部门路径映射到目标空间。
// 返回 false 表示部门为空（未知部门）：仍落到默认空间，但记异常。
func (s *YunzhijiaSyncService) departmentTenant(department string) (uint64, bool) {
	trimmed := strings.TrimSpace(department)
	if trimmed == "" {
		return s.cfg.TenantDefault, false
	}
	if strings.Contains(trimmed, s.cfg.HRDepartmentKeyword) {
		return s.cfg.TenantHR, true
	}
	return s.cfg.TenantDefault, true
}

// employeeRow 把上游条目转换为待 upsert 的员工记录。
// 只填同步字段；user_id / blocked / disabled_by_sync 由本地维护。
func (s *YunzhijiaSyncService) employeeRow(
	employee yunzhijia.Employee,
	status types.YunzhijiaEmployeeStatus,
	now time.Time,
) *types.YunzhijiaEmployee {
	tenantID, _ := s.departmentTenant(employee.Department)
	row := &types.YunzhijiaEmployee{
		OpenID:       employee.OpenID,
		Name:         employee.Name,
		Department:   employee.Department,
		TenantID:     tenantID,
		Status:       status,
		LastSyncedAt: now,
	}
	if employee.Mobile != "" {
		mobile := employee.Mobile
		row.Mobile = &mobile
	}
	return row
}

// buildPlan 对抓取结果做去重并生成抓取阶段异常：
//   - open_id 重复：首条生效，其余记 duplicate_open_id 并跳过；
//   - 在职列表同名：每个重名只记一条 duplicate_name，人员照常处理；
//   - 部门为空：记 unknown_department，仍落到默认空间。
func (s *YunzhijiaSyncService) buildPlan(fetched *yzjFetched) *yzjSyncPlan {
	plan := &yzjSyncPlan{}
	seen := make(map[string]bool)
	nameCount := make(map[string]int)

	for _, employee := range fetched.active {
		if seen[employee.OpenID] {
			plan.anomalies = append(plan.anomalies, duplicateOpenIDAnomaly(employee))
			continue
		}
		seen[employee.OpenID] = true
		plan.active = append(plan.active, employee)
		nameCount[employee.Name]++
	}
	for _, employee := range fetched.inactive {
		if seen[employee.OpenID] {
			plan.anomalies = append(plan.anomalies, duplicateOpenIDAnomaly(employee))
			continue
		}
		seen[employee.OpenID] = true
		plan.inactive = append(plan.inactive, employee)
	}

	names := make([]string, 0, len(nameCount))
	for name, count := range nameCount {
		if count > 1 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		plan.anomalies = append(plan.anomalies, types.YunzhijiaSyncAnomaly{
			Kind:   types.YunzhijiaAnomalyDuplicateName,
			Name:   name,
			Detail: fmt.Sprintf("在职列表存在 %d 名同名员工", nameCount[name]),
		})
	}

	for _, employee := range plan.active {
		if _, known := s.departmentTenant(employee.Department); !known {
			plan.anomalies = append(plan.anomalies, unknownDepartmentAnomaly(employee))
		}
	}
	for _, employee := range plan.inactive {
		if _, known := s.departmentTenant(employee.Department); !known {
			plan.anomalies = append(plan.anomalies, unknownDepartmentAnomaly(employee))
		}
	}
	return plan
}

func duplicateOpenIDAnomaly(employee yunzhijia.Employee) types.YunzhijiaSyncAnomaly {
	return types.YunzhijiaSyncAnomaly{
		Kind:   types.YunzhijiaAnomalyDuplicateOpenID,
		OpenID: employee.OpenID,
		Name:   employee.Name,
		Detail: "同一 open_id 重复出现，保留首条",
	}
}

func unknownDepartmentAnomaly(employee yunzhijia.Employee) types.YunzhijiaSyncAnomaly {
	return types.YunzhijiaSyncAnomaly{
		Kind:   types.YunzhijiaAnomalyUnknownDepartment,
		OpenID: employee.OpenID,
		Name:   employee.Name,
		Detail: "部门为空，已归入默认空间",
	}
}
