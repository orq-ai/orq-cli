package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiModelsJSON(t *testing.T) {
	config, err := BuildPiModelsJSON("https://api.orq.ai/v3/router", "$ORQ_API_KEY",
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
	config, err := BuildPiModelsJSON("https://api.orq.ai/v3/router", "$ORQ_API_KEY",
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

func TestResolvePiSessionConfig(t *testing.T) {
	ctx := sessionSkillsCtx(GatewayFlags{MCP: true, NoSkills: true})
	ctx.Getenv = env(map[string]string{
		"PI_MODEL":        "openai/gpt-5-mini",
		"PI_MODELS":       "vendor/extra-model",
		"ORQ_PI_BASE_URL": "https://pi.example/router",
	})
	ctx.Fetch = fetcherOf(DefaultPiModel, "openai/gpt-5-mini")
	plan, err := resolvePi(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if got, want := strings.Join(plan.PreArgs, " "), "--provider orq --model openai/gpt-5-mini"; got != want {
		t.Fatalf("PreArgs = %q, want %q", got, want)
	}
	if got, want := planEnvKeys(plan), "ORQ_API_KEY,ORQ_SERVER,PI_CODING_AGENT_DIR"; got != want {
		t.Fatalf("env keys = %q, want %q", got, want)
	}
	if plan.Env["ORQ_API_KEY"] != "orq-key" || plan.Env["ORQ_SERVER"] != DefaultGatewayAPIBaseURL {
		t.Fatalf("env: %v", plan.Env)
	}
	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if !strings.HasPrefix(filepath.Base(dir), "orq-pi-") {
		t.Fatalf("dir %q is not an orq-pi- temp dir", dir)
	}
	if len(plan.TempDirs) != 1 || plan.TempDirs[0].HostPath != dir {
		t.Fatalf("TempDirs: %v", plan.TempDirs)
	}
	if got := strings.Join(dirEntries(t, dir), ","); got != "models.json" {
		t.Fatalf("session entries = %q, want only models.json (pi takes no MCP)", got)
	}
	config, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"apiKey": "$ORQ_API_KEY"`, `"baseUrl": "https://pi.example/router"`, "vendor/extra-model"} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("missing %s in:\n%s", want, config)
		}
	}
}
