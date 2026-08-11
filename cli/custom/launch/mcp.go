package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultMCPURL is orq's hosted MCP server; agents get it wired automatically
// so orq tools are available without manual per-harness setup.
// api.orq.ai is the documented API host, matching auth.Client.MCPServerURL.
// my.orq.ai answers the same route but is the dashboard.
const DefaultMCPURL = "https://api.orq.ai/v2/mcp"

// DefaultSkillsPluginURL is the orq skills plugin (github.com/orq-ai/
// assistant-plugins) as a zip; claude loads it session-only via --plugin-url.
// Pinned to a commit SHA so a compromised or bad upstream push can't reach
// users unreviewed — bump the SHA in CLI releases to ship plugin updates
// (override ad hoc with ORQ_SKILLS_URL).
const DefaultSkillsPluginURL = "https://github.com/orq-ai/assistant-plugins/archive/415edd51ddba3b10d4e3091c6d91b0cbca57566b.zip"

// skillsPluginURL returns the plugin zip URL, or "" when disabled via
// --no-skills.
func skillsPluginURL(ctx *AgentContext) string {
	if ctx.Flags.NoSkills {
		return ""
	}
	return firstNonEmpty(ctx.Getenv("ORQ_SKILLS_URL"), DefaultSkillsPluginURL)
}

// mcpURL returns the orq MCP endpoint for this launch, or "" when disabled
// via --no-mcp. A non-default ORQ_API_BASE_URL (self-hosted / regional)
// carries the MCP endpoint with it. The API key is never embedded — each
// harness references the ORQ_API_KEY env var through its own mechanism.
func mcpURL(ctx *AgentContext) string {
	if ctx.Flags.NoMCP {
		return ""
	}
	// A credential that cannot pass MCP auth (session token from a login
	// made before the CLI requested mcp:* scopes) would make every MCP call
	// fail with insufficient_scope. Skip the wiring instead of shipping a
	// broken server; run.go prints the note.
	if ctx.Creds != nil && !ctx.Creds.SupportsMCP() {
		return ""
	}
	apiBase := ""
	if ctx.Creds != nil {
		apiBase = ctx.Creds.APIBaseURL
	}
	return firstNonEmpty(
		ctx.Getenv("ORQ_MCP_URL"),
		deriveFromAPIBase(apiBase, "/v2/mcp"),
		DefaultMCPURL,
	)
}

// claudeMCPConfig is the --mcp-config file payload; claude expands
// ${ORQ_API_KEY} from the environment at load time.
func claudeMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"orq": map[string]any{
				"type":    "http",
				"url":     url,
				"headers": map[string]string{"Authorization": "Bearer ${ORQ_API_KEY}"},
			},
		},
	})
	return string(encoded)
}

func writeClaudeMCPConfig(url string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "orq-claude-mcp-")
	if err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(claudeMCPConfig(url)), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// codexMCPArgs wires the orq MCP server via -c TOML overrides using codex's
// native streamable-HTTP transport with bearer_token_env_var.
func codexMCPArgs(url string) []string {
	return []string{
		"-c", tomlOverride("mcp_servers.orq.url", url),
		"-c", tomlOverride("mcp_servers.orq.bearer_token_env_var", "ORQ_API_KEY"),
	}
}

// openCodeMCPBlock is the "mcp" section for the inline OpenCode/Kilo config;
// {env:ORQ_API_KEY} keeps the key out of the file.
func openCodeMCPBlock(url string) map[string]any {
	return map[string]any{
		"orq": map[string]any{
			"type":    "remote",
			"url":     url,
			"oauth":   false,
			"headers": map[string]string{"Authorization": "Bearer {env:ORQ_API_KEY}"},
		},
	}
}

// kimiMCPConfig is the mcp.json written into KIMI_CODE_HOME; kimi resolves
// the bearer token from ORQ_API_KEY via bearerTokenEnvVar.
func kimiMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"orq": map[string]any{
				"url":               url,
				"bearerTokenEnvVar": "ORQ_API_KEY",
			},
		},
	})
	return string(encoded)
}
