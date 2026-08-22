package launch

import (
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
