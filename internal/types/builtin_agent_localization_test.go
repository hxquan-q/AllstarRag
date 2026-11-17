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

	// T07：品牌目标改为小爱系（英文长短语也走小爱助手/小爱云），
	// 单独 WeKnora 替换为「小爱 (XiaoAi)」；小写技术标识与 aud 原样保留。
	// T07-fix2：读路径在净化后为未带身份规则的持久化提示词补一行，
	// 避免模型中文回答继续自称「小艾」。该用例 ID 走通用（非助手）规则。
	const wantPrompt = "You are the 小爱助手. Deploy on 小爱云. " +
		"Keep lower: weknora. aud=weknora. End 小爱 (XiaoAi).\n" +
		"When replying in Chinese, always call yourself 小爱; never 小艾. When replying in English, use XiaoAi."
	if agent.Config.SystemPrompt != wantPrompt {
		t.Errorf("SystemPrompt = %q, want %q", agent.Config.SystemPrompt, wantPrompt)
	}
	const wantContext = "小爱 (XiaoAi) context: {content}"
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

// T07-fix2：DB 里已被 T03 改成 XiaoAi 的持久化内置 agent 提示词，读路径必须
// 映射回小爱并在末尾补身份规则，否则模型中文回答仍会自称「小艾」。
func TestApplyBuiltinAgentLocalizationRewritesPersistedXiaoAiPrompt(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	agent := &CustomAgent{
		ID:        testBuiltinBrandAgentID,
		IsBuiltin: true,
		TenantID:  1,
		Config: CustomAgentConfig{
			SystemPrompt: "You are XiaoAi, a knowledge-base question-answering assistant developed by Allstar. Help the user.",
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)

	const wantPrompt = "You are 小爱 (XiaoAi), a knowledge-base question-answering assistant developed by Allstar. Help the user.\n" +
		"When replying in Chinese, always call yourself 小爱; never 小艾. When replying in English, use XiaoAi."
	if agent.Config.SystemPrompt != wantPrompt {
		t.Fatalf("SystemPrompt = %q, want %q", agent.Config.SystemPrompt, wantPrompt)
	}
	if got := strings.Count(agent.Config.SystemPrompt, "never 小艾"); got != 1 {
		t.Fatalf("identity rule count = %d, want 1", got)
	}
	if strings.Contains(agent.Config.SystemPrompt, "You are XiaoAi") {
		t.Fatalf("persisted XiaoAi opening was not rewritten: %q", agent.Config.SystemPrompt)
	}
}

// T07-fix2：列表/详情都会走读路径，反复调用必须幂等，不重复追加身份规则。
func TestApplyBuiltinAgentLocalizationIdentityRuleIsIdempotent(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	agent := &CustomAgent{
		ID:        testBuiltinBrandAgentID,
		IsBuiltin: true,
		TenantID:  1,
		Config: CustomAgentConfig{
			SystemPrompt: "You are XiaoAi, an assistant developed by Allstar.",
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)
	first := agent.Config.SystemPrompt
	ApplyBuiltinAgentLocalization(context.Background(), agent)

	if agent.Config.SystemPrompt != first {
		t.Fatalf("second pass changed prompt: first %q, second %q", first, agent.Config.SystemPrompt)
	}
	if got := strings.Count(agent.Config.SystemPrompt, "never 小艾"); got != 1 {
		t.Fatalf("identity rule count after two passes = %d, want 1; prompt=%q", got, agent.Config.SystemPrompt)
	}
}

// T07-fix2/fix3：内置助手（builtin-weknora-assistant）用「小爱助手」专用身份规则。
// 用例输入取真实旧文本形态（带定冠词的 `You are the XiaoAi Assistant`），同时验证
// L1 映射与 fix3 的幂等 `XiaoAi Cloud` -> `小爱云`。
func TestApplyBuiltinAgentLocalizationAssistantUsesAssistantRule(t *testing.T) {
	restore := OverrideBuiltinAgentEntriesForTest(map[string]*BuiltinAgentEntry{
		BuiltinWeKnoraAssistantID: {
			ID:        BuiltinWeKnoraAssistantID,
			Avatar:    "assistant.png",
			IsBuiltin: true,
			I18n: map[string]BuiltinAgentI18n{
				"default": {Name: "XiaoAi Assistant", Description: "XiaoAi workspace assistant"},
			},
		},
	})
	t.Cleanup(restore)

	agent := &CustomAgent{
		ID:        BuiltinWeKnoraAssistantID,
		IsBuiltin: true,
		TenantID:  1,
		Config: CustomAgentConfig{
			SystemPrompt: "You are the XiaoAi Assistant. Deploy on XiaoAi Cloud. Help.",
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)

	const wantPrompt = "You are 小爱助手 (XiaoAi Assistant). Deploy on 小爱云. Help.\n" +
		"When replying in Chinese, always call yourself 小爱助手; never 小艾. When replying in English, use XiaoAi Assistant."
	if agent.Config.SystemPrompt != wantPrompt {
		t.Fatalf("SystemPrompt = %q, want %q", agent.Config.SystemPrompt, wantPrompt)
	}
	if strings.Contains(agent.Config.SystemPrompt, "You are the XiaoAi Assistant") {
		t.Fatalf("L1 opening was not rewritten: %q", agent.Config.SystemPrompt)
	}
	if strings.Contains(agent.Config.SystemPrompt, "always call yourself 小爱;") {
		t.Fatalf("assistant agent received the generic identity rule: %q", agent.Config.SystemPrompt)
	}
}

// T07-fix2：身份规则只补 SystemPrompt；空 SystemPrompt 保持空，ContextTemplate
// 只做品牌净化、绝不追加规则。
func TestApplyBuiltinAgentLocalizationRuleOnlyOnSystemPrompt(t *testing.T) {
	registerTestBuiltinBrandAgent(t)

	agent := &CustomAgent{
		ID:        testBuiltinBrandAgentID,
		IsBuiltin: true,
		TenantID:  1,
		Config: CustomAgentConfig{
			ContextTemplate: "WeKnora context: {content}",
		},
	}

	ApplyBuiltinAgentLocalization(context.Background(), agent)

	if agent.Config.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want empty", agent.Config.SystemPrompt)
	}
	if agent.Config.ContextTemplate != "小爱 (XiaoAi) context: {content}" {
		t.Fatalf("ContextTemplate = %q, want scrubbed template", agent.Config.ContextTemplate)
	}
	if strings.Contains(agent.Config.ContextTemplate, "never 小艾") {
		t.Fatalf("identity rule leaked into ContextTemplate: %q", agent.Config.ContextTemplate)
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
		{"英文长短短语与单独品牌", "WeKnora Assistant on WeKnora Cloud, then WeKnora.", "小爱助手 on 小爱云, then 小爱 (XiaoAi)."},
		{"中文品牌短语", "WeKnora 助手管理 WeKnora 知识库，WeKnora 云服务同步。", "小爱助手管理 小爱知识库，小爱云同步。"},
		{"小写技术标识不动", "aud=weknora search_weknora_docs .weknora/requirements.json", "aud=weknora search_weknora_docs .weknora/requirements.json"},
		{"空串保持空", "", ""},
		{"不重复替换", "WeKnora Cloud Assistant", "小爱云 Assistant"},
		// T07：厂商署名替换只作用于完整短语，单独出现的 Tencent 不在改写范围。
		{"厂商署名", "developed by Tencent stays; developed by Tencent.", "developed internally by Allstar stays; developed internally by Allstar."},
		// F4①：URL 保护只吃 ASCII 可见字符，中文逗号后的品牌词仍要替换。
		{"URL 在中文逗号处停止", "详见 https://a.com/x，WeKnora 助手会处理", "详见 https://a.com/x，小爱助手会处理"},
		// F4③：替换后不新增双空格。
		{"无双空格", "Hi WeKnora; 管理 WeKnora 知识库", "Hi 小爱 (XiaoAi); 管理 小爱知识库"},
		// F4②（用户已接受）：WeKnoraCloud 作为普通文字时仍按技术标识保护。
		{"WeKnoraCloud 普通文字保持保护", "WeKnoraCloud 凭证未配置", "WeKnoraCloud 凭证未配置"},
		// T07-fix2：DB 里已被 T03 改成 XiaoAi 的持久化提示词前缀也要映射回小爱。
		// T07-fix3 L1：真实旧文本是带定冠词的 `You are the XiaoAi Assistant`，必须
		// 先于 `You are the XiaoAi` 命中，映射为小爱助手而不是半截小爱。
		{"You are the XiaoAi Assistant 映射", "You are the XiaoAi Assistant. Help.", "You are 小爱助手 (XiaoAi Assistant). Help."},
		{"You are the XiaoAi 映射", "You are the XiaoAi. Help.", "You are the 小爱 (XiaoAi). Help."},
		// T07-fix3 M1：映射目标不再自带匹配串，二次净化不会套娃。
		{"XiaoAi Cloud 映射", "Deploy on XiaoAi Cloud.", "Deploy on 小爱云."},
		// 新增 XiaoAi 前缀替换不得触碰小写技术标识/HTTP 头/API 名/存储键。
		{
			"XiaoAi 前缀与技术标识共存",
			"You are XiaoAi. aud=weknora, X-WeKnora-Signature, WeKnoraCloud.BaseURL, WeKnora_theme.",
			"You are 小爱 (XiaoAi). aud=weknora, X-WeKnora-Signature, WeKnoraCloud.BaseURL, WeKnora_theme.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubBuiltinAgentBrandWords(tc.in)
			if got != tc.want {
				t.Fatalf("scrubBuiltinAgentBrandWords(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// T07-fix4 M-1：主表每条输入都跑两遍，锁住净化幂等；任何替换目标
			// 若再次包含匹配串，编辑器保存写回后会套娃，这里会直接变红。
			if again := scrubBuiltinAgentBrandWords(got); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// CP3 L1: 读路径净化只换品牌词。租户改过的内置 agent 提示词里若有 URL、
// HTTP 头、云端 API 名、localStorage 键等技术标识，必须原样保留；同一条
// 文本里其它位置的真实品牌词仍然替换。
func TestScrubBuiltinAgentBrandWordsProtectsTechnicalTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "URL 原样保留，URL 外品牌词仍替换",
			in:   "Read https://github.com/Tencent/WeKnora then ask WeKnora Assistant.",
			want: "Read https://github.com/Tencent/WeKnora then ask 小爱助手.",
		},
		{
			name: "HTTP 头原样保留",
			in:   "Sign with X-WeKnora-Signature and X-WeKnora-Desktop-Token. WeKnora verifies.",
			want: "Sign with X-WeKnora-Signature and X-WeKnora-Desktop-Token. 小爱 (XiaoAi) verifies.",
		},
		{
			name: "WeKnoraCloud API 名原样保留",
			in:   "Set WeKnoraCloud.BaseURL and WeKnoraCloudID, then describe WeKnora Cloud.",
			want: "Set WeKnoraCloud.BaseURL and WeKnoraCloudID, then describe 小爱云.",
		},
		{
			name: "localStorage 键原样保留",
			in:   "Keep WeKnora_theme and WeKnora_settings keys. Welcome to WeKnora.",
			want: "Keep WeKnora_theme and WeKnora_settings keys. Welcome to 小爱 (XiaoAi).",
		},
		{
			name: "embed 脚本名与 audience 原样保留",
			in:   "Load weknora-widget.js with aud=weknora. This is WeKnora.",
			want: "Load weknora-widget.js with aud=weknora. This is 小爱 (XiaoAi).",
		},
		{
			name: "重复技术标识逐个还原",
			in:   "https://a/WeKnora https://a/WeKnora WeKnora",
			want: "https://a/WeKnora https://a/WeKnora 小爱 (XiaoAi)",
		},
		{
			name: "普通品牌词仍替换",
			in:   "WeKnora、WeKnora Assistant、WeKnora Cloud、WeKnora 知识库",
			want: "小爱 (XiaoAi)、小爱助手、小爱云、小爱知识库",
		},
		{
			name: "空串保持空",
			in:   "",
			want: "",
		},
		{
			name: "小写技术标识不动",
			in:   "aud=weknora search_weknora_docs .weknora/requirements.json",
			want: "aud=weknora search_weknora_docs .weknora/requirements.json",
		},
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
