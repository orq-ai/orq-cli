package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/launch"
)

// The two sides have to agree about what is on disk: an entry `orq connect`
// wrote must suppress the session entry `orq launch` would otherwise inject
// under the same name. They agreed by coincidence while launch carried its own
// copy of the path-and-presence table, and drifted — different codex header
// rules, kilo's second filename missing. Now launch asks the registry, and this
// is the test that says so for every agent that participates.
func TestPersistedEntrySuppressesSessionWiring(t *testing.T) {
	cases := []struct {
		agent string
		// dirs the agent's detect() looks for, relative to HOME
		seed []string
		// injected reports whether the resolved plan carries a session entry.
		injected func(*launch.LaunchPlan) bool
	}{
		{
			agent: "claude",
			seed:  []string{".claude"},
			injected: func(p *launch.LaunchPlan) bool {
				return strings.Contains(strings.Join(p.PreArgs, " "), "--mcp-config")
			},
		},
		{
			agent: "codex",
			seed:  []string{".codex"},
			injected: func(p *launch.LaunchPlan) bool {
				return strings.Contains(strings.Join(p.PreArgs, " "), "mcp_servers."+launch.MCPServerName)
			},
		},
		{
			agent:    "opencode",
			seed:     []string{".config/opencode"},
			injected: func(p *launch.LaunchPlan) bool { return inlineMCPNamed(p.Env["OPENCODE_CONFIG_CONTENT"]) },
		},
		{
			agent:    "kilo",
			seed:     []string{".config/kilo"},
			injected: func(p *launch.LaunchPlan) bool { return inlineMCPNamed(p.Env["KILO_CONFIG_CONTENT"]) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
			t.Chdir(t.TempDir())
			for _, dir := range tc.seed {
				if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			launch.PersistedMCPHook = mcpEntryPresent
			t.Cleanup(func() { launch.PersistedMCPHook = nil })

			// Without an entry there is one to suppress, so the session entry
			// must be there — otherwise this test would pass on a launch that
			// never wires MCP at all.
			if !tc.injected(resolveLaunchPlan(t, tc.agent)) {
				t.Fatal("no session MCP entry before connect wrote one — the test proves nothing")
			}

			spec, ok := lookupAgent(tc.agent)
			if !ok || spec.writeMCP == nil {
				t.Fatalf("%s has no MCP support in the registry", tc.agent)
			}
			path, err := spec.mcpConfig(true)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.writeMCP(path, launch.DefaultMCPURL); err != nil {
				t.Fatal(err)
			}

			if tc.injected(resolveLaunchPlan(t, tc.agent)) {
				t.Fatalf("the entry orq connect wrote to %s did not suppress launch's session entry", path)
			}
		})
	}
}

// A commented-out table is not a wired entry. Reading it as one suppresses the
// session entry and leaves the user with no orq tools and nothing said.
func TestCommentedCodexTableDoesNotSuppressWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# [mcp_servers." + launch.MCPServerName + "]\n# url = \"" + launch.DefaultMCPURL + "\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	launch.PersistedMCPHook = mcpEntryPresent
	t.Cleanup(func() { launch.PersistedMCPHook = nil })

	plan := resolveLaunchPlan(t, "codex")
	if !strings.Contains(strings.Join(plan.PreArgs, " "), "mcp_servers."+launch.MCPServerName) {
		t.Fatal("a commented-out table read as wired and suppressed the session entry")
	}
}

// resolveLaunchPlan resolves one agent's launch plan with MCP on and everything
// that would reach the network stubbed.
func resolveLaunchPlan(t *testing.T, agent string) *launch.LaunchPlan {
	t.Helper()
	var def *launch.AgentDef
	for _, d := range launch.Agents() {
		if d.Name == agent {
			d := d
			def = &d
			break
		}
	}
	if def == nil {
		t.Fatalf("%s is not a launch agent", agent)
	}
	plan, err := def.Resolve(&launch.AgentContext{
		Creds:  &launch.Credentials{APIKey: "orq-key", APIBaseURL: launch.DefaultGatewayAPIBaseURL, Kind: launch.CredentialAPIKey},
		Getenv: os.Getenv,
		Flags:  launch.GatewayFlags{MCP: true, NoSkills: true},
		Fetch: func(_, _ string) ([]launch.ModelInfo, error) {
			return []launch.ModelInfo{{ID: "openai/gpt-5.4"}}, nil
		},
		ExecProbe: func(string, ...string) (string, error) {
			return `{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4","base_instructions":"You are Codex.","priority":1000}]}`, nil
		},
	})
	if err != nil {
		t.Fatalf("resolving %s: %v", agent, err)
	}
	if plan.Cleanup != nil {
		t.Cleanup(plan.Cleanup)
	}
	return plan
}

// inlineMCPNamed reports whether an inline opencode/kilo config document
// declares the orq MCP server.
func inlineMCPNamed(content string) bool {
	var cfg struct {
		MCP map[string]any `json:"mcp"`
	}
	if json.Unmarshal([]byte(content), &cfg) != nil {
		return false
	}
	_, ok := cfg.MCP[launch.MCPServerName]
	return ok
}
