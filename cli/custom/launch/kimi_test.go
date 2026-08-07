package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var kimiInfos = []ModelInfo{
	{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000},
	{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000},
}

func TestKimiTOMLProvidersAndCaps(t *testing.T) {
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", "openai/gpt-5-mini",
		[]string{"anthropic/claude-sonnet-4-6", "openai/gpt-5-mini"}, kimiInfos)

	for _, want := range []string{
		`default_model = "openai/gpt-5-mini"`,
		"[providers.orq]",
		`type = "openai"`,
		"[providers.orq-responses]",
		`type = "openai_responses"`,
		`[models."anthropic/claude-sonnet-4-6"]`,
		"max_context_size = 200000",
		"max_output_size = 64000",
		`base_url = "https://api.orq.ai/v3/router"`,
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("missing %q in:\n%s", want, toml)
		}
	}
	// Kimi resolves credentials from the file only; the key must be present.
	if !strings.Contains(toml, `api_key = "sk-test-key"`) {
		t.Fatal("providers must declare the literal api_key")
	}
	responsesBlock := toml[strings.Index(toml, `[models."openai/gpt-5-mini"]`):]
	if !strings.Contains(responsesBlock, `provider = "orq-responses"`) {
		t.Fatalf("openai model not routed to responses provider:\n%s", responsesBlock)
	}
}

func TestKimiTOMLFallbackCaps(t *testing.T) {
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", "anthropic/claude-sonnet-4-6",
		[]string{"anthropic/claude-sonnet-4-6"}, nil)
	if !strings.Contains(toml, "max_context_size = 262144") || !strings.Contains(toml, "max_output_size = 8192") {
		t.Fatalf("fallback caps missing:\n%s", toml)
	}
	if strings.Contains(toml, "[providers.orq-responses]") {
		t.Fatal("empty responses provider block should be omitted")
	}
}

func TestKimiTOMLEscaping(t *testing.T) {
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", `weird/a"b\c`, []string{`weird/a"b\c`}, nil)
	if !strings.Contains(toml, `[models."weird/a\"b\\c"]`) {
		t.Fatalf("escaping broken:\n%s", toml)
	}
}

func TestKimiResolvePlan(t *testing.T) {
	def := kimiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL},
		Getenv: env(nil),
		Flags:  GatewayFlags{},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return kimiInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	home := plan.Env["KIMI_CODE_HOME"]
	if home == "" || plan.Env["ORQ_API_KEY"] != "sk-test" ||
		plan.Env["OPENAI_API_KEY"] != "sk-test" ||
		plan.Env["OPENAI_BASE_URL"] != DefaultGatewayBaseURL {
		t.Fatalf("env: %v", plan.Env)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Kimi reads credentials from the file only, so the key must be there.
	if !strings.Contains(string(data), `api_key = "sk-test`) {
		t.Fatal("api_key missing from config.toml")
	}
	if !strings.Contains(string(data), "max_context_size = 200000") {
		t.Fatalf("metadata caps missing:\n%s", data)
	}

	plan.Cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove KIMI_CODE_HOME")
	}
}
