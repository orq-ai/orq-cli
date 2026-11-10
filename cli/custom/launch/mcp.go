package launch

import (
	"encoding/json"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"orq/cli/custom/auth"
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

// mcpKeyEnv is the variable --mcp-auth env points an entry at. Every agent's
// plan exports it, holding the credential this launch resolved.
const mcpKeyEnv = "ORQ_API_KEY"

// HTTPMCPEntry is the type/url-only server value shared by session launch
// configuration and persistent agent configuration.
func HTTPMCPEntry(url string) map[string]any {
	return map[string]any{"type": "http", "url": url}
}

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
	if hint := differingSkillsHint(agent); hint != "" {
		plan.Warnings = append(plan.Warnings, hint)
	}
}

// SkillsUpdateHook says whether a newer CLI is out to suggest. The commands
// package injects it, as with PersistedMCPHook: the update check lives there.
// known=false means "cannot tell"; hint="" with known=true means this binary
// is the latest, so there is no upgrade to suggest.
var SkillsUpdateHook func() (hint string, known bool)

// differingSkillsHint explains the name collisions an agent reports when a
// skill it also reads elsewhere is a different version from the bundled one.
// The bundle is pinned to the CLI release, so updating the CLI is the fix
// when the other copy is the newer one — unless this is already the latest.
func differingSkillsHint(agent string) string {
	paths, err := skills.DifferingCopies(agent)
	if err != nil || len(paths) == 0 {
		return ""
	}
	dirs := map[string]bool{}
	var where []string
	for _, p := range paths {
		if d := filepath.Dir(p); !dirs[d] {
			dirs[d] = true
			where = append(where, d)
		}
	}
	msg := fmt.Sprintf("%d orq skills in %s differ from the version this CLI bundles; this session uses the bundled ones",
		len(paths), strings.Join(where, ", "))
	suggestion := "if yours are newer, run 'orq update' to get a CLI that bundles them"
	if SkillsUpdateHook != nil {
		if hint, known := SkillsUpdateHook(); known {
			suggestion = hint
		}
	}
	if suggestion == "" {
		return msg
	}
	return msg + "; " + suggestion
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
// No credential is written anywhere: by default the URL is the whole entry and
// every harness authenticates this remote through its own OAuth flow. Under
// --mcp-auth env the entry also names ORQ_API_KEY, the variable and never its
// value, because OAuth needs a browser that a headless run does not have.
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
// session entry is the recoverable mistake, suppressing it is not. --mcp-auth env
// shadows it on purpose; see sessionMCPURL.
//
// kimi never asks: launch points its config home at a fresh temp dir, so the
// persisted file is never on the search path. omp never needs to: its
// session .mcp.json sits behind the user's linked-in mcp.json, and omp keeps
// the first same-named entry, so a persisted one wins anyway. pi has no MCP
// support at all.
func persistedMCPConfigured(agent string) bool {
	if PersistedMCPHook == nil {
		return false
	}
	return PersistedMCPHook(agent)
}

// sessionMCPURL is the endpoint to write a session entry for, or "" for none.
// --mcp-auth env writes one over a persisted entry on purpose: that entry may
// never have finished its browser login, and it would otherwise win and leave
// the flag doing nothing.
func sessionMCPURL(ctx *AgentContext, agent string) string {
	url := mcpURL(ctx)
	if url == "" || (!ctx.Flags.MCPEnvAuth && persistedMCPConfigured(agent)) {
		return ""
	}
	return url
}

// checkMCPEnvAuthOrigin refuses to hand this launch's credential to a server on
// another origin. It is an error because --mcp-auth env is for unattended runs,
// where nobody reads a warning. The platform serves MCP on the API host, so no
// real setup needs another origin: local development serves both on one
// localhost origin. Loopback gets no exemption, since a tunnel can sit behind it.
func checkMCPEnvAuthOrigin(apiBase string, getenv func(string) string) error {
	override := strings.TrimSpace(getenv("ORQ_MCP_URL"))
	if override == "" || sameAPIOrigin(override, apiBase) {
		return nil
	}
	// Neither error repeats the whole value: a URL can carry a password, and this
	// flag runs in CI.
	parsed, err := neturl.Parse(override)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("ORQ_MCP_URL needs an http:// or https:// scheme and a host")
	}
	return fmt.Errorf("ORQ_MCP_URL points at %s://%s, which is not on %s, so --mcp-auth env would send this launch's credential there; unset it or drop --mcp-auth env", parsed.Scheme, parsed.Host, apiBase)
}

// mcpEnvAuthWarnings names what --mcp-auth env cannot fix for the caller. It
// depends on local state, so --help also says which key the flag needs. The
// login-token case is in reportCredentialNotices, next to the note it would
// otherwise contradict.
func mcpEnvAuthWarnings(creds *Credentials) []string {
	var warnings []string
	// A profile's key is whatever the user gave it, so only the key `orq setup`
	// minted is known to be narrow. SavedAgentKey returns the profile's otherwise.
	if _, _, profile := profileKey(); !profile && creds.Kind == CredentialAPIKey {
		if saved, _ := auth.SavedAgentKey(); saved != "" && saved == creds.APIKey {
			warnings = append(warnings,
				"--mcp-auth env is using the key 'orq setup' saved, which normally holds only gateway access and model read, so most orq tools will refuse it; export a workspace API key with the permissions they need")
		}
	}
	return warnings
}

// sameAPIOrigin reports whether an MCP URL is on the API base's origin. The
// hosted service has two interchangeable names, and a pre-4.15 session stores
// the older one, so those two count as one origin.
func sameAPIOrigin(target, apiBase string) bool {
	parsed, err := neturl.Parse(target)
	if err != nil {
		return false
	}
	base, err := neturl.Parse(apiBase)
	if err != nil {
		return false
	}
	if parsed.Scheme == base.Scheme && parsed.Host == base.Host {
		return true
	}
	return auth.IsHostedAPIBase(parsed.Scheme+"://"+parsed.Host) && auth.IsHostedAPIBase(apiBase)
}

// httpMCPConfigJSON is the mcpServers payload used by claude and copilot.
// All authenticate the remote through their own OAuth flow, so no credential
// appears in it.
func httpMCPConfigJSON(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: HTTPMCPEntry(url),
		},
	})
	return string(encoded)
}

// httpMCPConfigJSONEnvAuth is httpMCPConfigJSON for --mcp-auth env. Claude
// expands ${VAR} in its --mcp-config file when it reads it.
func httpMCPConfigJSONEnvAuth(url string) string {
	entry := HTTPMCPEntry(url)
	entry["headers"] = map[string]any{"Authorization": "Bearer ${" + mcpKeyEnv + "}"}
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{MCPServerName: entry},
	})
	return string(encoded)
}

func writeClaudeMCPConfig(url string, envAuth bool) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "orq-claude-mcp-")
	if err != nil {
		return "", nil, err
	}
	config := httpMCPConfigJSON(url)
	if envAuth {
		config = httpMCPConfigJSONEnvAuth(url)
	}
	path = filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
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

// codexMCPEnvAuthArgs adds --mcp-auth env to codexMCPArgs: codex reads the
// bearer token from the named variable itself.
func codexMCPEnvAuthArgs() []string {
	return []string{
		"-c", tomlOverride("mcp_servers."+MCPServerName+".bearer_token_env_var", mcpKeyEnv),
	}
}

// openCodeMCPBlock is the "mcp" section for the inline OpenCode/Kilo config.
// OAuth is owned by the agent, not by this CLI.
func openCodeMCPBlock(url string) map[string]any {
	return map[string]any{MCPServerName: RemoteMCPEntry(url)}
}

// openCodeMCPEnvAuthBlock is openCodeMCPBlock for --mcp-auth env. oauth false
// stops opencode from starting its own flow beside the header; false is the one
// non-object value its schema takes there (see RemoteMCPEntry).
func openCodeMCPEnvAuthBlock(url string) map[string]any {
	entry := RemoteMCPEntry(url)
	entry["oauth"] = false
	entry["headers"] = map[string]any{"Authorization": "Bearer {env:" + mcpKeyEnv + "}"}
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
