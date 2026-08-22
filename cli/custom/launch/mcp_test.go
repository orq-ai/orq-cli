package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPIsOptIn(t *testing.T) {
	ctx := &AgentContext{Getenv: env(nil), Flags: GatewayFlags{}}
	if mcpURL(ctx) != "" {
		t.Fatalf("MCP wired without --mcp: %s", mcpURL(ctx))
	}
	// The shipped skill set is linked in from the binary now, so no plugin is
	// fetched unless someone pins their own bundle.
	if skillsPluginURL(ctx) != "" {
		t.Fatal("a plugin was fetched without ORQ_SKILLS_URL")
	}

	ctx.Flags.MCP = true
	if mcpURL(ctx) != DefaultMCPURL {
		t.Fatalf("--mcp: %s", mcpURL(ctx))
	}
	if skillsPluginURL(ctx) != "" {
		t.Fatal("--mcp should not pull a skills plugin off the network")
	}

	ctx.Getenv = env(map[string]string{
		"ORQ_MCP_URL":    "https://custom.example/mcp",
		"ORQ_SKILLS_URL": "https://example.com/custom.zip",
	})
	if mcpURL(ctx) != "https://custom.example/mcp" {
		t.Fatal("env override ignored")
	}
	if skillsPluginURL(ctx) != "https://example.com/custom.zip" {
		t.Fatal("ORQ_SKILLS_URL is the opt-in and must still be honoured")
	}

	ctx.Flags.NoSkills = true
	if skillsPluginURL(ctx) != "" {
		t.Fatal("--no-skills should still opt out of an explicit ORQ_SKILLS_URL")
	}
}

func TestClaudeMCPWiring(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{MCP: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if len(plan.PreArgs) != 2 || plan.PreArgs[0] != "--mcp-config" {
		t.Fatalf("preargs: %v", plan.PreArgs)
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

func TestClaudeBareLaunchWiresNoArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	// Session skills still install (they are local files, not an MCP-dependent
	// plugin), so a cleanup is expected; nothing reaches the argv.
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if len(plan.PreArgs) != 0 {
		t.Fatalf("bare launch should put nothing on the argv: %v", plan.PreArgs)
	}
}

func TestClaudeSkillsOverride(t *testing.T) {
	plan, err := resolveClaude(&AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(map[string]string{"ORQ_SKILLS_URL": "https://example.com/custom.zip"}),
		Flags:  GatewayFlags{MCP: true, NoSkills: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PreArgs) != 4 || plan.PreArgs[3] != "https://example.com/custom.zip" {
		t.Fatalf("skills url override: %v", plan.PreArgs)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
}

func TestCodexMCPArgs(t *testing.T) {
	plan, err := resolveCodex(codexCtx(nil, GatewayFlags{MCP: true}, okProbe))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	joined := strings.Join(plan.PreArgs, " ")
	if !strings.Contains(joined, `mcp_servers.`+MCPServerName+`.url="`+DefaultMCPURL+`"`) ||
		!strings.Contains(joined, `mcp_servers.`+MCPServerName+`.bearer_token_env_var="ORQ_API_KEY"`) {
		t.Fatalf("mcp overrides missing: %s", joined)
	}
}

func TestOpenCodeMCPBlock(t *testing.T) {
	content, err := BuildOpenCodeConfigContent(
		"https://api.orq.ai/v3/router", "openai/gpt-5-mini",
		[]string{"openai/gpt-5-mini"}, nil, DefaultMCPURL)
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
	orq := parsed.MCP[MCPServerName]
	if orq.Type != "remote" || orq.URL != DefaultMCPURL ||
		orq.Headers["Authorization"] != "Bearer {env:ORQ_API_KEY}" {
		t.Fatalf("mcp block: %+v", orq)
	}
}

func TestKimiMCPFile(t *testing.T) {
	def := kimiAgent()
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(nil),
		Flags:  GatewayFlags{MCP: true},
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

// TestWriteSessionSkillsPlantsShippedSkills exercises writeSessionSkills in
// isolation against a bare temp dir. This is agent-agnostic: kimi calls it
// today, and Task 10's four refcounted-link agents will reuse it too — see
// resolveKimi in kimi.go for the one call site currently wired up.
func TestWriteSessionSkillsPlantsShippedSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	if err := writeSessionSkills(dir); err != nil {
		t.Fatalf("writeSessionSkills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills written: %v %d", err, len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", entries[0].Name(), "SKILL.md")); err != nil {
		t.Errorf("skill not readable: %v", err)
	}
}

func TestMaybeWriteSessionSkillsSuppressedByNoSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	ctx := &AgentContext{Flags: GatewayFlags{NoSkills: true}, Getenv: func(string) string { return "" }}
	if err := maybeWriteSessionSkills(ctx, dir); err != nil {
		t.Fatalf("maybeWriteSessionSkills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
		t.Error("--no-skills still wrote skills")
	}
}
