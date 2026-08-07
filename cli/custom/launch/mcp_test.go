package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPURLDefaultAndOverrides(t *testing.T) {
	ctx := &AgentContext{Getenv: env(nil), Flags: GatewayFlags{}}
	if mcpURL(ctx) != DefaultMCPURL {
		t.Fatalf("default: %s", mcpURL(ctx))
	}

	ctx.Getenv = env(map[string]string{"ORQ_MCP_URL": "https://custom.example/mcp"})
	if mcpURL(ctx) != "https://custom.example/mcp" {
		t.Fatal("env override ignored")
	}

	ctx.Flags.NoMCP = true
	if mcpURL(ctx) != "" {
		t.Fatal("--no-mcp should disable")
	}
}

func TestClaudeMCPWiring(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if len(plan.PreArgs) != 4 || plan.PreArgs[0] != "--mcp-config" {
		t.Fatalf("preargs: %v", plan.PreArgs)
	}
	if plan.PreArgs[2] != "--plugin-url" || plan.PreArgs[3] != DefaultSkillsPluginURL {
		t.Fatalf("skills plugin args: %v", plan.PreArgs)
	}
	data, err := os.ReadFile(plan.PreArgs[1])
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, DefaultMCPURL) ||
		!strings.Contains(content, "Bearer ${ORQ_API_KEY}") {
		t.Fatalf("mcp config: %s", content)
	}
	// claude skips servers with a url but no transport type.
	if !strings.Contains(content, `"type":"http"`) {
		t.Fatalf("mcp config missing transport type: %s", content)
	}
	if strings.Contains(content, "orq-key") {
		t.Fatal("key leaked into mcp config file")
	}
	if plan.Env["ORQ_API_KEY"] != "orq-key" {
		t.Fatal("ORQ_API_KEY env missing for ${VAR} expansion")
	}
}

func TestClaudeNoMCP(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{NoMCP: true, NoSkills: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PreArgs) != 0 || plan.Cleanup != nil {
		t.Fatalf("opt-outs should skip all wiring: %v", plan.PreArgs)
	}
}

func TestClaudeSkillsOverride(t *testing.T) {
	plan, err := resolveClaude(&AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL},
		Getenv: env(map[string]string{"ORQ_SKILLS_URL": "https://example.com/custom.zip"}),
		Flags:  GatewayFlags{NoMCP: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PreArgs) != 2 || plan.PreArgs[1] != "https://example.com/custom.zip" {
		t.Fatalf("skills url override: %v", plan.PreArgs)
	}
}

func TestCodexMCPArgs(t *testing.T) {
	plan, err := resolveCodex(codexCtx(nil, GatewayFlags{}, okProbe))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	joined := strings.Join(plan.PreArgs, " ")
	if !strings.Contains(joined, `mcp_servers.orq.url="`+DefaultMCPURL+`"`) ||
		!strings.Contains(joined, `mcp_servers.orq.bearer_token_env_var="ORQ_API_KEY"`) {
		t.Fatalf("mcp overrides missing: %s", joined)
	}
}

func TestOpenCodeMCPBlock(t *testing.T) {
	content, err := BuildOpenCodeConfigContent(
		"https://api.orq.ai/v3/router", "openai/gpt-5-mini",
		[]string{"openai/gpt-5-mini"}, DefaultMCPURL)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCP map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			OAuth   bool              `json:"oauth"`
			Headers map[string]string `json:"headers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}
	orq := parsed.MCP["orq"]
	if orq.Type != "remote" || orq.URL != DefaultMCPURL ||
		orq.Headers["Authorization"] != "Bearer {env:ORQ_API_KEY}" {
		t.Fatalf("mcp block: %+v", orq)
	}
}

func TestKimiMCPFile(t *testing.T) {
	def := kimiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL},
		Getenv: env(nil),
		Flags:  GatewayFlags{},
		Fetch:  func(_, _ string) ([]ModelInfo, error) { return kimiInfos, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	data, err := os.ReadFile(filepath.Join(plan.Env["KIMI_CODE_HOME"], "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, DefaultMCPURL) ||
		!strings.Contains(content, `"bearerTokenEnvVar":"ORQ_API_KEY"`) {
		t.Fatalf("kimi mcp.json: %s", content)
	}
	if strings.Contains(content, "sk-test") {
		t.Fatal("key leaked into mcp.json")
	}
}
