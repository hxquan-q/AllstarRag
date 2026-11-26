package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // 校验 timezone 时不依赖宿主机 tzdata

	"github.com/robfig/cron/v3"
)

// YunzhijiaSSOConfig 是云之家免登录与员工同步的配置段。
//
// 密钥/API Key 不进这里：当前员工接口无凭证，后续若有也只放 .env。
// 配置缺失或非法时 fail-closed：Validate 返回错误，调用方不得启动登录/同步。
type YunzhijiaSSOConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// BaseURL 是云之家服务根地址（scheme + host）。
	BaseURL string `yaml:"base_url" json:"base_url"`
	// EmployeePath 是员工分页接口路径，例如 /@asd_rd/api/public/employees。
	EmployeePath string `yaml:"employee_path" json:"employee_path"`
	// TicketUserPath 是 ticket 换姓名接口路径。
	TicketUserPath string `yaml:"ticket_user_path" json:"ticket_user_path"`
	// AppID 是云之家应用标识。
	AppID string `yaml:"app_id" json:"app_id"`
	// TenantDefault 是默认归属空间（金蝶社区），TenantHR 是人力空间。
	TenantDefault uint64 `yaml:"tenant_default" json:"tenant_default"`
	TenantHR      uint64 `yaml:"tenant_hr" json:"tenant_hr"`
	// HRDepartmentKeyword 命中部门路径时归入人力空间。
	HRDepartmentKeyword string `yaml:"hr_department_keyword" json:"hr_department_keyword"`
	// SyncCron 是 5 段标准 cron（分 时 日 月 周）。
	SyncCron string `yaml:"sync_cron" json:"sync_cron"`
	// Timezone 是 cron 的计算时区。
	Timezone string `yaml:"timezone" json:"timezone"`
	// PageSize 是员工接口分页大小。
	PageSize int `yaml:"page_size" json:"page_size"`
	// HTTPTimeoutSeconds 是单次外部请求超时。
	HTTPTimeoutSeconds int `yaml:"http_timeout_seconds" json:"http_timeout_seconds"`
	// StartupCatchupHours 是启动补跑阈值：0 = 仅首次同步、不做过期补跑；
	// >0 = 距上次成功超过该小时数时补跑。
	StartupCatchupHours int `yaml:"startup_catchup_hours" json:"startup_catchup_hours"`
}

// Validate 全量校验配置；任何缺失或非法都返回错误（fail-closed）。
// 是否启用由调用方自行判断，这里不因 enabled=false 跳过校验。
// base_url 与路径的格式口径与客户端 parseBaseURL/joinURLPath 一致：
// 这里通过即保证客户端一定能构造。
func (c *YunzhijiaSSOConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("yunzhijia_sso: 配置段缺失")
	}

	var problems []string
	if err := validateYunzhijiaBaseURL(c.BaseURL); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateYunzhijiaPath("employee_path", c.EmployeePath); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateYunzhijiaPath("ticket_user_path", c.TicketUserPath); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(c.AppID) == "" {
		problems = append(problems, "app_id 不能为空")
	}
	if c.TenantDefault == 0 {
		problems = append(problems, "tenant_default 必须为正整数")
	}
	if c.TenantHR == 0 {
		problems = append(problems, "tenant_hr 必须为正整数")
	}
	if c.TenantDefault != 0 && c.TenantDefault == c.TenantHR {
		problems = append(problems, "tenant_default 与 tenant_hr 不能相同")
	}
	if strings.TrimSpace(c.HRDepartmentKeyword) == "" {
		problems = append(problems, "hr_department_keyword 不能为空")
	}
	if strings.TrimSpace(c.SyncCron) == "" {
		problems = append(problems, "sync_cron 不能为空")
	} else if _, err := cron.ParseStandard(strings.TrimSpace(c.SyncCron)); err != nil {
		problems = append(problems, fmt.Sprintf("sync_cron 非法: %v", err))
	}
	if strings.TrimSpace(c.Timezone) == "" {
		problems = append(problems, "timezone 不能为空")
	} else if _, err := time.LoadLocation(strings.TrimSpace(c.Timezone)); err != nil {
		problems = append(problems, fmt.Sprintf("timezone 非法: %v", err))
	}
	if c.PageSize <= 0 {
		problems = append(problems, "page_size 必须为正整数")
	}
	if c.HTTPTimeoutSeconds <= 0 {
		problems = append(problems, "http_timeout_seconds 必须为正整数")
	}
	if c.StartupCatchupHours < 0 {
		problems = append(problems, "startup_catchup_hours 不能为负数")
	}

	if len(problems) > 0 {
		return fmt.Errorf("yunzhijia_sso: %s", strings.Join(problems, "; "))
	}
	return nil
}

// validateYunzhijiaBaseURL 与客户端 parseBaseURL 同口径：非空、url.Parse
// 成功、scheme 为 http/https、host 非空、不带 query/fragment。
func validateYunzhijiaBaseURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("base_url 不能为空")
	}
	base, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("base_url 非法")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return fmt.Errorf("base_url 必须以 http:// 或 https:// 开头")
	}
	if base.Host == "" {
		return fmt.Errorf("base_url 缺少主机名")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("base_url 不能包含查询串或片段")
	}
	return nil
}

// validateYunzhijiaPath 要求非空、以 / 开头、不含 ? 或 #（与客户端
// joinURLPath 同口径，避免在配置里夹带查询串）。
func validateYunzhijiaPath(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s 不能为空", name)
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s 必须以 / 开头", name)
	}
	if strings.ContainsAny(value, "?#") {
		return fmt.Errorf("%s 不能包含查询串或片段", name)
	}
	return nil
}
