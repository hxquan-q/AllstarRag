package handler

import "testing"

// auth_login_ratelimit_mask_test.go 覆盖登录限速日志的邮箱掩码:
// 日志只展示首字符 + *** + 域名,key 的计数逻辑不受影响。

func TestMaskLoginRateLimitKey(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"alice@example.com|203.0.113.5", "a***@example.com|203.0.113.5"},
		{"a@b.test|127.0.0.1", "a***@b.test|127.0.0.1"},
		{"张三@example.com|10.0.0.1", "张***@example.com|10.0.0.1"},
		{"no-at-sign|10.0.0.1", "n***|10.0.0.1"},
		{"|10.0.0.1", "***|10.0.0.1"},
		{"", "***"},
		// R2 finding 2:邮箱本地部分允许 `|`(RFC 5322 atext),必须按
		// 最后一个竖线切分,否则邮箱剩余部分会漏进 IP 段。
		{"a|b@example.com|1.2.3.4", "a***@example.com|1.2.3.4"},
		{"a|b|1.2.3.4", "a***|1.2.3.4"},
	}
	for _, tt := range tests {
		if got := maskLoginRateLimitKey(tt.key); got != tt.want {
			t.Fatalf("maskLoginRateLimitKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}
