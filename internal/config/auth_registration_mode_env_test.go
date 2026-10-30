package config

import "testing"

// auth_registration_mode_env_test.go 固定 WEKNORA_AUTH_REGISTRATION_MODE 的
// env 覆盖契约:三个合法值(trim + 大小写不敏感)覆盖 YAML,空值保持 YAML,
// 非法值忽略并保持 YAML(不阻断启动);与历史 DISABLE_REGISTRATION 同时
// 设置时,专用变量优先。

// setAuthEnvForRegistrationMode 清掉会干扰断言的相邻 env,
// 避免开发机 shell 里的残留变量污染用例。
func setAuthEnvForRegistrationMode(t *testing.T, modeEnv, disableRegistration string) {
	t.Helper()
	t.Setenv("WEKNORA_AUTH_REGISTRATION_MODE", modeEnv)
	t.Setenv("DISABLE_REGISTRATION", disableRegistration)
	t.Setenv("WEKNORA_AUTH_COMPLEX_PASSWORD_ENABLED", "")
	t.Setenv("WEKNORA_AUTH_DEFAULT_TENANT_MODE", "")
	t.Setenv("WEKNORA_TENANT_ENABLE_RBAC", "")
	t.Setenv("WEKNORA_TENANT_MAX_OWNED_PER_USER", "")
	t.Setenv("WEKNORA_TENANT_SELF_SERVICE_CREATION_ENABLED", "")
	t.Setenv("WEKNORA_TENANT_ENABLE_CROSS_TENANT_ACCESS", "")
}

func TestApplyAuthAndTenantDefaults_RegistrationModeEnv(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		disable  string
		yamlMode string
		expected string
	}{
		{
			name: "self_serve overrides yaml invite_only",
			env:  AuthRegistrationModeSelfServe, yamlMode: AuthRegistrationModeInviteOnly,
			expected: AuthRegistrationModeSelfServe,
		},
		{
			name: "invite_only overrides yaml self_serve",
			env:  AuthRegistrationModeInviteOnly, yamlMode: AuthRegistrationModeSelfServe,
			expected: AuthRegistrationModeInviteOnly,
		},
		{
			name: "invite_register overrides yaml self_serve",
			env:  AuthRegistrationModeInviteRegister, yamlMode: AuthRegistrationModeSelfServe,
			expected: AuthRegistrationModeInviteRegister,
		},
		{
			name: "trim and case-insensitive",
			env:  "  INVITE_REGISTER  ", yamlMode: AuthRegistrationModeSelfServe,
			expected: AuthRegistrationModeInviteRegister,
		},
		{
			name: "empty env keeps yaml self_serve",
			env:  "", yamlMode: AuthRegistrationModeSelfServe,
			expected: AuthRegistrationModeSelfServe,
		},
		{
			name: "empty env keeps yaml invite_register",
			env:  "", yamlMode: AuthRegistrationModeInviteRegister,
			expected: AuthRegistrationModeInviteRegister,
		},
		{
			name: "empty env and empty yaml falls back to self_serve",
			env:  "", yamlMode: "",
			expected: AuthRegistrationModeSelfServe,
		},
		{
			name: "invalid env keeps yaml invite_only",
			env:  "open", yamlMode: AuthRegistrationModeInviteOnly,
			expected: AuthRegistrationModeInviteOnly,
		},
		{
			name: "invalid env and empty yaml falls back to self_serve",
			env:  "open", yamlMode: "",
			expected: AuthRegistrationModeSelfServe,
		},
		{
			name: "dedicated env wins over legacy disable_registration",
			env:  AuthRegistrationModeInviteRegister, disable: "true",
			yamlMode: AuthRegistrationModeSelfServe,
			expected: AuthRegistrationModeInviteRegister,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAuthEnvForRegistrationMode(t, tc.env, tc.disable)

			cfg := &Config{Auth: &AuthConfig{RegistrationMode: tc.yamlMode}}
			applyAuthAndTenantDefaults(cfg)

			if cfg.Auth.RegistrationMode != tc.expected {
				t.Fatalf("registration_mode = %q, want %q", cfg.Auth.RegistrationMode, tc.expected)
			}
		})
	}
}
