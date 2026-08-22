package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"orq/cli/custom/skills"
)

// MCPServerName is the key launch registers the orq MCP server under. It is
// deliberately identical to the one `orq setup` writes into an agent's own
// config: a session entry then SHADOWS the persisted one instead of sitting
// beside it. Using a different key made agents load both, so a user who had
// run setup saw the same server twice and paid for every tool definition
// twice in their context window.
const MCPServerName = "orq-workspace"

// DefaultMCPURL is orq's hosted MCP server; agents get it wired automatically
// so orq tools are available without manual per-harness setup.
// api.orq.ai is the documented API host, matching auth.Client.MCPServerURL.
// my.orq.ai answers the same route but is the dashboard.
const DefaultMCPURL = "https://api.orq.ai/v2/mcp"

// skillsPluginURL returns a plugin zip for claude to fetch, and normally
// returns nothing: the default skill set ships inside this binary and is
// linked into the agent's own skills directory for the session, so no network
// fetch happens on launch. ORQ_SKILLS_URL is the explicit opt-in for anyone
// pinning their own bundle, and only then is --plugin-url passed.
func skillsPluginURL(ctx *AgentContext) string {
	if ctx.Flags.NoSkills {
		return ""
	}
	return strings.TrimSpace(ctx.Getenv("ORQ_SKILLS_URL"))
}

// maybeInstallSessionSkills materializes the shipped skills into the real
// directory the agent reads, for the length of one launch, and chains the
// release onto the plan's cleanup. It is for the agents whose skills
// directory cannot be redirected (claude, codex, and the shared-directory
// readers); kimi instead gets a launcher-owned KIMI_CODE_HOME and uses
// maybeWriteSessionSkills.
//
// Failure is loud but never fatal: an agent that starts without skills is far
// better than an agent that refuses to start.
func maybeInstallSessionSkills(ctx *AgentContext, plan *LaunchPlan, agent string) {
	if ctx.Flags.NoSkills {
		return
	}
	if runtime.GOOS == "windows" {
		// No symlinks to rely on, so a session install would mean copying the
		// whole set in and out of the user's home on every launch. Point at
		// the one-off install instead.
		plan.Warnings = append(plan.Warnings,
			"skills are not installed for a single session on Windows — run 'orq connect skills' once to install them")
		return
	}
	if ctx.Flags.DryRun {
		// A dry run resolves everything and starts nothing, so it must not
		// rearrange the agent's real skills directory on the way. Report the
		// destination instead of writing to it: the point is to lose the side
		// effect, not the information.
		targets, err := skills.Targets([]string{agent})
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
			return
		}
		names, err := skills.Names()
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
			return
		}
		for _, target := range targets {
			plan.Notes = append(plan.Notes, fmt.Sprintf(
				"a real run links %d skills into %s for the session and removes them on exit", len(names), target.Dir))
		}
		return
	}
	release, err := skills.InstallSession(agent)
	if err != nil {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
		return
	}
	plan.AddCleanup(release)
}

// mcpURL returns the orq MCP endpoint for this launch, or "" without --mcp.
// A non-default ORQ_API_BASE_URL (self-hosted / regional)
// carries the MCP endpoint with it. The API key is never embedded — each
// harness references the ORQ_API_KEY env var through its own mechanism.
func mcpURL(ctx *AgentContext) string {
	if !ctx.Flags.MCP {
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
			MCPServerName: map[string]any{
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
		"-c", tomlOverride("mcp_servers."+MCPServerName+".url", url),
		"-c", tomlOverride("mcp_servers."+MCPServerName+".bearer_token_env_var", "ORQ_API_KEY"),
	}
}

// openCodeMCPBlock is the "mcp" section for the inline OpenCode/Kilo config;
// {env:ORQ_API_KEY} keeps the key out of the file.
func openCodeMCPBlock(url string) map[string]any {
	return map[string]any{
		MCPServerName: map[string]any{
			"type":    "remote",
			"url":     url,
			"oauth":   false,
			"headers": map[string]string{"Authorization": "Bearer {env:ORQ_API_KEY}"},
		},
	}
}

// writeSessionSkills symlinks the shipped skills into an agent directory the
// launcher owns for this session. Each name is symlinked into the current
// generation, which outlives the session (EnsureGeneration only retires a
// generation when a later call unpacks a different fingerprint, and the
// current one is always kept). On platforms where os.Symlink fails (Windows
// without developer mode/elevation), fall back to a real copy so the
// launcher never depends on symlink support to run.
func writeSessionSkills(dir string) error {
	gen, err := skills.EnsureGeneration()
	if err != nil {
		return err
	}
	names, err := skills.Names()
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, "skills")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		src := filepath.Join(gen, name)
		dst := filepath.Join(dest, name)
		if err := os.Symlink(src, dst); err != nil {
			if copyErr := skills.CopyDir(src, dst); copyErr != nil {
				return copyErr
			}
		}
	}
	return nil
}

// maybeWriteSessionSkills is the flag-aware wrapper every agent calls.
func maybeWriteSessionSkills(ctx *AgentContext, dir string) error {
	if ctx.Flags.NoSkills {
		return nil
	}
	return writeSessionSkills(dir)
}

// kimiMCPConfig is the mcp.json written into KIMI_CODE_HOME; kimi resolves
// the bearer token from ORQ_API_KEY via bearerTokenEnvVar.
func kimiMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"url":               url,
				"bearerTokenEnvVar": "ORQ_API_KEY",
			},
		},
	})
	return string(encoded)
}
