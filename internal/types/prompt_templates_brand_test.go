package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// T07 prompt fixture: the four shipped prompt YAML files must open every
// persona prompt with the Chinese self-name (with the English name in
// parentheses) and carry exactly one matching identity rule. The assertions are
// structural (per template), not count-based, so adding or removing a template
// does not silently weaken the guard. This is a static guard on the shipped
// configuration, not on runtime code.
type t07Template struct {
	ID      string `yaml:"id"`
	Content string `yaml:"content"`
}

type t07PromptFile struct {
	Templates []t07Template `yaml:"templates"`
}

const (
	t07GenericRule   = "When replying in Chinese, always call yourself 小爱; never 小艾. When replying in English, use XiaoAi."
	t07AssistantRule = "When replying in Chinese, always call yourself 小爱助手; never 小艾. When replying in English, use XiaoAi Assistant."
)

func readT07PromptFile(t *testing.T, name string) (t07PromptFile, string) {
	t.Helper()
	path := filepath.Join("..", "..", "config", "prompt_templates", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var file t07PromptFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file, string(raw)
}

func isT07BrandOpening(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "You are ") {
		return false
	}
	return strings.Contains(trimmed, "小爱 (XiaoAi)") || strings.Contains(trimmed, "小爱助手 (XiaoAi Assistant)")
}

// Prompt YAML structural guard (T07): every persona template opens with the
// 小爱 brand self-name and carries exactly one matching identity rule; the
// skill_installer shell prompt is not a persona; fixed-response fallback
// templates carry neither. YAML must still parse (readT07PromptFile fatals).
func TestPromptTemplatesUseXiaoAiBrandSelfName(t *testing.T) {
	personaFiles := []string{"system_prompt.yaml", "intent_prompts.yaml"}
	for _, name := range personaFiles {
		file, raw := readT07PromptFile(t, name)
		assertNoBarePromptBrand(t, name, raw)
		if len(file.Templates) == 0 {
			t.Fatalf("%s: parsed zero templates", name)
		}
		for _, template := range file.Templates {
			assertPersonaPrompt(t, name, template)
		}
	}

	// agent_system_prompt.yaml: every template is a persona except skill_installer,
	// which is an internal shell workflow prompt (brand opening, no identity rule).
	agentFile, agentRaw := readT07PromptFile(t, "agent_system_prompt.yaml")
	assertNoBarePromptBrand(t, "agent_system_prompt.yaml", agentRaw)
	if len(agentFile.Templates) == 0 {
		t.Fatal("agent_system_prompt.yaml: parsed zero templates")
	}
	personas := 0
	for _, template := range agentFile.Templates {
		if template.ID == "skill_installer" {
			if !t07HasBrandOpening(template.Content) {
				t.Errorf("agent_system_prompt.yaml/skill_installer: brand opening missing")
			}
			if strings.Contains(template.Content, "When replying in Chinese, always call yourself") {
				t.Errorf("agent_system_prompt.yaml/skill_installer: must not carry a persona identity rule")
			}
			continue
		}
		assertPersonaPrompt(t, "agent_system_prompt.yaml", template)
		personas++
	}
	if personas == 0 {
		t.Fatal("agent_system_prompt.yaml: no persona prompts found")
	}

	// fallback.yaml: only the model fallback prompt is a persona; fixed-response
	// templates carry no brand opening and no identity rule.
	fallbackFile, fallbackRaw := readT07PromptFile(t, "fallback.yaml")
	assertNoBarePromptBrand(t, "fallback.yaml", fallbackRaw)
	if len(fallbackFile.Templates) == 0 {
		t.Fatal("fallback.yaml: parsed zero templates")
	}
	foundFallbackPersona := false
	for _, template := range fallbackFile.Templates {
		if template.ID == "default_fallback_prompt" {
			assertPersonaPrompt(t, "fallback.yaml", template)
			foundFallbackPersona = true
			continue
		}
		if t07HasBrandOpening(template.Content) {
			t.Errorf("fallback.yaml/%s: fixed-response template must not open as a persona", template.ID)
		}
		if strings.Contains(template.Content, "always call yourself") {
			t.Errorf("fallback.yaml/%s: fixed-response template must not carry an identity rule", template.ID)
		}
	}
	if !foundFallbackPersona {
		t.Fatal("fallback.yaml: default_fallback_prompt not found")
	}
}

func assertNoBarePromptBrand(t *testing.T, name, raw string) {
	t.Helper()
	if strings.Contains(raw, "You are XiaoAi") {
		t.Errorf("%s: still contains a bare \"You are XiaoAi\" opening", name)
	}
	if strings.Contains(raw, "developed by Tencent") {
		t.Errorf("%s: still contains \"developed by Tencent\"", name)
	}
	if strings.Contains(raw, "小爱  (XiaoAi)") || strings.Contains(raw, "You are  小爱") {
		t.Errorf("%s: brand replacement introduced a double space", name)
	}
}

func t07HasBrandOpening(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if isT07BrandOpening(line) {
			return true
		}
	}
	return false
}

func assertPersonaPrompt(t *testing.T, file string, template t07Template) {
	t.Helper()
	if !t07HasBrandOpening(template.Content) {
		t.Errorf("%s/%s: persona prompt does not open with the 小爱 brand self-name", file, template.ID)
	}
	generic := strings.Count(template.Content, t07GenericRule)
	assistant := strings.Count(template.Content, t07AssistantRule)
	if template.ID == "weknora_assistant" {
		if generic != 0 || assistant != 1 {
			t.Errorf("%s/%s: assistant identity rule = %d, generic = %d; want assistant=1 generic=0",
				file, template.ID, assistant, generic)
		}
	} else if generic != 1 || assistant != 0 {
		t.Errorf("%s/%s: identity rule generic = %d, assistant = %d; want generic=1 assistant=0",
			file, template.ID, generic, assistant)
	}
	if got := strings.Count(template.Content, "小艾"); got != 1 {
		t.Errorf("%s/%s: 小艾 occurrences = %d, want 1 (inside the identity rule)", file, template.ID, got)
	}
}

// The skill_installer prompt is a shell workflow prompt, not a persona: it
// carries the brand name in the opening but no "who are you" identity rule.
func TestPromptTemplatesSkillInstallerHasNoIdentityRule(t *testing.T) {
	file, _ := readT07PromptFile(t, "agent_system_prompt.yaml")
	var content string
	for _, template := range file.Templates {
		if template.ID == "skill_installer" {
			content = template.Content
			break
		}
	}
	if content == "" {
		t.Fatal("skill_installer template not found")
	}
	if !strings.Contains(content, "You are the 小爱 (XiaoAi) Skill Installer.") {
		t.Errorf("skill_installer opening not brand-unified: %q", firstLine(content))
	}
	if strings.Contains(content, "When replying in Chinese, always call yourself") {
		t.Error("skill_installer must not carry a persona identity rule")
	}
}

// Every occurrence of 小艾 must sit inside a "never 小艾" rule. This is checked
// per template instead of against a global total, so adding a template cannot
// hide a free-standing 小艾 elsewhere.
func TestPromptTemplatesNeverUseXiaoAiShortName(t *testing.T) {
	for _, name := range []string{
		"system_prompt.yaml",
		"intent_prompts.yaml",
		"fallback.yaml",
		"agent_system_prompt.yaml",
	} {
		file, _ := readT07PromptFile(t, name)
		for _, template := range file.Templates {
			got := strings.Count(template.Content, "小艾")
			inRules := strings.Count(template.Content, "never 小艾")
			if got != inRules {
				t.Errorf("%s/%s: 小艾 occurrences = %d, but only %d are inside a \"never 小艾\" rule",
					name, template.ID, got, inRules)
			}
		}
	}
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
