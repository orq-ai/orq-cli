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

// MCPURLFor is the single derivation of the orq MCP endpoint: the explicit
// override first, then a non-default API base (self-hosted / regional) carrying
// the endpoint with it, then the hosted default. `orq connect` writes persistent
// entries against it rather than repeating the order, so a session launched by
// `orq launch` and a persisted entry can never point at two different servers.
func MCPURLFor(apiBase string) string {
	return firstNonEmpty(
		strings.TrimSpace(os.Getenv("ORQ_MCP_URL")),
		deriveFromAPIBase(apiBase, "/v2/mcp"),
		DefaultMCPURL,
	)
}

// mcpURL returns the orq MCP endpoint for this launch, or "" with --no-mcp.
// The API key is never embedded — each harness references the ORQ_API_KEY env
// var through its own mechanism.
func mcpURL(ctx *AgentContext) string {
	if !ctx.Flags.MCP {
		return ""
	}
	apiBase := ""
	if ctx.Creds != nil {
		apiBase = ctx.Creds.APIBaseURL
	}
	// ctx.Getenv first, then the shared resolver: the context's environment is
	// the seam tests inject through, and MCPURLFor reads the real one.
	return firstNonEmpty(ctx.Getenv("ORQ_MCP_URL"), MCPURLFor(apiBase))
}

// persistedMCPConfigured checks the configs that the agent reads alongside a
// launch-owned session override. A persisted OAuth entry is authoritative, so
// adding a second entry under the same name would only shadow it.
func persistedMCPConfigured(agent string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	paths := []string{}
	switch agent {
	case "claude":
		if cwd, err := os.Getwd(); err == nil {
			paths = append(paths, filepath.Join(cwd, ".mcp.json"))
		}
		paths = append(paths, filepath.Join(home, ".claude.json"))
	case "codex":
		root := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		paths = append(paths, filepath.Join(root, "config.toml"))
	case "opencode":
		paths = append(paths, filepath.Join(home, ".config/opencode/opencode.json"))
	case "kilo":
		paths = append(paths, filepath.Join(home, ".config/kilo/kilo.json"), filepath.Join(home, ".config/kilo/kilo.jsonc"))
	default:
		return false
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(path, ".toml") {
			if strings.Contains(string(data), "[mcp_servers."+MCPServerName+"]") || strings.Contains(string(data), "[mcp_servers.\""+MCPServerName+"\"]") {
				return true
			}
			continue
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		for _, key := range []string{"mcpServers", "mcp"} {
			if section, ok := raw[key].(map[string]any); ok {
				if _, exists := section[MCPServerName]; exists {
					return true
				}
			}
		}
	}
	return false
}

// claudeMCPConfig is the --mcp-config file payload. Claude authenticates the
// remote through its own OAuth flow.
func claudeMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"type": "http",
				"url":  url,
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

// codexMCPArgs wires the orq MCP server via -c TOML overrides. Codex performs
// OAuth for the named server when the user runs its login command.
func codexMCPArgs(url string) []string {
	return []string{
		"-c", tomlOverride("mcp_servers."+MCPServerName+".url", url),
	}
}

// openCodeMCPBlock is the "mcp" section for the inline OpenCode/Kilo config.
// OAuth is owned by the agent, not by this CLI.
func openCodeMCPBlock(url string) map[string]any {
	return map[string]any{
		MCPServerName: map[string]any{
			"type":  "remote",
			"url":   url,
			"oauth": true,
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

// maybeWriteSessionSkills is the flag-aware wrapper for the launcher-owned
// directory path (kimi). It carries the same two guarantees
// maybeInstallSessionSkills does: --no-skills writes nothing, --dry-run writes
// nothing and reports what a real run would do instead, and a failure is a
// warning on the plan rather than a refusal to start the agent.
func maybeWriteSessionSkills(ctx *AgentContext, plan *LaunchPlan, dir string) {
	if ctx.Flags.NoSkills {
		return
	}
	if ctx.Flags.DryRun {
		// Same reasoning as maybeInstallSessionSkills: a dry run resolves
		// everything and starts nothing, so it must not unpack a generation
		// into the user's home on the way. Report the destination instead of
		// writing to it — losing the side effect, not the information.
		names, err := skills.Names()
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
			return
		}
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"a real run links %d skills into %s for the session and removes them with it on exit",
			len(names), filepath.Join(dir, "skills")))
		return
	}
	if err := writeSessionSkills(dir); err != nil {
		// Skills are an enhancement; refusing to start the agent because a
		// symlink failed is worse than starting without them.
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
	}
}

// kimiMCPConfig is the mcp.json written into KIMI_CODE_HOME. Kimi authenticates
// this remote through its own OAuth flow.
func kimiMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"url": url,
			},
		},
	})
	return string(encoded)
}
