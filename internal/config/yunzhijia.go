package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // 校验 timezone 时不依赖宿主机 tzdata

	"github.com/robfig/cron/v3"
)

// 01-spec §13 的默认值。配置里省略某项时可用 ApplyDefaults 补齐；
// Enable 是总开关，默认值不自动打开，必须显式配置。
const (
	defaultYunzhijiaBaseURL          = "https://pj.szonesoft.com"
	defaultYunzhijiaEmployeePath     = "/@asd_rd/api/public/employees"
	defaultYunzhijiaTicketUserPath   = "/@project/api/plugin-api/ProjectAndCloud/queryUserName"
	defaultYunzhijiaAppID            = "500995523"
	defaultYunzhijiaTenantDefault    = uint64(10024)
	defaultYunzhijiaTenantHR         = uint64(10022)
	defaultYunzhijiaHRDepartment     = "人力资源部"
	defaultYunzhijiaSyncCron         = "0 3 * * *"
	defaultYunzhijiaTimezone         = "Asia/Shanghai"
	defaultYunzhijiaPageSize         = 500
	defaultYunzhijiaHTTPTimeoutSecs  = 10
	defaultYunzhijiaStartupCatchupHr = 24
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
	// StartupCatchupHours：启动时距上次成功超过该小时数就补跑一次。
	StartupCatchupHours int `yaml:"startup_catchup_hours" json:"startup_catchup_hours"`
}

// DefaultYunzhijiaSSOConfig 返回 01-spec §13 的完整默认配置。
// 返回的 Enabled 为 true，仅用于生成/补齐配置模板；是否启用仍由
// 配置文件里的 enabled 显式决定。
func DefaultYunzhijiaSSOConfig() *YunzhijiaSSOConfig {
	return &YunzhijiaSSOConfig{
		Enabled:             true,
		BaseURL:             defaultYunzhijiaBaseURL,
		EmployeePath:        defaultYunzhijiaEmployeePath,
		TicketUserPath:      defaultYunzhijiaTicketUserPath,
		AppID:               defaultYunzhijiaAppID,
		TenantDefault:       defaultYunzhijiaTenantDefault,
		TenantHR:            defaultYunzhijiaTenantHR,
		HRDepartmentKeyword: defaultYunzhijiaHRDepartment,
		SyncCron:            defaultYunzhijiaSyncCron,
		Timezone:            defaultYunzhijiaTimezone,
		PageSize:            defaultYunzhijiaPageSize,
		HTTPTimeoutSeconds:  defaultYunzhijiaHTTPTimeoutSecs,
		StartupCatchupHours: defaultYunzhijiaStartupCatchupHr,
	}
}

// ApplyDefaults 用 01-spec §13 的默认值补齐零值字段。
// Enabled 不参与补齐，避免把显式关闭误改成开启；nil 接收者安全返回。
func (c *YunzhijiaSSOConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultYunzhijiaBaseURL
	}
	if strings.TrimSpace(c.EmployeePath) == "" {
		c.EmployeePath = defaultYunzhijiaEmployeePath
	}
	if strings.TrimSpace(c.TicketUserPath) == "" {
		c.TicketUserPath = defaultYunzhijiaTicketUserPath
	}
	if strings.TrimSpace(c.AppID) == "" {
		c.AppID = defaultYunzhijiaAppID
	}
	if c.TenantDefault == 0 {
		c.TenantDefault = defaultYunzhijiaTenantDefault
	}
	if c.TenantHR == 0 {
		c.TenantHR = defaultYunzhijiaTenantHR
	}
	if strings.TrimSpace(c.HRDepartmentKeyword) == "" {
		c.HRDepartmentKeyword = defaultYunzhijiaHRDepartment
	}
	if strings.TrimSpace(c.SyncCron) == "" {
		c.SyncCron = defaultYunzhijiaSyncCron
	}
	if strings.TrimSpace(c.Timezone) == "" {
		c.Timezone = defaultYunzhijiaTimezone
	}
	if c.PageSize == 0 {
		c.PageSize = defaultYunzhijiaPageSize
	}
	if c.HTTPTimeoutSeconds == 0 {
		c.HTTPTimeoutSeconds = defaultYunzhijiaHTTPTimeoutSecs
	}
	if c.StartupCatchupHours == 0 {
		c.StartupCatchupHours = defaultYunzhijiaStartupCatchupHr
	}
}

// Validate 校验配置。enabled=false 时直接放行（登录/同步由调用方跳过）；
// enabled=true 时任何缺失或非法都返回错误（fail-closed）。
func (c *YunzhijiaSSOConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("yunzhijia_sso: 配置段缺失")
	}
	if !c.Enabled {
		return nil
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

// validateYunzhijiaBaseURL 要求绝对 http(s) 地址，且不带用户信息。
func validateYunzhijiaBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("base_url 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url 非法: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url 必须是 http 或 https 地址")
	}
	if u.Host == "" {
		return fmt.Errorf("base_url 缺少主机名")
	}
	if u.User != nil {
		return fmt.Errorf("base_url 不允许携带用户信息")
	}
	return nil
}

// validateYunzhijiaPath 要求非空且以 / 开头的绝对路径。
func validateYunzhijiaPath(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s 不能为空", name)
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s 必须以 / 开头", name)
	}
	return nil
}
