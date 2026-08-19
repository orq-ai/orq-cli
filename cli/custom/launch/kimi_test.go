package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var kimiInfos = []ModelInfo{
	{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000},
	{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000, SupportsResponses: true},
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

func TestKimiTOMLOmitsDefaultModelWhenUnset(t *testing.T) {
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", "",
		[]string{"anthropic/claude-sonnet-4-6"}, nil)
	if strings.Contains(toml, "default_model") {
		t.Fatalf("default_model written despite no model being named:\n%s", toml)
	}
	if !strings.HasPrefix(toml, "[providers.orq]") {
		t.Fatalf("block must open on a table so it can be appended to a config:\n%s", toml)
	}
}

func TestKimiTOMLFallbackCaps(t *testing.T) {
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", "anthropic/claude-sonnet-4-6",
		[]string{"anthropic/claude-sonnet-4-6"}, nil)
	if !strings.Contains(toml, "max_context_size = 128000") || !strings.Contains(toml, "max_output_size = 8192") {
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
	if home == "" || plan.Env["ORQ_API_KEY"] != "sk-test" {
		t.Fatalf("env: %v", plan.Env)
	}
	if _, ok := plan.Env["OPENAI_API_KEY"]; ok {
		t.Fatal("OPENAI_API_KEY must not be exported; kimi ignores it and it reads as a leaked OpenAI credential")
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

// The provider split follows metadata.supports_responses_api, not the "openai/"
// prefix. In a real workspace the prefix missed 13 of 25 Responses-capable
// models — every azure/gpt-*, the openai autorouters, and several third-party
// models — parking them on chat completions and losing the tools+reasoning path.
func TestKimiSplitsOnCatalogueMetadataNotPrefix(t *testing.T) {
	infos := []ModelInfo{
		{ID: "azure/gpt-5.4", ContextWindow: 400000, MaxOutputTokens: 128000, SupportsResponses: true},
		{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000},
	}
	toml := BuildKimiConfigTOML("https://api.orq.ai/v3/router", "sk-test-key", "azure/gpt-5.4",
		[]string{"azure/gpt-5.4", "anthropic/claude-sonnet-4-6"}, infos)

	// Assert on each model's own provider line: the [providers.*] headers all
	// precede the [models.*] blocks, so a substring search from a provider
	// header would match every model that follows it.
	providerOf := func(model string) string {
		block := toml[strings.Index(toml, `[models."`+model+`"]`):]
		line := block[strings.Index(block, "provider = ")+len("provider = "):]
		return strings.Trim(line[:strings.Index(line, "\n")], `"`)
	}
	if got := providerOf("azure/gpt-5.4"); got != KimiResponsesProvider {
		t.Errorf("azure/gpt-5.4 is Responses-capable but landed on %q:\n%s", got, toml)
	}
	if got := providerOf("anthropic/claude-sonnet-4-6"); got != KimiChatProvider {
		t.Errorf("a chat-only model landed on %q:\n%s", got, toml)
	}
}

// A model absent from the catalogue (--model, --models, a built-in default)
// has no metadata, so the prefix heuristic still decides. It is deliberately
// under-inclusive: chat completions serves every model, the reverse does not.
func TestResponsesModelSetFallsBackForUnknownModels(t *testing.T) {
	isResponses := ResponsesModelSet([]ModelInfo{{ID: "azure/gpt-5.4", SupportsResponses: true}})
	if !isResponses("azure/gpt-5.4") {
		t.Error("catalogue entry ignored")
	}
	if !isResponses("openai/gpt-5-mini") {
		t.Error("unknown openai/ model should fall back to the prefix heuristic")
	}
	if isResponses("anthropic/claude-sonnet-4-6") {
		t.Error("unknown non-openai model must default to chat")
	}
}
