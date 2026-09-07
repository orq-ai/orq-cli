package launch

import (
	"encoding/json"
	"testing"
)

var copilotInfos = []ModelInfo{
	{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000},
	{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000, SupportsResponses: true},
}

func TestCopilotResolvePlan(t *testing.T) {
	def := copilotAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: true},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return copilotInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"COPILOT_PROVIDER_API_KEY": "sk-test",
		"COPILOT_PROVIDER_TYPE":    "openai",
		"ORQ_API_KEY":              "sk-test",
	}
	for k, v := range want {
		if got := plan.Env[k]; got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if plan.Env["COPILOT_PROVIDER_BASE_URL"] == "" {
		t.Error("COPILOT_PROVIDER_BASE_URL missing")
	}
	if plan.Env["COPILOT_MODEL"] == "" {
		t.Error("COPILOT_MODEL missing")
	}
	// DefaultCopilotModel is not in copilotInfos, so ResolveGatewayConfig
	// substitutes the first fetched model (anthropic/claude-sonnet-4-6),
	// which the catalogue marks as chat-only.
	if plan.Env["COPILOT_PROVIDER_WIRE_API"] != "completions" {
		t.Errorf("wire_api = %q, want completions for %q", plan.Env["COPILOT_PROVIDER_WIRE_API"], plan.Env["COPILOT_MODEL"])
	}
}

func TestCopilotWireAPIFollowsCatalogueMetadata(t *testing.T) {
	def := copilotAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{Model: "openai/gpt-5-mini"},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return copilotInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["COPILOT_MODEL"] != "openai/gpt-5-mini" {
		t.Fatalf("COPILOT_MODEL: %v", plan.Env["COPILOT_MODEL"])
	}
	if plan.Env["COPILOT_PROVIDER_WIRE_API"] != "responses" {
		t.Fatalf("wire_api should follow catalogue metadata.supports_responses_api: %v", plan.Env["COPILOT_PROVIDER_WIRE_API"])
	}
}

func TestCopilotNoMCP(t *testing.T) {
	def := copilotAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: false},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return copilotInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.PreArgs {
		if a == "--additional-mcp-config" {
			t.Fatal("--no-mcp must not add --additional-mcp-config")
		}
	}
}

func TestCopilotMCP(t *testing.T) {
	def := copilotAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: true},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return copilotInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, a := range plan.PreArgs {
		if a == "--additional-mcp-config" {
			found = true
			if i+1 >= len(plan.PreArgs) {
				t.Fatal("--additional-mcp-config has no value")
			}
			// Copilot rejects a bare name-keyed map with "mcpServers: Required",
			// so assert the wrapper rather than just the server name appearing
			// somewhere in the JSON.
			var payload struct {
				MCPServers map[string]struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(plan.PreArgs[i+1]), &payload); err != nil {
				t.Fatalf("MCP config is not valid JSON: %v", err)
			}
			server, ok := payload.MCPServers[MCPServerName]
			if !ok {
				t.Fatalf("mcpServers[%q] missing: %s", MCPServerName, plan.PreArgs[i+1])
			}
			if server.Type != "http" || server.URL == "" {
				t.Fatalf("server entry = %+v, want type http with a url", server)
			}
		}
	}
	if !found {
		t.Fatal("--additional-mcp-config missing with --mcp")
	}
}

// TestCopilotOnPremBaseURL guards the priority chain in ResolveGatewayConfig:
// a non-empty DefaultBaseURL sits ahead of deriveFromAPIBase, so setting it
// would silently send an on-prem customer's traffic to the public gateway.
func TestCopilotOnPremBaseURL(t *testing.T) {
	const onPrem = "https://orq.internal.example.com"
	def := copilotAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: onPrem, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return copilotInfos, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Env["COPILOT_PROVIDER_BASE_URL"]; got != onPrem+"/v3/router" {
		t.Fatalf("COPILOT_PROVIDER_BASE_URL = %q, want it derived from the session API base", got)
	}
}

func resolveCopilotWith(t *testing.T, fetch func(string, string) ([]ModelInfo, error)) *LaunchPlan {
	t.Helper()
	plan, err := copilotAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Fetch:  fetch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestCopilotSubstitutionIsAnnounced covers the warning itself, not just its
// downstream effect: TestCopilotResolvePlan exercises this path but asserts
// only the resulting wire_api, so dropping the warning would still pass there.
func TestCopilotSubstitutionIsAnnounced(t *testing.T) {
	plan := resolveCopilotWith(t, func(_, _ string) ([]ModelInfo, error) { return copilotInfos, nil })

	if !warningsContain(plan, "is not enabled in this workspace") {
		t.Fatalf("default %q is absent from the catalogue but no substitution warning fired: %v",
			DefaultCopilotModel, plan.Warnings)
	}
}

// TestCopilotSkillsAreDeclaredUnavailable pins the guard in
// maybeInstallSessionSkills. RES-1457 scopes skills wiring out, so copilot is
// registered in neither skills map; the point is that the launch says so
// rather than reporting success having linked nothing.
func TestCopilotSkillsAreDeclaredUnavailable(t *testing.T) {
	plan := resolveCopilotWith(t, func(_, _ string) ([]ModelInfo, error) { return copilotInfos, nil })

	if !warningsContain(plan, "orq skills are not wired for copilot") {
		t.Fatalf("expected a skills-unavailable warning, got: %v", plan.Warnings)
	}
}

// TestCopilotCapsFollowCatalogueMetadata pins the token limits to the fetched
// metadata for the one active model. Copilot otherwise applies its own BYOK
// defaults, which truncate a 400k-context model at a fraction of its window.
func TestCopilotCapsFollowCatalogueMetadata(t *testing.T) {
	plan, err := copilotAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{Model: "openai/gpt-5-mini"},
		Fetch:  func(_, _ string) ([]ModelInfo, error) { return copilotInfos, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := plan.Env["COPILOT_PROVIDER_MAX_PROMPT_TOKENS"]; got != "400000" {
		t.Errorf("max prompt tokens = %q, want 400000", got)
	}
	if got := plan.Env["COPILOT_PROVIDER_MAX_OUTPUT_TOKENS"]; got != "128000" {
		t.Errorf("max output tokens = %q, want 128000", got)
	}
	if warningsContain(plan, "conservative caps") {
		t.Errorf("caps are known, so no fallback warning belongs on the plan: %v", plan.Warnings)
	}
}

// TestCopilotCapsFallBackWhenMetadataMissing is the other half: a model with no
// metadata gets the conservative pair, and the user is told rather than left to
// discover the truncation.
func TestCopilotCapsFallBackWhenMetadataMissing(t *testing.T) {
	plan, err := copilotAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{Model: "openai/gpt-5-mini"},
		Fetch:  func(_, _ string) ([]ModelInfo, error) { return []ModelInfo{{ID: "openai/gpt-5-mini"}}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := plan.Env["COPILOT_PROVIDER_MAX_PROMPT_TOKENS"]; got != "128000" {
		t.Errorf("max prompt tokens = %q, want the %d fallback", got, fallbackContextSize)
	}
	if got := plan.Env["COPILOT_PROVIDER_MAX_OUTPUT_TOKENS"]; got != "8192" {
		t.Errorf("max output tokens = %q, want the %d fallback", got, fallbackOutputSize)
	}
	if !warningsContain(plan, "conservative caps") {
		t.Errorf("expected a conservative-caps warning, got: %v", plan.Warnings)
	}
}
