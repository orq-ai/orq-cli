package launch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToOpenCodeModel(t *testing.T) {
	if ToOpenCodeModel(IsResponsesModel, "openai/gpt-5-mini") != "orq-openai/openai/gpt-5-mini" {
		t.Fatal("openai should map to responses provider")
	}
	if ToOpenCodeModel(IsResponsesModel, "anthropic/claude-sonnet-4-6") != "orq/anthropic/claude-sonnet-4-6" {
		t.Fatal("non-openai should map to chat provider")
	}
	if ToOpenCodeModel(IsResponsesModel, "orq/anthropic/claude-sonnet-4-6") != "orq/anthropic/claude-sonnet-4-6" {
		t.Fatal("already-prefixed id should normalize first")
	}
}

func TestOpenCodeConfigSplitsProviders(t *testing.T) {
	content, err := BuildOpenCodeConfigContent(
		"https://api.orq.ai/v3/router",
		"openai/gpt-5-mini",
		[]string{"openai/gpt-5-mini", "anthropic/claude-sonnet-4-6"}, nil,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Schema   string `json:"$schema"`
		Provider map[string]struct {
			Npm     string                    `json:"npm"`
			Options map[string]string         `json:"options"`
			Models  map[string]map[string]any `json:"models"`
		} `json:"provider"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.Schema != "https://opencode.ai/config.json" {
		t.Fatalf("schema: %s", parsed.Schema)
	}
	chat := parsed.Provider["orq"]
	if chat.Npm != "@ai-sdk/openai-compatible" ||
		chat.Options["baseURL"] != "https://api.orq.ai/v3/router" ||
		chat.Options["apiKey"] != "{env:ORQ_API_KEY}" {
		t.Fatalf("chat provider: %+v", chat)
	}
	if _, ok := chat.Models["anthropic/claude-sonnet-4-6"]; !ok || len(chat.Models) != 1 {
		t.Fatalf("chat models: %v", chat.Models)
	}
	responses := parsed.Provider["orq-openai"]
	if responses.Npm != "@ai-sdk/openai" {
		t.Fatalf("responses provider: %+v", responses)
	}
	if _, ok := responses.Models["openai/gpt-5-mini"]; !ok || len(responses.Models) != 1 {
		t.Fatalf("responses models: %v", responses.Models)
	}
	if parsed.Model != "orq-openai/openai/gpt-5-mini" {
		t.Fatalf("model: %s", parsed.Model)
	}
}

func TestOpenCodeConfigOmitsEmptyProvider(t *testing.T) {
	content, _ := BuildOpenCodeConfigContent(
		"https://api.orq.ai/v3/router",
		"anthropic/claude-sonnet-4-6",
		[]string{"anthropic/claude-sonnet-4-6"}, nil,
		"",
	)
	if strings.Contains(content, "orq-openai") {
		t.Fatalf("responses provider should be absent: %s", content)
	}
}

func TestOpenCodeResolvePlan(t *testing.T) {
	def := opencodeAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{},
		Fetch:  fetcherOf("openai/gpt-5-mini"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["ORQ_API_KEY"] != "orq-key" {
		t.Fatalf("env: %v", plan.Env)
	}
	config := plan.Env["OPENCODE_CONFIG_CONTENT"]
	if config == "" || strings.Contains(config, "orq-key") {
		t.Fatalf("config missing or leaks key: %s", config)
	}
}

func TestKiloUsesOwnConfigEnvVar(t *testing.T) {
	def := kiloAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{},
		Fetch:  fetcherOf("openai/gpt-5-mini"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["KILO_CONFIG_CONTENT"] == "" {
		t.Fatalf("kilo config env missing: %v", plan.Env)
	}
	if _, present := plan.Env["OPENCODE_CONFIG_CONTENT"]; present {
		t.Fatal("kilo must not set the opencode env var")
	}
}
