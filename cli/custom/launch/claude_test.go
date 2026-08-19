package launch

import (
	"strings"
	"testing"
)

func claudeCtx(envMap map[string]string, flags GatewayFlags) *AgentContext {
	return &AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL},
		Getenv: env(envMap),
		Flags:  flags,
	}
}

func TestClaudeDefaults(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL":         DefaultClaudeGatewayURL,
		"ANTHROPIC_AUTH_TOKEN":       "orq-key",
		"ANTHROPIC_API_KEY":          "",
		"ANTHROPIC_MODEL":            DefaultClaudeModel,
		"ANTHROPIC_SMALL_FAST_MODEL": DefaultClaudeSmallFastModel,
	}
	for k, v := range want {
		got, present := plan.Env[k]
		if !present || got != v {
			t.Fatalf("%s: got %q (present=%v), want %q", k, got, present, v)
		}
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

func TestClaudeBaseURLPriority(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(
		map[string]string{"ORQ_ANTHROPIC_BASE_URL": "https://env.example/v3/anthropic"},
		GatewayFlags{BaseURL: "https://flag.example/v3/anthropic"},
	))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://flag.example/v3/anthropic" {
		t.Fatalf("flag should win: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}

	plan, _ = resolveClaude(claudeCtx(
		map[string]string{"ORQ_ANTHROPIC_BASE_URL": "https://env.example/v3/anthropic"}, GatewayFlags{}))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://env.example/v3/anthropic" {
		t.Fatalf("env should win over default: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}

	// The OpenAI-shaped shared router var must NOT reach claude — it speaks
	// the Anthropic-native API (review finding: silent misroute).
	plan, _ = resolveClaude(claudeCtx(
		map[string]string{"ORQ_GATEWAY_URL": "https://api.orq.ai/v3/router"}, GatewayFlags{}))
	if plan.Env["ANTHROPIC_BASE_URL"] != DefaultClaudeGatewayURL {
		t.Fatalf("ORQ_GATEWAY_URL must be ignored by claude: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}
}

func TestClaudeModelPriority(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(
		map[string]string{"ANTHROPIC_MODEL": "anthropic/env-model", "ANTHROPIC_SMALL_FAST_MODEL": "anthropic/small-env"},
		GatewayFlags{Model: "anthropic/flag-model"},
	))
	if plan.Env["ANTHROPIC_MODEL"] != "anthropic/flag-model" {
		t.Fatalf("model: %v", plan.Env["ANTHROPIC_MODEL"])
	}
	if plan.Env["ANTHROPIC_SMALL_FAST_MODEL"] != "anthropic/small-env" {
		t.Fatalf("small fast: %v", plan.Env["ANTHROPIC_SMALL_FAST_MODEL"])
	}
}

func TestClaudeWarnsBareModel(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, GatewayFlags{Model: "claude-sonnet-4-6"}))
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "provider/") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// Claude Code resolves /model opus|sonnet|haiku through three tier variables.
// Unset, it sends the bare alias, which the gateway rejects for having no
// provider/ prefix, so a launched session cannot switch tiers at all.
func TestClaudeWiresEveryModelTier(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	for _, tc := range []struct{ env, want string }{
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", DefaultClaudeOpusModel},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", DefaultClaudeSonnetModel},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", DefaultClaudeHaikuModel},
	} {
		got := plan.Env[tc.env]
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.env, got, tc.want)
		}
		if !strings.Contains(got, "/") {
			t.Errorf("%s = %q has no provider/ prefix; the gateway rejects bare ids", tc.env, got)
		}
	}
}

// The tiers are overridable, like ANTHROPIC_MODEL already is: a user who has
// pinned one in their shell keeps it.
func TestClaudeTiersHonourTheEnvironment(t *testing.T) {
	ctx := claudeCtx(map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "anthropic/claude-opus-4-8"}, GatewayFlags{})
	plan, err := resolveClaude(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if got := plan.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "anthropic/claude-opus-4-8" {
		t.Errorf("env override ignored: got %q", got)
	}
}
