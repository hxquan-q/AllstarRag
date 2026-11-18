package types

import (
	"context"
	"strings"
	"testing"
)

// T07-fix3 M1 往返：读路径净化不写库，但编辑器保存会把净化后的 SystemPrompt
// 写回 DB；下一次读取再次净化时不得追加第二条身份规则、也不得套娃。
func TestApplyBuiltinAgentLocalizationReadSaveReadRoundTrip(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	read := func(prompt string) *CustomAgent {
		agent := &CustomAgent{
			ID:        testBuiltinBrandAgentID,
			IsBuiltin: true,
			TenantID:  1,
			Config:    CustomAgentConfig{SystemPrompt: prompt},
		}
		ApplyBuiltinAgentLocalization(context.Background(), agent)
		return agent
	}

	// 第一次读取：旧持久化提示词（XiaoAi 前缀 + XiaoAi Cloud）被净化并补规则。
	first := read("You are XiaoAi, a knowledge-base question-answering assistant developed by Allstar. " +
		"Deploy on XiaoAi Cloud.").Config.SystemPrompt
	if strings.Contains(first, "XiaoAi Cloud") {
		t.Fatalf("first read left XiaoAi Cloud: %q", first)
	}
	if got := strings.Count(first, "never 小艾"); got != 1 {
		t.Fatalf("first read identity rule count = %d, want 1; prompt=%q", got, first)
	}

	// 模拟编辑器保存：把第一次净化后的文本原样写回，再走一次读路径。
	second := read(first).Config.SystemPrompt
	if second != first {
		t.Fatalf("read-save-read changed prompt:\nfirst  %q\nsecond %q", first, second)
	}
	if got := strings.Count(second, "never 小艾"); got != 1 {
		t.Fatalf("round-trip identity rule count = %d, want 1; prompt=%q", got, second)
	}
	if strings.Contains(second, "小爱云 (") {
		t.Fatalf("round-trip nested XiaoAi Cloud replacement: %q", second)
	}
}

// T07-fix4 R5：持久化的 builtin-skill-installer 提示词走读路径时只做品牌词
// 净化，不追加 persona 身份规则（与 agent_system_prompt.yaml 的 installer 口径
// 一致）。普通/助手仍按原规则，见同包
// TestApplyBuiltinAgentLocalizationRewritesPersistedXiaoAiPrompt /
// TestApplyBuiltinAgentLocalizationAssistantUsesAssistantRule。
func TestApplyBuiltinAgentLocalizationSkillInstallerSkipsIdentityRule(t *testing.T) {
	restore := OverrideBuiltinAgentEntriesForTest(map[string]*BuiltinAgentEntry{
		BuiltinSkillInstallerID: {
			ID:        BuiltinSkillInstallerID,
			Avatar:    "📦",
			IsBuiltin: true,
			I18n: map[string]BuiltinAgentI18n{
				"default": {Name: "Skill Installer", Description: "Internal agent"},
			},
		},
	})
	t.Cleanup(restore)

	agent := &CustomAgent{
		ID:        BuiltinSkillInstallerID,
		IsBuiltin: true,
		TenantID:  1,
		Config: CustomAgentConfig{
			SystemPrompt: "You are the XiaoAi Skill Installer. developed by Tencent. " +
				"Manage WeKnora 知识库.",
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)

	const wantPrompt = "You are the 小爱 (XiaoAi) Skill Installer. developed internally by Allstar. " +
		"Manage 小爱知识库."
	if agent.Config.SystemPrompt != wantPrompt {
		t.Fatalf("SystemPrompt = %q, want %q", agent.Config.SystemPrompt, wantPrompt)
	}
	if strings.Contains(agent.Config.SystemPrompt, "never 小艾") {
		t.Fatalf("identity rule leaked into installer prompt: %q", agent.Config.SystemPrompt)
	}
	if strings.Contains(agent.Config.SystemPrompt, "When replying in Chinese") {
		t.Fatalf("identity rule leaked into installer prompt: %q", agent.Config.SystemPrompt)
	}

	// 二次读取（编辑器保存写回）同样不得出现身份规则。
	ApplyBuiltinAgentLocalization(context.Background(), agent)
	if agent.Config.SystemPrompt != wantPrompt {
		t.Fatalf("second pass changed installer prompt: %q", agent.Config.SystemPrompt)
	}
}
