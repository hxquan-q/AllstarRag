package types

import (
	"context"
	"testing"
)

func TestApplyBuiltinAgentLocalizationOverlaysYAMLLocale(t *testing.T) {
	restore := OverrideBuiltinAgentEntriesForTest(map[string]*BuiltinAgentEntry{
		BuiltinQuickAnswerID: {
			ID:     BuiltinQuickAnswerID,
			Avatar: "quick.png",
			I18n: map[string]BuiltinAgentI18n{
				"default": {Name: "快速问答", Description: "中文 RAG"},
				"en-US":   {Name: "Quick Answer", Description: "Knowledge base RAG Q&A"},
				"zh-CN":   {Name: "快速问答", Description: "中文 RAG"},
			},
		},
	})
	t.Cleanup(restore)

	agent := &CustomAgent{
		ID:          BuiltinQuickAnswerID,
		Name:        "快速问答",
		Description: "中文 RAG",
		Avatar:      "",
		IsBuiltin:   true,
		TenantID:    1,
	}
	ctx := context.WithValue(context.Background(), LanguageContextKey, "en-US")
	ApplyBuiltinAgentLocalization(ctx, agent)

	if agent.Name != "Quick Answer" {
		t.Fatalf("Name = %q, want Quick Answer", agent.Name)
	}
	if agent.Description != "Knowledge base RAG Q&A" {
		t.Fatalf("Description = %q, want English copy", agent.Description)
	}
	if agent.Avatar != "quick.png" {
		t.Fatalf("Avatar = %q, want quick.png", agent.Avatar)
	}
}

func TestApplyBuiltinAgentLocalizationNilSafe(t *testing.T) {
	ApplyBuiltinAgentLocalization(context.Background(), nil)
}

func TestApplyBuiltinAgentLocalizationLeavesUnknownAgents(t *testing.T) {
	agent := &CustomAgent{
		ID:          "custom-agent-1",
		Name:        "Mine",
		Description: "keep me",
		Config: CustomAgentConfig{
			SystemPrompt:    "You are the WeKnora Assistant. aud=weknora.",
			ContextTemplate: "WeKnora context",
		},
	}
	ctx := context.WithValue(context.Background(), LanguageContextKey, "en-US")
	ApplyBuiltinAgentLocalization(ctx, agent)
	if agent.Name != "Mine" || agent.Description != "keep me" {
		t.Fatalf("custom agent was rewritten: %+v", agent)
	}
	// T03: brand-word scrubbing is built-in-only. A tenant-authored agent must
	// keep its own prompt verbatim, including any WeKnora wording the user wrote.
	if agent.Config.SystemPrompt != "You are the WeKnora Assistant. aud=weknora." {
		t.Fatalf("custom agent prompt was rewritten: %q", agent.Config.SystemPrompt)
	}
	if agent.Config.ContextTemplate != "WeKnora context" {
		t.Fatalf("custom agent context template was rewritten: %q", agent.Config.ContextTemplate)
	}
}

// T03 red-phase fixture: a built-in agent whose stored row still carries the
// pre-rename prompt. OverrideBuiltinAgentEntriesForTest registers the YAML
// entry so ApplyBuiltinAgentLocalization treats the ID as built-in.
const testBuiltinBrandAgentID = "builtin-brand-test"

func registerTestBuiltinBrandAgent(t *testing.T) {
	t.Helper()
	restore := OverrideBuiltinAgentEntriesForTest(map[string]*BuiltinAgentEntry{
		testBuiltinBrandAgentID: {
			ID:        testBuiltinBrandAgentID,
			Avatar:    "brand.png",
			IsBuiltin: true,
			I18n: map[string]BuiltinAgentI18n{
				"default": {Name: "XiaoAi Assistant", Description: "XiaoAi workspace assistant"},
				"en-US":   {Name: "XiaoAi Assistant", Description: "XiaoAi workspace assistant"},
			},
		},
	})
	t.Cleanup(restore)
}

// T03 red: a built-in agent persisted in the DB before the rename keeps its old
// prompt in Config. The read path must scrub brand words without touching the
// lowercase technical identifiers, and without rewriting unrelated text.
func TestApplyBuiltinAgentLocalizationSanitizesPersistedBuiltinPrompt(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	agent := &CustomAgent{
		ID:          testBuiltinBrandAgentID,
		Name:        "WeKnora 助手",
		Description: "旧描述",
		IsBuiltin:   true,
		TenantID:    1,
		Config: CustomAgentConfig{
			SystemPrompt: "You are the WeKnora Assistant. Deploy on WeKnora Cloud. " +
				"Keep lower: weknora. aud=weknora. End WeKnora.",
			ContextTemplate: "WeKnora context: {content}",
			Temperature:     0.42,
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)

	const wantPrompt = "You are the XiaoAi Assistant. Deploy on XiaoAi Cloud. " +
		"Keep lower: weknora. aud=weknora. End XiaoAi."
	if agent.Config.SystemPrompt != wantPrompt {
		t.Errorf("SystemPrompt = %q, want %q", agent.Config.SystemPrompt, wantPrompt)
	}
	const wantContext = "XiaoAi context: {content}"
	if agent.Config.ContextTemplate != wantContext {
		t.Errorf("ContextTemplate = %q, want %q", agent.Config.ContextTemplate, wantContext)
	}
	// Scrubbing is text-only: other tenant config fields must stay as stored.
	if agent.Config.Temperature != 0.42 {
		t.Errorf("Temperature = %v, want 0.42", agent.Config.Temperature)
	}
	// The T01 localisation overlay must keep working alongside the scrub.
	if agent.Name != "XiaoAi Assistant" {
		t.Errorf("Name = %q, want XiaoAi Assistant", agent.Name)
	}
	if agent.Description != "XiaoAi workspace assistant" {
		t.Errorf("Description = %q, want XiaoAi workspace assistant", agent.Description)
	}
	if agent.Avatar != "brand.png" {
		t.Errorf("Avatar = %q, want brand.png", agent.Avatar)
	}
}

// T03 boundary guard: empty prompts stay empty (no accidental "XiaoAi"
// injection), and a built-in agent with no stored prompt must not gain one from
// the read path.
func TestApplyBuiltinAgentLocalizationKeepsEmptyPromptEmpty(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	agent := &CustomAgent{ID: testBuiltinBrandAgentID, TenantID: 1}
	ApplyBuiltinAgentLocalization(context.Background(), agent)

	if agent.Config.SystemPrompt != "" {
		t.Errorf("SystemPrompt = %q, want empty", agent.Config.SystemPrompt)
	}
	if agent.Config.ContextTemplate != "" {
		t.Errorf("ContextTemplate = %q, want empty", agent.Config.ContextTemplate)
	}
}
