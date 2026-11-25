package config

// R2 部署前审查 M1：Validate 必须与客户端 parseBaseURL/joinURLPath 同口径，
// 否则登录 handler 会放过一个客户端根本构造不出的配置。这里覆盖 base_url
// 的空/scheme/host/query/fragment 与路径的 ?/# 拒绝。

import (
	"strings"
	"testing"
)

// yzjValidConfig 返回一份字段齐全、Validate 必过的配置；测试在此基础上
// 只改一个字段来验证单点校验。
func yzjValidConfig() *YunzhijiaSSOConfig {
	return &YunzhijiaSSOConfig{
		Enabled:             true,
		BaseURL:             "https://yunzhijia.invalid",
		EmployeePath:        "/@asd_rd/api/public/employees",
		TicketUserPath:      "/@asd_rd/api/public/ticket/user",
		AppID:               "app-placeholder",
		TenantDefault:       10024,
		TenantHR:            10022,
		HRDepartmentKeyword: "人力资源部",
		SyncCron:            "0 3 * * *",
		Timezone:            "Asia/Shanghai",
		PageSize:            100,
		HTTPTimeoutSeconds:  10,
	}
}

func TestYunzhijiaValidateAcceptsCompleteConfig(t *testing.T) {
	if err := yzjValidConfig().Validate(); err != nil {
		t.Fatalf("完整合法配置应通过 Validate，实际: %v", err)
	}
}

func TestYunzhijiaValidateRejectsBadBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"空", "", "base_url 不能为空"},
		{"仅空白", "   ", "base_url 不能为空"},
		{"非 http(s)", "ftp://yunzhijia.invalid", "必须以 http:// 或 https:// 开头"},
		{"无 scheme", "yunzhijia.invalid", "必须以 http:// 或 https:// 开头"},
		{"缺主机名", "https://", "base_url 缺少主机名"},
		{"带查询串", "https://yunzhijia.invalid?appid=1", "不能包含查询串或片段"},
		{"带片段", "https://yunzhijia.invalid#frag", "不能包含查询串或片段"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := yzjValidConfig()
			cfg.BaseURL = tc.baseURL
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("base_url=%q 应被 Validate 拒绝", tc.baseURL)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应包含 %q，实际: %v", tc.want, err)
			}
		})
	}
}

func TestYunzhijiaValidateRejectsPathWithQueryOrFragment(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*YunzhijiaSSOConfig)
		want string
	}{
		{"employee_path 带 ?", func(c *YunzhijiaSSOConfig) {
			c.EmployeePath = "/employees?status=active"
		}, "employee_path 不能包含查询串或片段"},
		{"employee_path 带 #", func(c *YunzhijiaSSOConfig) {
			c.EmployeePath = "/employees#frag"
		}, "employee_path 不能包含查询串或片段"},
		{"ticket_user_path 带 ?", func(c *YunzhijiaSSOConfig) {
			c.TicketUserPath = "/ticket/user?x=1"
		}, "ticket_user_path 不能包含查询串或片段"},
		{"ticket_user_path 带 #", func(c *YunzhijiaSSOConfig) {
			c.TicketUserPath = "/ticket/user#frag"
		}, "ticket_user_path 不能包含查询串或片段"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := yzjValidConfig()
			tc.mut(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("含查询串/片段的路径应被 Validate 拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应包含 %q，实际: %v", tc.want, err)
			}
		})
	}
}

func TestYunzhijiaValidateRejectsEmptyOrRelativePaths(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*YunzhijiaSSOConfig)
		want string
	}{
		{"employee_path 为空", func(c *YunzhijiaSSOConfig) { c.EmployeePath = "" },
			"employee_path 不能为空"},
		{"employee_path 不以 / 开头", func(c *YunzhijiaSSOConfig) { c.EmployeePath = "employees" },
			"employee_path 必须以 / 开头"},
		{"ticket_user_path 为空", func(c *YunzhijiaSSOConfig) { c.TicketUserPath = "" },
			"ticket_user_path 不能为空"},
		{"ticket_user_path 不以 / 开头", func(c *YunzhijiaSSOConfig) { c.TicketUserPath = "ticket/user" },
			"ticket_user_path 必须以 / 开头"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := yzjValidConfig()
			tc.mut(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("空/相对路径应被 Validate 拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应包含 %q，实际: %v", tc.want, err)
			}
		})
	}
}
