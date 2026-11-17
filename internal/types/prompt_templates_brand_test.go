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
// parentheses) and carry exactly one identity rule. This is a static guard on
// the shipped configuration, not on runtime code.
type t07PromptFile struct {
	Templates []struct {
		ID      string `yaml:"id"`
		Content string `yaml:"content"`
	} `yaml:"templates"`
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

// Prompt YAML static guard (T07): 22 brand openings (21 personas + the
// skill_installer shell prompt), 21 identity rules, no bare "You are XiaoAi"
// and no "developed by Tencent".
func TestPromptTemplatesUseXiaoAiBrandSelfName(t *testing.T) {
	files := []struct {
		name           string
		templates      int
		brandOpenings  int
		identityRules  int
		assistantRules int
	}{
		{"system_prompt.yaml", 6, 6, 6, 0},
		{"intent_prompts.yaml", 7, 7, 7, 0},
		{"fallback.yaml", 5, 1, 1, 0},
		{"agent_system_prompt.yaml", 8, 8, 6, 1},
	}

	totalOpenings := 0
	totalRules := 0
	for _, tc := range files {
		file, raw := readT07PromptFile(t, tc.name)
		if len(file.Templates) != tc.templates {
			t.Errorf("%s: templates = %d, want %d", tc.name, len(file.Templates), tc.templates)
		}
		if strings.Contains(raw, "You are XiaoAi") {
			t.Errorf("%s: still contains a bare \"You are XiaoAi\" opening", tc.name)
		}
		if strings.Contains(raw, "developed by Tencent") {
			t.Errorf("%s: still contains \"developed by Tencent\"", tc.name)
		}
		if strings.Contains(raw, "小爱  (XiaoAi)") || strings.Contains(raw, "You are  小爱") {
			t.Errorf("%s: brand replacement introduced a double space", tc.name)
		}

		openings, rules, assistantRules := 0, 0, 0
		for _, template := range file.Templates {
			for _, line := range strings.Split(template.Content, "\n") {
				if isT07BrandOpening(line) {
					openings++
				}
			}
			if strings.Contains(template.Content, t07GenericRule) {
				rules++
			}
			if strings.Contains(template.Content, t07AssistantRule) {
				assistantRules++
			}
		}
		if openings != tc.brandOpenings {
			t.Errorf("%s: brand openings = %d, want %d", tc.name, openings, tc.brandOpenings)
		}
		if rules != tc.identityRules {
			t.Errorf("%s: generic identity rules = %d, want %d", tc.name, rules, tc.identityRules)
		}
		if assistantRules != tc.assistantRules {
			t.Errorf("%s: assistant identity rules = %d, want %d", tc.name, assistantRules, tc.assistantRules)
		}
		totalOpenings += openings
		totalRules += rules + assistantRules
	}

	if totalOpenings != 22 {
		t.Errorf("total brand openings = %d, want 22 (21 personas + skill_installer)", totalOpenings)
	}
	if totalRules != 21 {
		t.Errorf("total identity rules = %d, want 21", totalRules)
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

// Every persona prompt (all brand openings except the installer) carries a
// "never 小艾" rule, so 小艾 may only appear inside those 21 rules.
func TestPromptTemplatesNeverUseXiaoAiShortName(t *testing.T) {
	total := 0
	for _, name := range []string{
		"system_prompt.yaml",
		"intent_prompts.yaml",
		"fallback.yaml",
		"agent_system_prompt.yaml",
	} {
		_, raw := readT07PromptFile(t, name)
		total += strings.Count(raw, "小艾")
	}
	if total != 21 {
		t.Errorf("occurrences of 小艾 = %d, want 21 (only inside \"never 小艾\" rules)", total)
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
