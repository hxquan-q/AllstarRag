package types

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// T03 票级复跑边界：红用例只覆盖英文品牌短语，这里锁中文短语、替换顺序、
// 小写技术标识与空串。
func TestScrubBuiltinAgentBrandWords(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"英文长短短语与单独品牌", "WeKnora Assistant on WeKnora Cloud, then WeKnora.", "XiaoAi Assistant on XiaoAi Cloud, then XiaoAi."},
		{"中文品牌短语", "WeKnora 助手管理 WeKnora 知识库，WeKnora 云服务同步。", "小爱助手管理 小爱知识库，小爱云同步。"},
		{"小写技术标识不动", "aud=weknora search_weknora_docs .weknora/requirements.json", "aud=weknora search_weknora_docs .weknora/requirements.json"},
		{"空串保持空", "", ""},
		{"不重复替换", "WeKnora Cloud Assistant", "XiaoAi Cloud Assistant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubBuiltinAgentBrandWords(tc.in); got != tc.want {
				t.Fatalf("scrubBuiltinAgentBrandWords(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// T03 票级复跑：无 DB 记录的内置 agent 直接读 YAML。这里直接解析随仓库
// 发布的配置，锁 5 个语言的名称/描述与保留的技术标识。
func TestBuiltinAgentYAMLHasNoVisibleWeKnoraBrand(t *testing.T) {
	agentsData, err := os.ReadFile(filepath.Join("..", "..", "config", "builtin_agents.yaml"))
	if err != nil {
		t.Fatalf("read builtin_agents.yaml: %v", err)
	}
	var file builtinAgentsFile
	if err := yaml.Unmarshal(agentsData, &file); err != nil {
		t.Fatalf("parse builtin_agents.yaml: %v", err)
	}
	var entry *BuiltinAgentEntry
	for i := range file.BuiltinAgents {
		if file.BuiltinAgents[i].ID == "builtin-weknora-assistant" {
			entry = &file.BuiltinAgents[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("builtin-weknora-assistant not found in YAML")
	}
	if entry.Config.SystemPromptID != "weknora_assistant" {
		t.Errorf("system_prompt_id = %q, want weknora_assistant (technical id must stay)", entry.Config.SystemPromptID)
	}
	wantNames := map[string]string{
		"default": "XiaoAi Assistant",
		"zh-CN":   "小爱助手",
		"zh-TW":   "小愛助手",
		"ja-JP":   "XiaoAi アシスタント",
		"ko-KR":   "XiaoAi 어시스턴트",
	}
	for locale, want := range wantNames {
		got, ok := entry.I18n[locale]
		if !ok {
			t.Errorf("locale %s missing", locale)
			continue
		}
		if got.Name != want {
			t.Errorf("%s name = %q, want %q", locale, got.Name, want)
		}
		if got.Description == "" {
			t.Errorf("%s description is empty", locale)
		}
		if strings.Contains(got.Description, "WeKnora") {
			t.Errorf("%s description still contains WeKnora: %q", locale, got.Description)
		}
	}

	promptData, err := os.ReadFile(filepath.Join("..", "..", "config", "prompt_templates", "agent_system_prompt.yaml"))
	if err != nil {
		t.Fatalf("read agent_system_prompt.yaml: %v", err)
	}
	if strings.Contains(string(promptData), "WeKnora") {
		t.Error("agent_system_prompt.yaml still contains visible WeKnora brand word")
	}
}
