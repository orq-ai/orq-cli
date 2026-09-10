package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGeminiSettingsJSON(t *testing.T) {
	settings := BuildGeminiSettingsJSON("https://api.orq.ai/v2/mcp")
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{
		`"selectedType":"gemini-api-key"`,
		`"httpUrl":"https://api.orq.ai/v2/mcp"`,
		`"orq-workspace"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func TestBuildGeminiSettingsJSONNoMCP(t *testing.T) {
	settings := BuildGeminiSettingsJSON("")
	if _, ok := settings["mcpServers"]; ok {
		t.Fatal("mcpServers must be omitted when no MCP URL is given")
	}
}

func TestGeminiResolvePlan(t *testing.T) {
	def := geminiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: true},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Env["GEMINI_API_KEY"] != "sk-test" {
		t.Fatalf("GEMINI_API_KEY: %v", plan.Env["GEMINI_API_KEY"])
	}
	if plan.Env["ORQ_API_KEY"] != "sk-test" {
		t.Fatalf("ORQ_API_KEY: %v", plan.Env["ORQ_API_KEY"])
	}
	// Confirmed live: without this, a headless launch exits before making any
	// request ("Gemini CLI is not running in a trusted directory"), since
	// there is no interactive prompt to trust it through.
	if plan.Env["GEMINI_CLI_TRUST_WORKSPACE"] != "true" {
		t.Fatalf("GEMINI_CLI_TRUST_WORKSPACE: %v", plan.Env["GEMINI_CLI_TRUST_WORKSPACE"])
	}
	// GEMINI_CLI_HOME, not HOME: gemini-cli's own homedir() reads it ahead of
	// os.homedir(), so the user's real environment is left alone.
	if plan.Env["HOME"] != "" {
		t.Fatalf("gemini must not override HOME, got %q", plan.Env["HOME"])
	}
	home := plan.Env["GEMINI_CLI_HOME"]
	if home == "" {
		t.Fatal("GEMINI_CLI_HOME missing")
	}
	if got := []string{"--model", DefaultGeminiModel}; plan.PreArgs[0] != got[0] || plan.PreArgs[1] != got[1] {
		t.Fatalf("PreArgs: %v", plan.PreArgs)
	}

	data, err := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"selectedType"`) {
		t.Fatalf("settings.json missing auth selectedType:\n%s", data)
	}
	if !strings.Contains(string(data), MCPServerName) {
		t.Fatalf("settings.json missing MCP server entry:\n%s", data)
	}

	foundFreshHomeWarning := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, "fresh config home") {
			foundFreshHomeWarning = true
		}
	}
	if !foundFreshHomeWarning {
		t.Fatalf("expected a fresh-config-home warning, got: %v", plan.Warnings)
	}

	plan.Cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove the gemini config home temp dir")
	}
}

// TestGeminiOnPremBaseURL guards DefaultBaseURL against regressing to a bare
// DefaultGeminiGatewayURL: that value sits ahead of any derivation in
// ResolveGatewayConfig's chain, so an on-prem session would send every prompt
// to the public gateway while authenticating with the customer's own key.
func TestGeminiOnPremBaseURL(t *testing.T) {
	const onPrem = "https://orq.internal.example.com"
	def := geminiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: onPrem, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if got := plan.Env["GOOGLE_GEMINI_BASE_URL"]; got != onPrem+"/v3/google" {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL = %q, want it derived from the session API base", got)
	}
}

// TestGeminiIgnoresSharedGatewayEnv: ORQ_GATEWAY_URL sits ahead of
// DefaultBaseURL in the chain, so a user who exports it for the router agents
// would otherwise aim gemini's Gemini-native client at /v3/router.
func TestGeminiIgnoresSharedGatewayEnv(t *testing.T) {
	plan, err := geminiAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(map[string]string{"ORQ_GATEWAY_URL": "https://gw.example/v3/router"}),
		Fetch:  func(_, _ string) ([]ModelInfo, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if got := plan.Env["GOOGLE_GEMINI_BASE_URL"]; got != DefaultGeminiGatewayURL {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL = %q, want ORQ_GATEWAY_URL ignored", got)
	}
}

// TestGeminiBaseURLEnvStillWins: the agent's own variable is the supported way
// to repoint it, so ignoring the shared one must not ignore this one too.
func TestGeminiBaseURLEnvStillWins(t *testing.T) {
	const custom = "https://gw.example/v3/google"
	plan, err := geminiAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(map[string]string{"ORQ_GEMINI_BASE_URL": custom}),
		Fetch:  func(_, _ string) ([]ModelInfo, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if got := plan.Env["GOOGLE_GEMINI_BASE_URL"]; got != custom {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL = %q, want %q", got, custom)
	}
}

// TestGeminiRewriteWarning pins the model ids gemini-cli silently replaces.
// The cases marked below were verified against gemini-cli 0.58.0 by pointing
// it at a local Google-wire server and reading the id it actually requested,
// so the rule is observed behaviour, not only a reading of its source.
func TestGeminiRewriteWarning(t *testing.T) {
	// Anything ending in "flash" is rewritten, whatever precedes it: the gate
	// bottoms out in isFlashModel's model.endsWith("flash").
	rewritten := []string{
		"gemini-3.7-flash",
		"gemini-3.6-flash",
		"gemini-2.5-flash",           // verified live
		"google-ai/gemini-3.7-flash", // verified live
		"google-ai/gemini-3.5-flash",
		"flash",              // verified live
		"gemini-flash",       // verified live
		"orq-flash",          // verified live
		"custom-model-flash", // verified live
	}
	for _, m := range rewritten {
		if geminiRewriteWarning(m) == "" {
			t.Errorf("%q is rewritten by gemini-cli but produced no warning", m)
		}
	}

	// Survive untouched: ids that do not end in "flash", plus the one id the
	// gate exempts by name.
	intact := []string{
		"gemini-3.5-flash",       // the substitution target, so nothing is lost
		"gemini-2.5-pro",         // verified live
		"gemini-3-flash-preview", // verified live, PREVIEW_GEMINI_FLASH_MODEL
		"google-ai/gemini-2.5-pro",
		"google/gemini-2.5-pro",
		"gemini-3.7-flash-001",
		"my-gemini-3.7-flash-alias",
		"gemini-3.7-turbo",
		"",
	}
	for _, m := range intact {
		if w := geminiRewriteWarning(m); w != "" {
			t.Errorf("%q passes through gemini-cli untouched but warned: %s", m, w)
		}
	}
}

// catalogue builds a Fetch stub returning provider-qualified ids, the shape
// FetchEnabledModels actually produces. Every other gemini test stubs an empty
// catalogue, which is what let the default-substitution bug through.
func catalogue(ids ...string) func(string, string) ([]ModelInfo, error) {
	return func(_, _ string) ([]ModelInfo, error) {
		infos := make([]ModelInfo, len(ids))
		for i, id := range ids {
			infos[i] = ModelInfo{ID: id}
		}
		return infos, nil
	}
}

func resolveGeminiWith(t *testing.T, fetch func(string, string) ([]ModelInfo, error)) *LaunchPlan {
	t.Helper()
	plan, err := geminiAgent().Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Fetch:  fetch,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plan.Cleanup)
	return plan
}

func modelArg(t *testing.T, plan *LaunchPlan) string {
	t.Helper()
	for i, a := range plan.PreArgs {
		if a == "--model" && i+1 < len(plan.PreArgs) {
			return plan.PreArgs[i+1]
		}
	}
	t.Fatalf("no --model in PreArgs: %v", plan.PreArgs)
	return ""
}

// TestGeminiDefaultMatchesQualifiedCatalogueID is the regression test for the
// bare default never matching. The anthropic entry sorts first on purpose: it
// is exactly what the resolver picked before geminiNormalize was wired in.
func TestGeminiDefaultMatchesQualifiedCatalogueID(t *testing.T) {
	plan := resolveGeminiWith(t, catalogue("anthropic/claude-sonnet-4-6", "google/"+DefaultGeminiModel))

	if got := modelArg(t, plan); got != DefaultGeminiModel {
		t.Fatalf("--model = %q, want the default %q", got, DefaultGeminiModel)
	}
	if warningsContain(plan, "is not enabled in this workspace") {
		t.Fatalf("default is in the catalogue but a substitution warning fired: %v", plan.Warnings)
	}
}

// TestGeminiNeverSubstitutesForeignProvider guards the Gemini-native wire
// against being handed an id it cannot answer for.
func TestGeminiNeverSubstitutesForeignProvider(t *testing.T) {
	plan := resolveGeminiWith(t, catalogue("anthropic/claude-sonnet-4-6", "openai/gpt-5.6-terra"))

	if got := modelArg(t, plan); got != DefaultGeminiModel {
		t.Fatalf("--model = %q, want the default %q rather than another provider's model", got, DefaultGeminiModel)
	}
	if !warningsContain(plan, "this agent can serve") {
		t.Fatalf("expected a no-servable-models warning, got: %v", plan.Warnings)
	}
}

// TestGeminiSubstitutesWithinGoogle keeps the substitution behaviour where it
// is still correct: the default is disabled, so fall back inside the provider
// gemini-cli can actually talk to.
func TestGeminiSubstitutesWithinGoogle(t *testing.T) {
	plan := resolveGeminiWith(t, catalogue("anthropic/claude-sonnet-4-6", "google/gemini-3-pro"))

	if got := modelArg(t, plan); got != "gemini-3-pro" {
		t.Fatalf("--model = %q, want the enabled google model", got)
	}
	if !warningsContain(plan, "is not enabled in this workspace") {
		t.Fatalf("expected a substitution warning, got: %v", plan.Warnings)
	}
}

// TestGeminiDefaultModelSurvivesRewrite guards the default against regressing
// to a provider-qualified flash id, which gemini-cli would silently replace.
func TestGeminiDefaultModelSurvivesRewrite(t *testing.T) {
	if w := geminiRewriteWarning(DefaultGeminiModel); w != "" {
		t.Fatalf("DefaultGeminiModel %q would be rewritten by gemini-cli: %s", DefaultGeminiModel, w)
	}
}

func TestGeminiResolveNoMCP(t *testing.T) {
	def := geminiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: false},
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	data, err := os.ReadFile(filepath.Join(plan.Env["GEMINI_CLI_HOME"], ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "mcpServers") {
		t.Fatalf("--no-mcp must omit mcpServers:\n%s", data)
	}
}
