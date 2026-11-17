package types

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// YAML data structures for config/builtin_agents.yaml
// ---------------------------------------------------------------------------

// BuiltinAgentI18n holds localised name and description for a single locale.
type BuiltinAgentI18n struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// BuiltinAgentEntry is one entry in the builtin_agents list in YAML.
type BuiltinAgentEntry struct {
	ID        string                      `yaml:"id"`
	Avatar    string                      `yaml:"avatar"`
	IsBuiltin bool                        `yaml:"is_builtin"`
	I18n      map[string]BuiltinAgentI18n `yaml:"i18n"`
	Config    CustomAgentConfig           `yaml:"config"`
}

// builtinAgentsFile is the top-level YAML structure.
type builtinAgentsFile struct {
	BuiltinAgents []BuiltinAgentEntry `yaml:"builtin_agents"`
}

// ---------------------------------------------------------------------------
// Global registry (populated from YAML at startup)
// ---------------------------------------------------------------------------

var (
	builtinAgentEntries     map[string]*BuiltinAgentEntry // keyed by agent ID
	builtinAgentEntriesMu   sync.RWMutex
	builtinAgentEntriesOnce sync.Once
)

// LoadBuiltinAgentsConfig loads built-in agent definitions from the given
// config directory (e.g. "./config"). The file must be named "builtin_agents.yaml".
// This should be called once at startup, after config.LoadConfig determines
// the config directory.
//
// If the file does not exist, the function is a no-op and the hard-coded
// defaults in BuiltinAgentRegistry remain effective.
func LoadBuiltinAgentsConfig(configDir string) error {
	var loadErr error
	builtinAgentEntriesOnce.Do(func() {
		filePath := filepath.Join(configDir, "builtin_agents.yaml")
		data, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				// File not found – perfectly fine, keep using hard-coded defaults.
				return
			}
			loadErr = fmt.Errorf("read builtin_agents.yaml: %w", err)
			return
		}

		var file builtinAgentsFile
		if err := yaml.Unmarshal(data, &file); err != nil {
			loadErr = fmt.Errorf("parse builtin_agents.yaml: %w", err)
			return
		}

		builtinAgentEntriesMu.Lock()
		defer builtinAgentEntriesMu.Unlock()

		builtinAgentEntries = make(map[string]*BuiltinAgentEntry, len(file.BuiltinAgents))
		for i := range file.BuiltinAgents {
			entry := &file.BuiltinAgents[i]
			builtinAgentEntries[entry.ID] = entry
		}

		// Rebuild the BuiltinAgentRegistry so that IsBuiltinAgentID / GetBuiltinAgent
		// continue to work transparently.
		rebuildRegistryFromConfig()
	})
	return loadErr
}

// rebuildRegistryFromConfig replaces the BuiltinAgentRegistry entries with
// factory functions that read from the YAML-loaded config. Must be called
// while builtinAgentEntriesMu is held.
func rebuildRegistryFromConfig() {
	for id := range builtinAgentEntries {
		agentID := id // capture for closure
		BuiltinAgentRegistry[agentID] = func(tenantID uint64) *CustomAgent {
			return buildAgentFromEntry(agentID, tenantID, "")
		}
	}
}

// ---------------------------------------------------------------------------
// Public API — context-aware, i18n-capable
// ---------------------------------------------------------------------------

// GetBuiltinAgentWithContext returns a built-in agent whose Name and
// Description are localised according to the language in ctx.
// Falls back to GetBuiltinAgent (default locale) when no YAML config is loaded.
func GetBuiltinAgentWithContext(ctx context.Context, id string, tenantID uint64) *CustomAgent {
	locale := localeFromCtx(ctx)

	builtinAgentEntriesMu.RLock()
	entry, ok := builtinAgentEntries[id]
	builtinAgentEntriesMu.RUnlock()

	if !ok || entry == nil {
		// No YAML entry — fall back to hard-coded factory.
		if factory, exists := BuiltinAgentRegistry[id]; exists {
			return factory(tenantID)
		}
		return nil
	}

	return buildAgentFromEntry(id, tenantID, locale)
}

// ApplyBuiltinAgentLocalization overlays the locale-specific name, description,
// and avatar from builtin_agents.yaml onto a persisted built-in agent.
// Other DB fields are left untouched; the persisted Config.SystemPrompt and
// Config.ContextTemplate of built-in agents are brand-word scrubbed on the read
// path (text only, never written back).
//
// Built-in agents are seeded into the database with the locale that was active
// at first run. ListAgents / GetAgentByID used to return those stored strings
// verbatim, so switching the UI language left the cards in Chinese (or
// whichever language was seeded). YAML already has per-locale copy; this
// applies it on the read path.
func ApplyBuiltinAgentLocalization(ctx context.Context, agent *CustomAgent) {
	if agent == nil {
		return
	}
	localized := GetBuiltinAgentWithContext(ctx, agent.ID, agent.TenantID)
	if localized == nil {
		return
	}
	if localized.Name != "" {
		agent.Name = localized.Name
	}
	if localized.Description != "" {
		agent.Description = localized.Description
	}
	agent.Avatar = localized.Avatar

	// 改名前置入数据库的内置 agent 提示词可能仍自称 WeKnora/XiaoAi。读路径
	// 不写库;但编辑器保存会写回,所以每条替换必须幂等。非内置 agent 走不到
	// 这里(GetBuiltinAgentWithContext 对它们返回 nil)。
	agent.Config.SystemPrompt = scrubBuiltinAgentBrandWords(agent.Config.SystemPrompt)
	// 旧提示词可能没有对应身份规则,补一行;已含同一条 rule 则跳过,保证幂等。
	// 只补 SystemPrompt,ContextTemplate 保持不变。
	rule := builtinAgentIdentityRule
	if agent.ID == BuiltinWeKnoraAssistantID {
		rule = builtinAssistantIdentityRule
	}
	if p := agent.Config.SystemPrompt; p != "" && !strings.Contains(p, rule) {
		agent.Config.SystemPrompt = p + "\n" + rule
	}
	agent.Config.ContextTemplate = scrubBuiltinAgentBrandWords(agent.Config.ContextTemplate)
}

// 读路径补写的身份规则。旧提示词里没有「禁止小艾」约束时追加,避免模型
// 中文回答自称小艾。内置助手(小爱助手)用专用版本。
const (
	builtinAgentIdentityRule     = "When replying in Chinese, always call yourself 小爱; never 小艾. When replying in English, use XiaoAi."
	builtinAssistantIdentityRule = "When replying in Chinese, always call yourself 小爱助手; never 小艾. When replying in English, use XiaoAi Assistant."
)

// builtinAgentBrandReplacements 按顺序执行:先替换较长的品牌短语,最后才换
// 单独的 "WeKnora",避免 "WeKnora Cloud" 被替换成半截。只匹配品牌词精确
// 大小写;小写 weknora 技术标识(aud=weknora、工具名、URL)和其它字符不动。
var builtinAgentBrandReplacements = [][2]string{
	{"developed by Tencent", "developed internally by Allstar"},
	{"You are the XiaoAi Assistant", "You are 小爱助手 (XiaoAi Assistant)"},
	{"You are XiaoAi Assistant", "You are 小爱助手 (XiaoAi Assistant)"},
	{"You are XiaoAi", "You are 小爱 (XiaoAi)"},
	{"You are the XiaoAi", "You are the 小爱 (XiaoAi)"},
	{"XiaoAi Cloud", "小爱云"},
	{"WeKnora Cloud", "小爱云"},
	{"WeKnora Assistant", "小爱助手"},
	{"WeKnora 助手", "小爱助手"},
	{"WeKnora 云服务", "小爱云"},
	{"WeKnora 知识库", "小爱知识库"},
	{"WeKnora", "小爱 (XiaoAi)"},
}

// scrubBuiltinAgentBrandWords 净化内置 agent 提示词中的品牌词;空串保持空串。
// URL 与技术标识先换成占位符,品牌词替换完再原样还原——租户改过的提示词里
// 链接、HTTP 头和云端/存储标识都不能因为品牌改名而被改写。
func scrubBuiltinAgentBrandWords(s string) string {
	if s == "" {
		return s
	}
	protected := make([]string, 0, 4)
	masked := builtinAgentProtectedTokens.ReplaceAllStringFunc(s, func(token string) string {
		protected = append(protected, token)
		return builtinAgentTokenPlaceholder(len(protected) - 1)
	})
	for _, pair := range builtinAgentBrandReplacements {
		masked = strings.ReplaceAll(masked, pair[0], pair[1])
	}
	for i, token := range protected {
		masked = strings.ReplaceAll(masked, builtinAgentTokenPlaceholder(i), token)
	}
	return masked
}

// builtinAgentProtectedTokens matches the technical identifiers that must stay
// byte-for-byte: URLs, X-WeKnora-* headers, WeKnoraCloud* API names,
// WeKnora_theme / WeKnora_settings storage keys, the embed script name and the
// lower-case audience parameter. Ordering matters only for the leftmost match.
var builtinAgentProtectedTokens = regexp.MustCompile(
	`https?://[\x21-\x7E]+` +
		`|X-WeKnora-[A-Za-z0-9-]*` +
		`|WeKnoraCloud[A-Za-z0-9_]*` +
		`|WeKnora_[A-Za-z0-9_]+` +
		`|weknora-widget\.js` +
		`|aud=weknora`)

// builtinAgentTokenPlaceholder builds a sentinel that prompt text cannot
// contain; the index keeps repeated tokens apart.
func builtinAgentTokenPlaceholder(index int) string {
	return fmt.Sprintf("\x00builtin-brand-token-%d\x00", index)
}

var builtinAgentEntriesTestMu sync.Mutex

// OverrideBuiltinAgentEntriesForTest replaces the in-memory YAML registry.
// Tests in other packages use this to assert locale overlays; call the
// returned func to restore the previous map. Concurrent tests are serialized
// so they do not clobber each other's entries.
func OverrideBuiltinAgentEntriesForTest(entries map[string]*BuiltinAgentEntry) func() {
	builtinAgentEntriesTestMu.Lock()
	builtinAgentEntriesMu.Lock()
	prev := builtinAgentEntries
	builtinAgentEntries = entries
	builtinAgentEntriesMu.Unlock()
	return func() {
		builtinAgentEntriesMu.Lock()
		builtinAgentEntries = prev
		builtinAgentEntriesMu.Unlock()
		builtinAgentEntriesTestMu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildAgentFromEntry constructs a *CustomAgent from a BuiltinAgentEntry.
// locale can be "" to use the "default" locale.
func buildAgentFromEntry(id string, tenantID uint64, locale string) *CustomAgent {
	builtinAgentEntriesMu.RLock()
	entry, ok := builtinAgentEntries[id]
	builtinAgentEntriesMu.RUnlock()

	if !ok || entry == nil {
		return nil
	}

	i18n := resolveI18n(entry.I18n, locale)

	agent := &CustomAgent{
		ID:          entry.ID,
		Name:        i18n.Name,
		Description: i18n.Description,
		Avatar:      entry.Avatar,
		IsBuiltin:   entry.IsBuiltin,
		TenantID:    tenantID,
		Config:      entry.Config, // value copy
	}
	agent.EnsureDefaults()
	return agent
}

// resolveI18n picks the best locale match from the i18n map.
// Priority: exact match → language-only match → "default" → first entry.
func resolveI18n(m map[string]BuiltinAgentI18n, locale string) BuiltinAgentI18n {
	if len(m) == 0 {
		return BuiltinAgentI18n{}
	}

	// 1. Exact match  (e.g. "zh-CN")
	if v, ok := m[locale]; ok {
		return v
	}

	// 2. Language-only match (e.g. "zh-CN" → try "zh")
	if idx := strings.IndexAny(locale, "-_"); idx > 0 {
		lang := locale[:idx]
		if v, ok := m[lang]; ok {
			return v
		}
		// Also try matching entries that start with the same language prefix
		for k, v := range m {
			if strings.HasPrefix(k, lang) {
				return v
			}
		}
	}

	// 3. "default" key
	if v, ok := m["default"]; ok {
		return v
	}

	// 4. First available entry
	for _, v := range m {
		return v
	}
	return BuiltinAgentI18n{}
}

// localeFromCtx extracts the locale string from ctx, falling back to "".
func localeFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	lang, _ := LanguageFromContext(ctx)
	return lang
}

// ResolveBuiltinAgentPromptRefs iterates over all builtin agent entries and
// resolves system_prompt_id / context_template_id references by calling the
// provided resolver function.  The resolver takes a template ID and returns
// the template content string (empty string if not found).
//
// This must be called after both LoadBuiltinAgentsConfig and prompt template
// loading have completed.
func ResolveBuiltinAgentPromptRefs(resolver func(id string) string) {
	builtinAgentEntriesMu.Lock()
	defer builtinAgentEntriesMu.Unlock()

	for _, entry := range builtinAgentEntries {
		if entry == nil {
			continue
		}
		// Resolve system_prompt_id → SystemPrompt
		if entry.Config.SystemPromptID != "" && entry.Config.SystemPrompt == "" {
			if content := resolver(entry.Config.SystemPromptID); content != "" {
				entry.Config.SystemPrompt = content
			} else {
				fmt.Printf("Warning: builtin agent %q references system_prompt_id %q but template not found\n",
					entry.ID, entry.Config.SystemPromptID)
			}
		}
		// Resolve context_template_id → ContextTemplate
		if entry.Config.ContextTemplateID != "" && entry.Config.ContextTemplate == "" {
			if content := resolver(entry.Config.ContextTemplateID); content != "" {
				entry.Config.ContextTemplate = content
			} else {
				fmt.Printf("Warning: builtin agent %q references context_template_id %q but template not found\n",
					entry.ID, entry.Config.ContextTemplateID)
			}
		}
	}
}
