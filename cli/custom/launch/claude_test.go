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
		map[string]string{"ORQ_GATEWAY_URL": "https://env.example/v3/anthropic"},
		GatewayFlags{BaseURL: "https://flag.example/v3/anthropic"},
	))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://flag.example/v3/anthropic" {
		t.Fatalf("flag should win: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}

	plan, _ = resolveClaude(claudeCtx(
		map[string]string{"ORQ_GATEWAY_URL": "https://env.example/v3/anthropic"}, GatewayFlags{}))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://env.example/v3/anthropic" {
		t.Fatalf("env should win over default: %v", plan.Env["ANTHROPIC_BASE_URL"])
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
