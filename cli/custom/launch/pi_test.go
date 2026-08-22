package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiModelsJSON(t *testing.T) {
	config, err := BuildPiModelsJSON("https://api.orq.ai/v3/router",
		[]string{"anthropic/claude-sonnet-4-6", "openai/gpt-5-mini"},
		[]ModelInfo{{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"providers"`,
		`"baseUrl": "https://api.orq.ai/v3/router"`,
		`"apiKey": "$ORQ_API_KEY"`,
		`"api": "openai-completions"`,
		`"id": "anthropic/claude-sonnet-4-6"`,
		`"contextWindow": 200000`,
		`"maxTokens": 64000`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("missing %s in:\n%s", want, config)
		}
	}
	// gpt-5-mini has no metadata: fallback caps apply.
	if !strings.Contains(config, `"contextWindow": 128000`) {
		t.Fatalf("fallback context window missing:\n%s", config)
	}
}

func TestPiModelsJSONResponsesOverride(t *testing.T) {
	config, err := BuildPiModelsJSON("https://api.orq.ai/v3/router",
		[]string{"openai/gpt-5-mini", "anthropic/claude-haiku-4-5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `"api": "openai-responses"`) {
		t.Fatalf("openai model must carry the per-model responses override:\n%s", config)
	}
	// anthropic model uses the provider default: exactly one per-model api
	// override expected.
	if strings.Count(config, `"api": "openai-responses"`) != 1 {
		t.Fatalf("expected exactly one responses override:\n%s", config)
	}
}

func TestPiSessionGetsSkillsInItsTempAgentDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	if err := writeSessionSkills(dir); err != nil {
		t.Fatalf("writeSessionSkills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills written: %v %d", err, len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", entries[0].Name(), "SKILL.md")); err != nil {
		t.Errorf("skill not readable: %v", err)
	}
}

func TestSessionSkillsAreSuppressedByNoSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	ctx := &AgentContext{Flags: GatewayFlags{NoSkills: true}, Getenv: func(string) string { return "" }}
	if err := maybeWriteSessionSkills(ctx, dir); err != nil {
		t.Fatalf("maybeWriteSessionSkills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
		t.Error("--no-skills still wrote skills")
	}
}
