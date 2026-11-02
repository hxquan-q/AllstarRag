package config

import "testing"

// auth_registration_mode_behavior_test.go 是 T02 的独立行为测试,固定
// WEKNORA_AUTH_REGISTRATION_MODE 的对外契约:
//   - 三个合法值覆盖 YAML 值;
//   - trim + 大小写不敏感;
//   - 非法值忽略(保持 YAML / 默认值),不阻断;
//   - 与历史 DISABLE_REGISTRATION=true 同时设置时专用变量优先。
// 与实现者的测试文件相互独立,用例名与 helper 均不共用。

func clearRegistrationModeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WEKNORA_AUTH_REGISTRATION_MODE",
		"DISABLE_REGISTRATION",
		"WEKNORA_AUTH_COMPLEX_PASSWORD_ENABLED",
		"WEKNORA_AUTH_DEFAULT_TENANT_MODE",
		"WEKNORA_TENANT_ENABLE_RBAC",
		"WEKNORA_TENANT_MAX_OWNED_PER_USER",
		"WEKNORA_TENANT_SELF_SERVICE_CREATION_ENABLED",
		"WEKNORA_TENANT_ENABLE_CROSS_TENANT_ACCESS",
	} {
		t.Setenv(key, "")
	}
}

func resolveMode(t *testing.T, yamlMode string) string {
	t.Helper()
	cfg := &Config{Auth: &AuthConfig{RegistrationMode: yamlMode}}
	applyAuthAndTenantDefaults(cfg)
	return cfg.Auth.RegistrationMode
}

func TestBehaviorRegistrationModeEnvThreeLegalValues(t *testing.T) {
	cases := []struct {
		env      string
		yamlMode string
	}{
		{AuthRegistrationModeSelfServe, AuthRegistrationModeInviteOnly},
		{AuthRegistrationModeInviteOnly, AuthRegistrationModeSelfServe},
		{AuthRegistrationModeInviteRegister, AuthRegistrationModeSelfServe},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			clearRegistrationModeEnv(t)
			t.Setenv("WEKNORA_AUTH_REGISTRATION_MODE", tc.env)
			if got := resolveMode(t, tc.yamlMode); got != tc.env {
				t.Fatalf("env=%q yaml=%q: registration_mode = %q, want %q",
					tc.env, tc.yamlMode, got, tc.env)
			}
		})
	}
}

func TestBehaviorRegistrationModeEnvCaseAndWhitespace(t *testing.T) {
	cases := []struct {
		env      string
		expected string
	}{
		{"SELF_SERVE", AuthRegistrationModeSelfServe},
		{"  invite_only\t", AuthRegistrationModeInviteOnly},
		{"\tInViTe_ReGiStEr \n", AuthRegistrationModeInviteRegister},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			clearRegistrationModeEnv(t)
			t.Setenv("WEKNORA_AUTH_REGISTRATION_MODE", tc.env)
			if got := resolveMode(t, AuthRegistrationModeSelfServe); got != tc.expected {
				t.Fatalf("env=%q: registration_mode = %q, want %q", tc.env, got, tc.expected)
			}
		})
	}
}

func TestBehaviorRegistrationModeInvalidEnvIgnored(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		yamlMode string
		expected string
	}{
		{"unknown word", "open", AuthRegistrationModeInviteOnly, AuthRegistrationModeInviteOnly},
		{"hyphen instead of underscore", "invite-register", AuthRegistrationModeSelfServe, AuthRegistrationModeSelfServe},
		{"space inside", "self serve", AuthRegistrationModeInviteOnly, AuthRegistrationModeInviteOnly},
		{"trailing junk", "invite_register_v2", AuthRegistrationModeSelfServe, AuthRegistrationModeSelfServe},
		{"whitespace only keeps yaml", "   ", AuthRegistrationModeInviteRegister, AuthRegistrationModeInviteRegister},
		{"invalid with empty yaml falls back", "open", "", AuthRegistrationModeSelfServe},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearRegistrationModeEnv(t)
			t.Setenv("WEKNORA_AUTH_REGISTRATION_MODE", tc.env)
			if got := resolveMode(t, tc.yamlMode); got != tc.expected {
				t.Fatalf("env=%q yaml=%q: registration_mode = %q, want %q",
					tc.env, tc.yamlMode, got, tc.expected)
			}
		})
	}
}

func TestBehaviorLegacyDisableRegistrationAlone(t *testing.T) {
	clearRegistrationModeEnv(t)
	t.Setenv("DISABLE_REGISTRATION", "true")
	if got := resolveMode(t, AuthRegistrationModeSelfServe); got != AuthRegistrationModeInviteOnly {
		t.Fatalf("DISABLE_REGISTRATION=true: registration_mode = %q, want %q",
			got, AuthRegistrationModeInviteOnly)
	}
}

// TestBehaviorDedicatedEnvWinsOverLegacyDisable 覆盖 S2 优先级:
// 历史变量残留为 true 时,专用变量仍生效(部署切 invite_register 不被顶掉)。
func TestBehaviorDedicatedEnvWinsOverLegacyDisable(t *testing.T) {
	cases := []string{
		AuthRegistrationModeSelfServe,
		AuthRegistrationModeInviteOnly,
		AuthRegistrationModeInviteRegister,
	}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			clearRegistrationModeEnv(t)
			t.Setenv("DISABLE_REGISTRATION", "TRUE")
			t.Setenv("WEKNORA_AUTH_REGISTRATION_MODE", mode)
			if got := resolveMode(t, AuthRegistrationModeSelfServe); got != mode {
				t.Fatalf("env=%q + DISABLE_REGISTRATION=TRUE: registration_mode = %q, want %q",
					mode, got, mode)
			}
		})
	}
}

func TestBehaviorRegistrationModeDefaultWhenUnset(t *testing.T) {
	clearRegistrationModeEnv(t)
	if got := resolveMode(t, ""); got != AuthRegistrationModeSelfServe {
		t.Fatalf("all unset: registration_mode = %q, want %q", got, AuthRegistrationModeSelfServe)
	}
}
