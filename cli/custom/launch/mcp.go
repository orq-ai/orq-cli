package launch

import (
	"encoding/json"
	"fmt"
	neturl "net/url"
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
const DefaultMCPURL = DefaultGatewayAPIBaseURL + "/v2/mcp"

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
// maybeWriteSessionSkills. Links land in the current directory's local set,
// or the global set when launched from $HOME (skills.ScopeFor).
//
// Failure is loud but never fatal: an agent that starts without skills is far
// better than an agent that refuses to start.
func maybeInstallSessionSkills(ctx *AgentContext, plan *LaunchPlan, agent string) {
	if ctx.Flags.NoSkills {
		return
	}
	// An agent with no skills directory registered would otherwise resolve zero
	// targets and return success having linked nothing. Say so instead, here
	// rather than at the call site, so the next agent added inherits the check.
	if !skills.Receives(agent) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"orq skills are not wired for %s yet, so this session starts without them", agent))
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
		cwd, err := os.Getwd()
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
			return
		}
		targets, err := skills.Targets([]string{agent}, skills.ScopeFor(cwd))
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

// mcpAuthEnvVar is the variable an --mcp-auth env entry names. Every agent's
// plan already exports it, so the reference always resolves in the child.
const mcpAuthEnvVar = "ORQ_API_KEY"

func setMCPAuth(flags *GatewayFlags, value string) error {
	switch value {
	case MCPAuthOAuth, MCPAuthEnv:
		flags.MCPAuth = value
		return nil
	}
	return fmt.Errorf("flag --mcp-auth expects %q or %q, got %q", MCPAuthOAuth, MCPAuthEnv, value)
}

// mcpEnvAuth reports whether this launch authenticates the orq MCP server with
// a bearer header naming ORQ_API_KEY instead of the agent's OAuth flow. OAuth
// needs a browser, so a headless run (CI, a test runner) never connects
// without it. Only the variable's name is written: the key stays in the
// environment, as it does for the model provider.
func mcpEnvAuth(ctx *AgentContext) bool {
	return ctx.Flags.MCPAuth == MCPAuthEnv
}

// mcpEnvAuthWarnings names what --mcp-auth env changes that the user did not
// ask about. OAuth never shows the MCP host the API key; env sends it there
// as a bearer token, which only matters when ORQ_MCP_URL points the entry at
// a host other than the API's own. And a login token is what ORQ_API_KEY
// holds when there is no durable key, so the orq tools stop when it expires.
func mcpEnvAuthWarnings(ctx *AgentContext) []string {
	url := mcpURL(ctx)
	if !mcpEnvAuth(ctx) || url == "" {
		return nil
	}
	var warnings []string
	apiBase := firstNonEmpty(ctx.Creds.APIBaseURL, DefaultGatewayAPIBaseURL)
	if mcpHost, apiHost := urlHost(url), urlHost(apiBase); mcpHost != apiHost {
		warnings = append(warnings, fmt.Sprintf(
			"--mcp-auth env sends your orq API key to the MCP server at %s, which is not the API host %s", mcpHost, apiHost))
	}
	if ctx.Creds.Kind == CredentialSessionToken {
		warnings = append(warnings,
			"--mcp-auth env authenticates the orq MCP server with the login token, so the orq tools stop working when it expires (about an hour)")
	}
	return warnings
}

// urlHost is the lowercased host of raw, or raw itself when it does not parse,
// so two unparseable values still compare as different hosts.
func urlHost(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.ToLower(u.Hostname())
}

// wantSessionMCPEntry reports whether launch writes its own orq MCP entry for
// the agent. A persisted OAuth entry normally wins, but with --mcp-auth env
// the session entry must shadow it: the persisted one may never have been
// through its browser login.
func wantSessionMCPEntry(ctx *AgentContext, agent string) bool {
	return mcpEnvAuth(ctx) || !persistedMCPConfigured(agent)
}

// mcpURL returns the orq MCP endpoint for this launch, or "" with --no-mcp.
// By default no credential is written anywhere: the URL is the whole entry,
// and every harness authenticates this remote through its own OAuth flow.
// --mcp-auth env adds a header that names ORQ_API_KEY (see mcpEnvAuth).
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

// PersistedMCPHook reports whether an agent already carries an orq MCP entry in
// a config it reads on its own. The commands package injects it at wiring time
// from the same registry `orq connect` writes through: launch cannot import
// that package, and the copy of the path-and-presence table that used to live
// here had already drifted from it — different codex header rules, a missing
// agent, one file of a two-file pair.
var PersistedMCPHook func(agent string) bool

// persistedMCPConfigured is the launch-side question: a persisted OAuth entry is
// authoritative, so adding a second entry under the same name would only shadow
// it. Unset hook means "cannot tell", which has to answer false — writing the
// session entry is the recoverable mistake, suppressing it is not.
//
// kimi never asks: launch points KIMI_CODE_HOME at a fresh temp dir, so no
// persisted file is on its search path and the session entry must always be
// written. pi has no MCP support at all.
func persistedMCPConfigured(agent string) bool {
	if PersistedMCPHook == nil {
		return false
	}
	return PersistedMCPHook(agent)
}

// httpMCPConfigJSON is the mcpServers payload for the agents that take the orq
// MCP server as JSON: claude through --mcp-config, copilot through
// --additional-mcp-config. Both authenticate the remote through their own OAuth
// flow, so no credential appears in it.
func httpMCPConfigJSON(url string) string {
	return httpMCPConfigJSONAuth(url, "")
}

// httpMCPConfigJSONAuth is httpMCPConfigJSON with an Authorization header when
// authorization is non-empty. Callers pass a reference to ORQ_API_KEY in the
// agent's own interpolation syntax, never the key.
func httpMCPConfigJSONAuth(url, authorization string) string {
	entry := map[string]any{
		"type": "http",
		"url":  url,
	}
	if authorization != "" {
		entry["headers"] = map[string]string{"Authorization": authorization}
	}
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{MCPServerName: entry},
	})
	return string(encoded)
}

// claudeMCPEnvAuth is the header claude expands from its environment when it
// loads an MCP config (${VAR} interpolation).
const claudeMCPEnvAuth = "Bearer ${" + mcpAuthEnvVar + "}"

func writeClaudeMCPConfig(url string, envAuth bool) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "orq-claude-mcp-")
	if err != nil {
		return "", nil, err
	}
	authorization := ""
	if envAuth {
		authorization = claudeMCPEnvAuth
	}
	path = filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(httpMCPConfigJSONAuth(url, authorization)), 0o600); err != nil {
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

// codexMCPEnvAuthArgs makes codex send ORQ_API_KEY as the server's bearer
// token, read from its environment by name.
func codexMCPEnvAuthArgs() []string {
	return []string{
		"-c", tomlOverride("mcp_servers."+MCPServerName+".bearer_token_env_var", mcpAuthEnvVar),
	}
}

// openCodeMCPBlock is the "mcp" section for the inline OpenCode/Kilo config.
// OAuth is owned by the agent, not by this CLI.
func openCodeMCPBlock(url string) map[string]any {
	return map[string]any{MCPServerName: RemoteMCPEntry(url)}
}

// openCodeMCPEnvAuthBlock is openCodeMCPBlock authenticated by a header
// opencode fills from ORQ_API_KEY ({env:VAR} interpolation). oauth is false
// so opencode does not start its own discovery and browser flow alongside
// the header; false is the one non-object value its schema accepts there.
func openCodeMCPEnvAuthBlock(url string) map[string]any {
	entry := RemoteMCPEntry(url)
	entry["headers"] = map[string]string{"Authorization": "Bearer {env:" + mcpAuthEnvVar + "}"}
	entry["oauth"] = false
	return map[string]any{MCPServerName: entry}
}

// RemoteMCPEntry is the opencode/kilo mcp value, shared by the session config
// launch injects and the persistent entry `orq connect` writes — one shape, so
// the two cannot describe the same file two different ways.
//
// type and url only. The SDK's McpRemoteConfig types oauth as
// `McpOAuthConfig | false`, so "oauth": true is not merely ignored: opencode
// 1.17.20 rejects the whole document with "Expected McpOAuthConfig | false, got
// true" and exits 1, taking the user's other MCP servers down with it. Omitting
// the key is what turns OAuth auto-detection (RFC 9728 discovery, dynamic
// client registration) on, and enabled defaults to true.
func RemoteMCPEntry(url string) map[string]any {
	return map[string]any{"type": "remote", "url": url}
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
// this remote through its own OAuth flow, or with envAuth reads the bearer
// token from ORQ_API_KEY by name (bearerTokenEnvVar).
func kimiMCPConfig(url string, envAuth bool) string {
	entry := map[string]any{"url": url}
	if envAuth {
		entry["bearerTokenEnvVar"] = mcpAuthEnvVar
	}
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{MCPServerName: entry},
	})
	return string(encoded)
}
