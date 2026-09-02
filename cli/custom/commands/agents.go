package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
	"orq/cli/custom/skills"

	"github.com/pelletier/go-toml"
	"github.com/spf13/viper"
)

// Writers clear their own keys before emitting, so writing an empty catalogue would delete a working config.
var errNoModelsToOffer = errors.New("no models to offer")

func launchCatalog(models []auth.RouterModel) ([]string, []launch.ModelInfo) {
	refs := make([]string, 0, len(models))
	infos := make([]launch.ModelInfo, 0, len(models))
	for _, m := range models {
		refs = append(refs, m.Ref())
		infos = append(infos, launch.ModelInfo{
			ID:                m.Ref(),
			ContextWindow:     m.Metadata.ContextWindow,
			MaxOutputTokens:   m.Metadata.MaxOutputTokens,
			SupportsResponses: m.Metadata.SupportsResponses,
		})
	}
	return refs, infos
}

type agentSpec struct {
	ID     string
	Label  string
	detect func() bool
	// providerConfig returns the file registering orq as an LLM provider; empty when we configure none.
	providerConfig func(global bool) (string, error)
	// writeProvider registers the provider and its models, returning how many models it listed.
	writeProvider func(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) (int, error)
	// providerPresent is the read-side pair of writeProvider, in that agent's own format; required whenever writeProvider is set.
	providerPresent func(path string) bool
	// removeProvider is writeProvider's inverse; required whenever the writer is set.
	removeProvider func(path string) (bool, error)
	// providerEmbedsKey marks a provider config holding the credential literally rather than referencing ORQ_API_KEY.
	providerEmbedsKey bool
	// providerKey reads back the embedded credential. A literal copy cannot follow
	// a renewal, so only reading it can tell a wired agent from a dead one.
	providerKey func(path string) string
	// mcpConfig returns the file holding the agent's MCP servers; nil when the agent has no MCP support.
	mcpConfig func(global bool) (string, error)
	// writeMCP writes the orq-workspace entry. No credential argument, by design: every
	// MCP-capable agent authenticates by OAuth, so no future edit here can put a key into an
	// agent config file — the leak class e44c747 was written to close. The signature bounds
	// what reaches the writer; TestMCPCredentialCanary is what checks what the writer emits.
	writeMCP func(path, url string) error
	// mcpPresent is writeMCP's read-side pair; required whenever writeMCP is set.
	mcpPresent func(path string) bool
	// removeMCP is writeMCP's inverse; required whenever writeMCP is set.
	removeMCP func(path string) (bool, error)
}

// preferredCodingModels are matched as prefixes against the live catalogue, in order.
var preferredCodingModels = []string{
	"anthropic/claude-sonnet-4",
	"anthropic/claude-opus-4",
	"openai/gpt-5",
	"moonshotai/kimi-k2",
}

func agentRegistry() []agentSpec {
	return []agentSpec{
		{
			ID:         "claude",
			Label:      "Claude Code",
			detect:     detectAny(".claude", ".claude.json"),
			mcpConfig:  projectOrGlobalPath(".mcp.json", ".claude.json"),
			writeMCP:   writeMCPJSON("mcpServers", claudeMCPEntry),
			mcpPresent: jsonProviderPresentAt("mcpServers", launch.MCPServerName),
			removeMCP:  func(p string) (bool, error) { return removeJSONKeys(p, "mcpServers", launch.MCPServerName) },
		},
		{
			ID:    "codex",
			Label: "Codex",
			// Same resolution the writers use, or a CODEX_HOME machine is never offered codex.
			detect:          detectPath(codexPath("")),
			providerConfig:  codexPath(codexProfileName + ".config.toml"),
			writeProvider:   writeCodexProviderTOML,
			removeProvider:  removeCodexProfile,
			providerPresent: tomlTablePresent("model_providers." + launch.CodexProvider),
			// The base config.toml, not the orq.config.toml profile: MCP servers are
			// not profile-scoped in codex, and writeCodexProviderTOML owns the
			// profile file outright and rewrites it wholesale, so an entry placed
			// there would be destroyed on the next 'orq connect gateway'.
			mcpConfig:  codexMCPPath(),
			writeMCP:   writeCodexMCPTOML,
			mcpPresent: tomlTablePresent("mcp_servers." + launch.MCPServerName),
			removeMCP:  func(p string) (bool, error) { return removeTOMLTables(p, codexOwnedMCPTable) },
		},
		{
			ID:     "opencode",
			Label:  "opencode",
			detect: detectAny(".config/opencode"),
			// Provider is global only: opencode and kilo reject {env:...} references in a project config.
			providerConfig:  alwaysGlobalPath(".config/opencode/opencode.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			removeProvider:  removeOpenCodeProviders,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
			mcpConfig:       projectOrGlobalPath("opencode.json", ".config/opencode/opencode.json"),
			writeMCP:        writeMCPJSON("mcp", remoteMCPEntry),
			mcpPresent:      jsonProviderPresentAt("mcp", launch.MCPServerName),
			removeMCP:       func(p string) (bool, error) { return removeJSONKeys(p, "mcp", launch.MCPServerName) },
		},
		{
			ID:     "kimi",
			Label:  "Kimi Code",
			detect: detectAny(".kimi-code", ".kimi"),
			// Kimi reads config.toml only from the home directory.
			providerConfig:    alwaysGlobalPath(".kimi-code/config.toml"),
			writeProvider:     writeKimiProviderTOML,
			removeProvider:    removeKimiProviderTOML,
			providerPresent:   tomlTablePresent("providers." + launch.KimiChatProvider),
			providerEmbedsKey: true,
			providerKey:       tomlStringAt("providers." + launch.KimiChatProvider + ".api_key"),
			// Kimi's MCP config, unlike its provider TOML, has a project scope too.
			mcpConfig:  projectOrGlobalPath(".kimi-code/mcp.json", ".kimi-code/mcp.json"),
			writeMCP:   writeMCPJSON("mcpServers", kimiMCPEntry),
			mcpPresent: jsonProviderPresentAt("mcpServers", launch.MCPServerName),
			removeMCP:  func(p string) (bool, error) { return removeJSONKeys(p, "mcpServers", launch.MCPServerName) },
		},
		{
			ID:     "kilo",
			Label:  "Kilo Code",
			detect: detectAny(".config/kilo"),
			// Provider is global only — same env-reference restriction as opencode above.
			providerConfig:  alwaysGlobalPath(".config/kilo/kilo.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			removeProvider:  removeOpenCodeProviders,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
			mcpConfig:       projectOrGlobalPath("kilo.json", ".config/kilo/kilo.json"),
			writeMCP:        writeMCPJSON("mcp", remoteMCPEntry),
			// The shipped kilo binary reads kilo.jsonc as well as kilo.json; writes still go to .json.
			// The remover has to follow the reader: an entry only kiloMCPPresent can see would
			// otherwise be reported as wired forever and never removable.
			mcpPresent: kiloMCPPresent,
			removeMCP:  kiloRemoveMCP,
		},
		{
			ID:     "pi",
			Label:  "Pi Coding Agent",
			detect: piDetect,
			// pi reads models.json from its agent dir ($PI_CODING_AGENT_DIR, ~/.pi/agent) only.
			providerConfig:  piPath("models.json"),
			writeProvider:   writePiProviderJSON,
			removeProvider:  func(p string) (bool, error) { return removeJSONKeys(p, "providers", launch.PiProvider) },
			providerPresent: jsonProviderPresentAt("providers", launch.PiProvider),
		},
	}
}

func lookupAgent(id string) (agentSpec, bool) {
	for _, a := range agentRegistry() {
		if a.ID == id {
			return a, true
		}
	}
	return agentSpec{}, false
}

func agentIDs() []string {
	ids := make([]string, 0, len(agentRegistry()))
	for _, a := range agentRegistry() {
		ids = append(ids, a.ID)
	}
	return ids
}

func alwaysGlobalPath(rel string) func(bool) (string, error) {
	return func(bool) (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rel), nil
	}
}

// projectOrGlobalPath resolves the project copy for global=false and the home copy for
// global=true. claude, kimi, opencode and kilo have two MCP scopes; codex has its own
// resolver (codexMCPPath) because its global side honours $CODEX_HOME.
func projectOrGlobalPath(projectRel, globalRel string) func(bool) (string, error) {
	return func(global bool) (string, error) {
		if !global {
			cwd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			return filepath.Join(cwd, projectRel), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, globalRel), nil
	}
}

// piPath resolves inside pi's agent directory: $PI_CODING_AGENT_DIR when set,
// ~/.pi/agent otherwise — the same order pi itself resolves it, and the same
// variable `orq launch pi` sets to point pi at a temp dir.
func piPath(rel string) func(bool) (string, error) {
	return func(bool) (string, error) {
		if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
			return filepath.Join(dir, rel), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".pi", "agent", rel), nil
	}
}

// piDetect mirrors piPath so detection and the write agree on one directory.
func piDetect() bool {
	dir, err := piPath("")(true)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// codexPath resolves inside codex's config directory: $CODEX_HOME when set, ~/.codex otherwise.
func codexPath(rel string) func(bool) (string, error) {
	return func(bool) (string, error) {
		if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
			return filepath.Join(dir, rel), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".codex", rel), nil
	}
}

// codexMCPPath is codex's MCP config in either scope: the project
// .codex/config.toml at cwd, or config.toml under $CODEX_HOME (~/.codex).
// projectOrGlobalPath cannot express the env fallback and codexPath cannot
// express the project side. Codex loads the project file only for a
// repository its global config marks trusted; connect prints that line.
func codexMCPPath() func(bool) (string, error) {
	global := codexPath("config.toml")
	return func(isGlobal bool) (string, error) {
		if isGlobal {
			return global(true)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "config.toml"), nil
	}
}

func codexBaseConfigPath() string {
	path, err := codexPath("config.toml")(true)
	if err != nil {
		return "codex's config.toml"
	}
	return path
}

func detectPath(resolve func(bool) (string, error)) func() bool {
	return func() bool {
		path, err := resolve(true)
		if err != nil {
			return false
		}
		_, statErr := os.Stat(path)
		return statErr == nil
	}
}

func detectAny(relPaths ...string) func() bool {
	return func() bool {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		for _, rel := range relPaths {
			if _, err := os.Stat(filepath.Join(home, rel)); err == nil {
				return true
			}
			if _, err := os.Stat(rel); err == nil {
				return true
			}
		}
		return false
	}
}

// ============================================================================
// Agent config readers and writers
// ============================================================================

func readJSONConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON — left untouched: %w", path, err)
	}
	// Valid JSON that is not an object: `null` unmarshals into a nil map, which then panics on assignment.
	if out == nil {
		return nil, fmt.Errorf("%s is valid JSON but not an object — left untouched", path)
	}
	return out, nil
}

// writeJSONConfig writes atomically and backs the target up once: some of these files (~/.claude.json) hold the agent's entire user state.
func writeJSONConfig(path string, cfg map[string]any) error {
	return writeJSONConfigMode(path, cfg, 0o600)
}

// writeJSONConfigMode is writeJSONConfig with a caller-chosen file mode. A
// project ./.mcp.json is meant to be committed, and Claude itself creates it
// 0644 — narrowing it to writeJSONConfig's default 0600 would fight that.
func writeJSONConfigMode(path string, cfg map[string]any, mode os.FileMode) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(path, append(data, '\n'), mode)
}

// writeConfigFile is how every agent config this binary owns reaches disk:
// backed up once, written to a temp file, renamed into place. Format-agnostic
// on purpose — codex's config.toml is the same class of file as ~/.claude.json
// (the agent's own state, not ours), and a safety that lived only in the JSON
// writer was a safety codex did not get.
func writeConfigFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		backup := path + ".orq-bak"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			// A backup that silently did not happen is worse than not starting.
			original, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s to back it up: %w", path, err)
			}
			if err := os.WriteFile(backup, original, 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", backup, err)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, ".orq-cfg-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeTOMLConfig is writeConfigFile plus a parse of what is about to land:
// these TOML rewrites are line-oriented, so a mis-detected table header can
// produce a file the agent cannot load. Refusing beats reporting success over
// a codex that no longer starts.
func writeTOMLConfig(path, content string) error {
	if _, err := toml.Load(content); err != nil {
		return fmt.Errorf("rewriting %s would produce invalid TOML — left untouched: %w", path, err)
	}
	return writeConfigFile(path, []byte(content), 0o600)
}

// A present non-object value is refused, matching readJSONConfig's rule for invalid JSON: never overwrite what we cannot merge into.
func nestedMap(cfg map[string]any, key string) (map[string]any, error) {
	if existing, ok := cfg[key].(map[string]any); ok {
		return existing, nil
	}
	if v, present := cfg[key]; present {
		return nil, fmt.Errorf("%q holds %T, not an object — left untouched", key, v)
	}
	created := map[string]any{}
	cfg[key] = created
	return created, nil
}

// writeOpenCodeProviderJSON never writes the top-level "model" key: that is the user's default, and there is no profile to scope a change to.
func writeOpenCodeProviderJSON(path, routerURL, _ string, models []auth.RouterModel, defaultModel string) (int, error) {
	if len(models) == 0 {
		return 0, errNoModelsToOffer
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return 0, err
	}
	refs, infos := launchCatalog(models)
	built, err := launch.BuildOpenCodeConfigContent(routerURL, defaultModel, refs, infos, "")
	if err != nil {
		return 0, err
	}
	var generated struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.Unmarshal([]byte(built), &generated); err != nil {
		return 0, err
	}

	providers, err := nestedMap(cfg, "provider")
	if err != nil {
		return 0, err
	}
	// Drop ours first so a provider that lost all its models leaves no stale entry.
	for _, name := range []string{launch.OpenCodeChatProvider, launch.OpenCodeResponsesProvider} {
		delete(providers, name)
	}
	for name, block := range generated.Provider {
		providers[name] = block
	}
	return len(refs), writeJSONConfig(path, cfg)
}

// writePiProviderJSON registers the gateway in pi's models.json, the durable
// twin of the throwaway one `orq launch pi` writes. Merged, not overwritten: the
// same file is where users declare their own local providers (ollama, vLLM).
//
// apiKey is unused — the block carries "$ORQ_API_KEY", which pi resolves at
// request time, so no credential lands on disk. defaultModel is unused too: pi
// keeps the model it opens with in settings.json, which is the user's own pick
// made in pi's UI, and the same reason the opencode writer never touches
// "model".
func writePiProviderJSON(path, routerURL, _ string, models []auth.RouterModel, _ string) (int, error) {
	if len(models) == 0 {
		return 0, errNoModelsToOffer
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return 0, err
	}
	refs, infos := launchCatalog(models)
	built, err := launch.BuildPiModelsJSON(routerURL, refs, infos)
	if err != nil {
		return 0, err
	}
	var generated struct {
		Providers map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(built), &generated); err != nil {
		return 0, err
	}

	providers, err := nestedMap(cfg, "providers")
	if err != nil {
		return 0, err
	}
	// Drop ours first so a rerun replaces the block rather than merging into a
	// stale model list.
	delete(providers, launch.PiProvider)
	for name, block := range generated.Providers {
		providers[name] = block
	}
	return len(refs), writeJSONConfig(path, cfg)
}

// codexProfileName: $CODEX_HOME/<name>.config.toml is loaded by 'codex --profile <name>'.
const codexProfileName = "orq"

// codexDefaultModel picks a model the gateway serves natively over Responses: codex speaks only that API, and a translated model rejects its tool definitions.
func codexDefaultModel(models []auth.RouterModel, chosen string) string {
	responses := make([]auth.RouterModel, 0, len(models))
	for _, m := range models {
		if m.Metadata.SupportsResponses {
			responses = append(responses, m)
		}
	}
	for _, m := range responses {
		if m.Ref() == chosen {
			return chosen
		}
	}
	for _, group := range auth.CandidateCodingModels(responses, preferredCodingModels) {
		return group[0].Ref()
	}
	// Same order as CandidateCodingModels so the two paths agree and re-runs are stable.
	best, bestRank := "", 0
	for _, m := range responses {
		ref, rank := m.Ref(), auth.SizeVariantRank(m.ModelID)
		if best == "" || rank < bestRank || (rank == bestRank && ref > best) {
			best, bestRank = ref, rank
		}
	}
	return best
}

// writeCodexProviderTOML owns this profile file outright and rewrites it wholesale; the credential stays out, codex resolves ORQ_API_KEY via env_key.
func writeCodexProviderTOML(path, routerURL, _ string, models []auth.RouterModel, defaultModel string) (int, error) {
	if len(models) == 0 {
		return 0, errNoModelsToOffer
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	body := renderTOMLSettings(launch.CodexProviderSettings(
		codexDefaultModel(models, defaultModel), routerURL, ""))
	header := "# Written by 'orq setup'. Use it with: codex --profile " + codexProfileName + "\n" +
		"# Regenerated on every run; edit " + codexBaseConfigPath() + " instead.\n\n"
	return 0, os.WriteFile(path, []byte(header+body), 0o600)
}

// renderTOMLSettings hoists undotted keys above the tables: a root key after a table header belongs to that table.
func renderTOMLSettings(settings [][2]string) string {
	var root strings.Builder
	tables := map[string]*strings.Builder{}
	var order []string
	for _, kv := range settings {
		key, value := kv[0], kv[1]
		dot := strings.LastIndex(key, ".")
		if dot < 0 {
			fmt.Fprintf(&root, "%s = %s\n", key, launch.TOMLString(value))
			continue
		}
		header, field := key[:dot], key[dot+1:]
		b, ok := tables[header]
		if !ok {
			b = &strings.Builder{}
			tables[header] = b
			order = append(order, header)
			fmt.Fprintf(b, "[%s]\n", header)
		}
		fmt.Fprintf(b, "%s = %s\n", field, launch.TOMLString(value))
	}
	out := root.String()
	for _, header := range order {
		if out != "" {
			out += "\n"
		}
		out += tables[header].String()
	}
	return out
}

// writeKimiProviderTOML rewrites orq's tables on every run: kimi reads the credential only as a literal, so a stale key here 401s every prompt.
func writeKimiProviderTOML(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) (int, error) {
	if len(models) == 0 {
		return 0, errNoModelsToOffer
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	kept := stripOrqKimiTables(string(existing))
	// Only when the file has none: kimi writes this key when the user picks.
	// Keyed by the full ref (m.Ref()), to match the [models."<ref>"] tables below.
	prefix := ""
	if defaultModel != "" && !hasKimiDefaultModel(kept) {
		prefix = fmt.Sprintf("default_model = %q\n\n", defaultModel)
	}
	if kept = strings.TrimRight(kept, "\n"); kept != "" {
		kept += "\n\n"
	}
	kept = prefix + kept
	// No default for the builder: emitted here it would land after the user's tables and TOML would scope it to the last one.
	refs, infos := launchCatalog(models)
	block := launch.BuildKimiConfigTOML(routerURL, apiKey, "", refs, infos)
	// The file carries a literal credential; writeTOMLConfig renames a 0600
	// temp into place, so an existing wider copy is replaced rather than kept.
	if err := writeTOMLConfig(path, kept+block); err != nil {
		return 0, err
	}
	return len(refs), nil
}

func hasKimiDefaultModel(toml string) bool {
	for _, line := range strings.Split(toml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			return false // past the root table; anything below is scoped
		}
		if strings.HasPrefix(trimmed, "default_model") {
			return true
		}
	}
	return false
}

// stripOrqKimiTables removes the tables we own: re-appending without dropping them duplicates tables, which kimi refuses to parse.
func stripOrqKimiTables(toml string) string {
	var out, table strings.Builder
	header := ""
	flush := func() {
		if !orqOwnedKimiTable(header, table.String()) {
			out.WriteString(table.String())
		}
		table.Reset()
	}
	for _, line := range strings.SplitAfter(toml, "\n") {
		if h := strings.TrimSpace(strings.TrimSuffix(line, "\n")); strings.HasPrefix(h, "[") {
			flush()
			header = h
		}
		table.WriteString(line)
	}
	flush()
	return out.String()
}

// Both provider names must be listed: a missed one leaves stale tables that survive the strip, duplicate on rewrite, and make the file undecodable.
func orqOwnedKimiTable(header, body string) bool {
	for _, provider := range []string{launch.KimiChatProvider, launch.KimiResponsesProvider} {
		if header == "[providers."+provider+"]" ||
			strings.HasPrefix(header, "[providers."+provider+".") {
			return true
		}
	}
	if strings.HasPrefix(header, "[models.") {
		for _, provider := range []string{launch.KimiChatProvider, launch.KimiResponsesProvider} {
			if strings.Contains(body, `provider = "`+provider+`"`) {
				return true
			}
		}
	}
	return false
}

// ============================================================================
// MCP entry writers — the orq-workspace server, no credential ever included.
// ============================================================================

// claudeMCPEntry is claude's mcpServers value: a plain streamable-HTTP remote.
// Claude authenticates it via 'claude mcp login', not a config field.
func claudeMCPEntry(url string) map[string]any {
	return map[string]any{"type": "http", "url": url}
}

// kimiMCPEntry is kimi's mcpServers value — url only; kimi's own 'kimi mcp
// auth' drives the OAuth flow, same as claude.
func kimiMCPEntry(url string) map[string]any {
	return map[string]any{"url": url}
}

// remoteMCPEntry is launch.RemoteMCPEntry: the persistent entry and the session
// config launch injects are the same shape by construction.
func remoteMCPEntry(url string) map[string]any { return launch.RemoteMCPEntry(url) }

// writeMCPJSON writes the orq-workspace entry into a JSON config under the
// given section key ("mcpServers" for claude/kimi, "mcp" for opencode/kilo).
// The whole entry is assigned rather than merged into, so a v4.13.10 leftover
// carrying "Authorization": "Bearer {env:ORQ_API_KEY}" is upgraded to the
// headerless shape in place.
func writeMCPJSON(key string, entry func(url string) map[string]any) func(path, url string) error {
	return func(path, url string) error {
		cfg, err := readJSONConfig(path)
		if err != nil {
			return err
		}
		servers, err := nestedMap(cfg, key)
		if err != nil {
			return err
		}
		servers[launch.MCPServerName] = entry(url)
		return writeJSONConfigMode(path, cfg, mcpFileMode(path))
	}
}

// mcpFileMode preserves an existing file's mode so a write never narrows it,
// and otherwise gives a project-scoped .mcp.json the 0644 Claude itself
// creates it with — it is meant to be committed and shared with a team.
// Every other MCP config keeps writeJSONConfig's default 0600.
func mcpFileMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	if filepath.Base(path) == ".mcp.json" {
		return 0o644
	}
	return 0o600
}

// kiloMCPPresent checks kilo.json, which is where we write, and kilo.jsonc,
// which the shipped kilo binary also reads — an entry made directly against
// the jsonc file must still read back as wired.
func kiloMCPPresent(path string) bool {
	check := jsonProviderPresentAt("mcp", launch.MCPServerName)
	if check(path) {
		return true
	}
	if strings.HasSuffix(path, ".json") {
		return check(path + "c")
	}
	return false
}

// kiloRemoveMCP is kiloMCPPresent's write-side pair: it strips the entry from
// both files the reader accepts, so a jsonc-configured kilo can be disconnected.
func kiloRemoveMCP(path string) (bool, error) {
	removed, err := removeJSONKeys(path, "mcp", launch.MCPServerName)
	if err != nil {
		return removed, err
	}
	// Only when the reader can actually see an entry there: a kilo.jsonc
	// carrying comments does not parse as JSON, and reporting that as a
	// disconnect failure would be a lie about a file holding no orq entry.
	if !strings.HasSuffix(path, ".json") || !jsonProviderPresentAt("mcp", launch.MCPServerName)(path+"c") {
		return removed, nil
	}
	removedJSONC, err := removeJSONKeys(path+"c", "mcp", launch.MCPServerName)
	return removed || removedJSONC, err
}

// writeCodexMCPTOML merges the orq-workspace table into codex's base
// config.toml, the file codex always reads. writeCodexProviderTOML owns the
// profile file outright and rewrites it wholesale, so MCP — which codex does
// not scope by profile — cannot live there without being destroyed on the
// next 'orq connect gateway'.
func writeCodexMCPTOML(path, url string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	kept, _ := stripTOMLTables(string(data), codexOwnedMCPTable)
	if kept = strings.TrimRight(kept, "\n"); kept != "" {
		kept += "\n\n"
	}
	block := fmt.Sprintf("[mcp_servers.%s]\nurl = %s\n", launch.MCPServerName, launch.TOMLString(url))
	return writeTOMLConfig(path, kept+block)
}

// codexOwnedMCPTable claims exactly the orq-workspace table (and any nested
// under it), so stripping it for a rewrite leaves every other table in
// config.toml — the user's own MCP servers included — byte-identical.
//
// Compares normalized segments, not the raw header string: TOML allows a
// dotted key segment to be quoted (`[mcp_servers."orq-workspace"]` is the
// same table as `[mcp_servers.orq-workspace]`), and a hand-written or
// tool-written quoted header that a raw comparison failed to claim would be
// left behind, then duplicated by the next write — a config.toml with two
// `[mcp_servers.orq-workspace]` tables is not valid TOML and codex would
// refuse to load it at all.
func codexOwnedMCPTable(header string) bool {
	segments := normalizeTOMLHeaderSegments(header)
	want := []string{"mcp_servers", launch.MCPServerName}
	if len(segments) < len(want) {
		return false
	}
	for i, w := range want {
		if segments[i] != w {
			return false
		}
	}
	return true
}

// normalizeTOMLHeaderSegments splits a table header into its dotted
// segments, stripping the brackets and unquoting each segment so
// `[a."b".c]`, `[a.b.c]`, and `[ a . b . c ]` all normalize the same way.
func normalizeTOMLHeaderSegments(header string) []string {
	trimmed := strings.TrimSpace(header)
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	parts := strings.Split(trimmed, ".")
	segments := make([]string, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		segments[i] = p
	}
	return segments
}

// ============================================================================
// Doctor checks
// ============================================================================

// codingAgentChecks reports one row per detected agent — whether a provider
// config is on disk, whether the credential in it is current, and whether
// ORQ_API_KEY is exported — plus one coding_agents summary row.
// Detected-but-unwired is info, not warn: leaving an agent unconnected is a
// choice, and only the states that actually break something (wired without
// ORQ_API_KEY, or wired with a superseded key) warn. Statuses never drive
// doctor's exit code.
//
// activeWorkspace flags a wired agent pinned elsewhere; "" means unknown and
// never produces that row.
func codingAgentChecks(activeWorkspace string) []doctorCheck {
	var checks []doctorCheck
	var wiredIDs []string
	detected, fullyWired := 0, 0
	// Read once, both are per-process: detectShell spells the source line for fish and honours a non-default config directory.
	keyExported := agentKeyExported()
	savedKey, savedWS := savedAPIKey()
	sourceLine := detectShell(viper.GetString("config-directory")).displayLine()
	for _, spec := range agentRegistry() {
		if !spec.detect() {
			continue // nothing to say about an agent that is not installed
		}
		if spec.writeProvider == nil {
			continue
		}
		details := map[string]any{}
		path, wired := wiredPath(spec.providerConfig, spec.providerPresent)
		if wired {
			details["provider"] = path
		}
		details["api_key_in_env"] = keyExported
		// A record can outlive its config, so it is never proof of wiring alone.
		recordedWS, _ := agentWiring(spec.ID)
		// Only on a wired agent: "run 'orq connect X' (workspace acme)" would read
		// as if connecting lands in acme, when acme is just a stale record.
		if recordedWS != "" {
			details["workspace"] = recordedWS
		}
		wsNote := ""
		if wired && recordedWS != "" {
			wsNote = " (workspace " + recordedWS + ")"
		}
		check := doctorCheck{ID: "coding_agent_" + spec.ID, Details: details}

		detected++
		if wired {
			fullyWired++
			wiredIDs = append(wiredIDs, spec.ID)
		}
		switch {
		case !wired:
			check.Status = "info"
			check.Message = spec.Label + " detected but not wired — run 'orq connect " + spec.ID + "'"
		case wired && staleEmbeddedKey(spec, path, savedKey):
			// A config holding the key literally cannot follow a renewal. Wiring
			// one agent renews for all of them, so an agent left out of that run
			// keeps a key that dies on the old expiry date while every other row
			// here stays green.
			details["stale_key"] = true
			check.Status = "warn"
			check.Message = spec.Label + " holds an older key than the one saved — it will stop authenticating. " +
				"Run 'orq connect " + spec.ID + "' to rewire it" + wsNote
		// kimi's provider TOML holds the key literally, so an unexported ORQ_API_KEY breaks nothing.
		case keyExported || spec.providerEmbedsKey:
			check.Status = "pass"
			check.Message = spec.Label + " is wired to orq" + wsNote
		default:
			check.Status = "warn"
			check.Message = spec.Label + " is wired, but ORQ_API_KEY is not set in this shell — " +
				"agents started from here will fail to authenticate. Run '" + sourceLine + "', or start them from a new shell" + wsNote
		}
		checks = append(checks, check)
		// Pinning is the design, so a different workspace is information, not a
		// fault — never warn.
		if wired && keyWorkspaceMismatch(recordedWS, activeWorkspace) {
			// resolveConnectAuth rejects 'orq connect' when the saved key belongs to
			// another workspace, so the remedy has to mint one first.
			remedy := launch.RemedyForWorkspace(spec.ID, savedWS, activeWorkspace)
			// setup only mints; only 'orq connect <id>' repoints the agent.
			action := "run '" + remedy + "' to move it to " + activeWorkspace
			// Mirrors RemedyForWorkspace: an empty savedWS already yields
			// 'orq connect <id>', which does not mint a key.
			if savedWS != "" && savedWS != activeWorkspace {
				action = "run '" + remedy + "' to mint a key for " + activeWorkspace +
					", then 'orq connect " + spec.ID + "' to move it there"
			}
			checks = append(checks, doctorCheck{
				ID:     "agent_workspace_" + spec.ID,
				Status: "info",
				// Exempt from printDoctorSummary's healthy-row collapse: this is the
				// only place naming where an agent is pinned.
				AlwaysShow: true,
				Message:    spec.Label + " is pinned to workspace " + recordedWS + ", the workspace it was connected against — " + action,
			})
		}
	}
	if detected > 0 {
		checks = append(checks, codingAgentsSummary(detected, fullyWired, wiredIDs))
	}
	return checks
}

// staleEmbeddedKey reports a wired config whose literal credential is not the
// one this machine has saved. Both sides must be known: no saved key, or a
// config we cannot read a key out of, is "unknown", never "stale".
func staleEmbeddedKey(spec agentSpec, path, savedKey string) bool {
	if spec.providerKey == nil || savedKey == "" {
		return false
	}
	embedded := spec.providerKey(path)
	return embedded != "" && embedded != savedKey
}

// codingAgentsSummary is the one row the human view keeps when every per-agent
// check is healthy. Never warn: how many agents to connect is the user's call.
func codingAgentsSummary(detected, fullyWired int, wiredIDs []string) doctorCheck {
	check := doctorCheck{
		ID:      "coding_agents",
		Status:  "info",
		Details: map[string]any{"detected": detected, "wired": fullyWired},
	}
	if fullyWired == detected {
		check.Status = "pass"
	}
	if fullyWired == 0 {
		check.Message = fmt.Sprintf("0 of %d wired — run 'orq connect'", detected)
	} else {
		check.Message = fmt.Sprintf("%d of %d wired: %s", fullyWired, detected, strings.Join(wiredIDs, ", "))
	}
	return check
}

// wiredPath checks both scopes: setup may have written either, and checking only the global one misreported project-scoped agents as unwired.
func wiredPath(resolve func(bool) (string, error), present func(string) bool) (string, bool) {
	if resolve == nil || present == nil {
		return "", false
	}
	for _, global := range []bool{false, true} {
		path, err := resolve(global)
		if err != nil || path == "" {
			continue
		}
		if present(path) {
			return path, true
		}
	}
	return "", false
}

// jsonProviderPresentAt reads a JSON provider map: opencode and kilo keep theirs
// under "provider", pi under "providers". Parametrised rather than one detector
// per format, so a new JSON agent cannot land in someone else's key by accident.
func jsonProviderPresentAt(key, provider string) func(path string) bool {
	return func(path string) bool {
		cfg, err := readJSONConfig(path)
		if err != nil {
			return false
		}
		providers, ok := cfg[key].(map[string]any)
		if !ok {
			return false
		}
		_, present := providers[provider]
		return present
	}
}

// tomlStringAt reads one string out of a TOML file, empty when the file, the
// path or the type does not hold.
func tomlStringAt(dotted string) func(path string) string {
	return func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		tree, err := toml.LoadBytes(data)
		if err != nil {
			return ""
		}
		s, _ := tree.GetPath(strings.Split(dotted, ".")).(string)
		return strings.TrimSpace(s)
	}
}

// tomlTablePresent parses rather than substring-matches: a commented-out block would otherwise read as wired.
func tomlTablePresent(table string) func(path string) bool {
	return func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		tree, err := toml.LoadBytes(data)
		if err != nil {
			return false
		}
		return tree.HasPath(strings.Split(table, "."))
	}
}

// agentKeyExported is deliberately narrower than envAPIKeySet: agent configs interpolate ORQ_API_KEY by name, so ORQ_TOKEN alone must not pass.
// The snapshot, not the environment: our own PreRun injects a session token into ORQ_API_KEY, which would report every shell as already exporting it.
func agentKeyExported() bool {
	return UserEnvAPIKey() != ""
}

// ============================================================================
// Removers — the inverse of each writer, for `orq disconnect`
// ============================================================================

// removeJSONKeys deletes keys from one section of a JSON config. Reports
// whether anything was removed. A file we created (no prior backup) that ends
// empty is deleted; a user file is rewritten, never removed.
func removeJSONKeys(path, section string, keys ...string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil || cfg == nil {
		return false, fmt.Errorf("%s is not a JSON object — left untouched", path)
	}
	sec, ok := cfg[section].(map[string]any)
	if !ok {
		return false, nil
	}
	removed := false
	for _, k := range keys {
		if _, present := sec[k]; present {
			delete(sec, k)
			removed = true
		}
	}
	if !removed {
		return false, nil
	}
	if len(sec) == 0 {
		delete(cfg, section)
	}
	if len(cfg) == 0 {
		if _, err := os.Stat(path + ".orq-bak"); errors.Is(err, os.ErrNotExist) {
			return true, os.Remove(path)
		}
	}
	// writeJSONConfigMode, not writeJSONConfig: a project .mcp.json was written
	// 0644 by writeMCPJSON, and rewriting it here through the 0600 default would
	// narrow it right back — the mode discipline has to hold in both directions.
	return true, writeJSONConfigMode(path, cfg, mcpFileMode(path))
}

// stripTOMLTables drops every table whose header the predicate claims,
// preserving the rest of the file verbatim. Lines before the first header
// belong to the root table and are always kept.
func stripTOMLTables(content string, owned func(header string) bool) (string, bool) {
	var out, table strings.Builder
	header := ""
	removed := false
	flush := func() {
		if header != "" && owned(header) {
			removed = true
		} else {
			out.WriteString(table.String())
		}
		table.Reset()
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		if h := strings.TrimSpace(strings.TrimSuffix(line, "\n")); strings.HasPrefix(h, "[") {
			flush()
			header = h
		}
		table.WriteString(line)
	}
	flush()
	return out.String(), removed
}

// removeTOMLTables applies stripTOMLTables to a file. A file left with only
// whitespace held nothing but our block, so it is deleted.
func removeTOMLTables(path string, owned func(header string) bool) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	kept, removed := stripTOMLTables(string(data), owned)
	if !removed {
		return false, nil
	}
	if strings.TrimSpace(kept) == "" {
		return true, os.Remove(path)
	}
	kept = strings.TrimRight(kept, "\n") + "\n"
	return true, writeTOMLConfig(path, kept)
}

func removeOpenCodeProviders(path string) (bool, error) {
	return removeJSONKeys(path, "provider", launch.OpenCodeChatProvider, launch.OpenCodeResponsesProvider)
}

// The codex profile file is entirely ours; removal is deletion.
func removeCodexProfile(path string) (bool, error) {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func removeKimiProviderTOML(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	kept := stripOrqKimiTables(string(data))
	if kept == string(data) {
		return false, nil
	}
	kept = strings.TrimLeft(dropDanglingKimiDefault(kept), "\n")
	if strings.TrimSpace(kept) == "" {
		return true, os.Remove(path)
	}
	kept = strings.TrimRight(kept, "\n") + "\n"
	return true, writeTOMLConfig(path, kept)
}

// dropDanglingKimiDefault removes a default_model whose model table is gone.
// The writer only ever adds the key alongside its table, so a dangling one is
// ours; a user's own default still has its table and is kept.
func dropDanglingKimiDefault(content string) string {
	var def string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "default_model") {
			if _, v, ok := strings.Cut(t, "="); ok {
				def = strings.Trim(strings.TrimSpace(v), `"`)
			}
			break
		}
	}
	if def == "" || strings.Contains(content, "[models."+fmt.Sprintf("%q", def)+"]") {
		return content
	}
	var out strings.Builder
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "default_model") {
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

// skillsCheck reports the states nothing else converges on its own: a
// recorded link whose path is gone, one whose path something else has taken
// over, an install left behind by a CLI update, and a manifest that cannot be
// read at all. Each is real, persistent, and otherwise invisible — refresh
// acts on none of them between fingerprint changes, and on a foreign path it
// never acts at all.
//
// The states come from skills.ReadStatus, not from a local os.Lstat: a path
// existing is not a path being ours, and doctor calling a user-replaced
// directory "installed" is exactly the gap that made the difference matter.
//
// Collapsed per directory, like connect --status's version: deleting a
// skills directory records a dozen missing links, and a dozen lines pointing
// at one remedy say nothing a count would not.
func skillsCheck() (doctorCheck, bool) {
	status, err := skills.ReadStatus()
	if err != nil {
		// A manifest that exists and will not load is the one thing here the
		// user cannot see any other way: every skills command fails against
		// it, and doctor is where they come to find out why. Reporting no
		// check at all would hide it precisely when there is something to
		// report. A machine that never connected returns nil, nil instead.
		return doctorCheck{
			ID:      "skills",
			Status:  "fail",
			Message: fmt.Sprintf("the orq skills manifest could not be read: %v", err),
		}, true
	}
	if status == nil || len(status.Links) == 0 {
		return doctorCheck{}, false
	}
	recorded := len(status.Links)
	missing := status.Count(skills.LinkMissing)
	foreign := status.Count(skills.LinkForeign)
	// Three buckets, three remedies: a global link is put back by a bare
	// connect, a local one by connect --local from this directory, and a
	// local install elsewhere is only counted — the fix is to run doctor
	// from there.
	var missingGlobal, missingLocal, elsewhere int
	for _, l := range status.Links {
		switch {
		case l.Place == skills.PlaceElsewhere:
			elsewhere++
		case l.State != skills.LinkMissing:
		case l.Place == skills.PlaceGlobal:
			missingGlobal++
		default:
			missingLocal++
		}
	}
	check := doctorCheck{
		ID: "skills",
		Details: map[string]any{
			"recorded":  recorded,
			"missing":   missing,
			"foreign":   foreign,
			"elsewhere": elsewhere,
			"stale":     status.Stale,
		},
	}
	switch {
	case missingGlobal == 0 && missingLocal == 0 && foreign == 0 && !status.Stale:
		check.Status = "pass"
		check.Message = fmt.Sprintf("%d orq skills installed", recorded)
	case missingGlobal > 0 && missingLocal > 0:
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d of %d recorded orq skills are not installed in %s — run 'orq connect skills' for the global set and 'orq connect skills --local' from this directory for the local one",
			missingGlobal+missingLocal, recorded, strings.Join(skillDirsIn(status, skills.LinkMissing, ""), ", "))
	case missingGlobal > 0:
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d of %d recorded orq skills are not installed in %s — run 'orq connect skills' to install them",
			missingGlobal, recorded, strings.Join(skillDirsIn(status, skills.LinkMissing, skills.PlaceGlobal), ", "))
	case missingLocal > 0:
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d of %d recorded orq skills are not installed in %s — run 'orq connect skills --local' from that directory",
			missingLocal, recorded, strings.Join(skillDirsIn(status, skills.LinkMissing, skills.PlaceLocal), ", "))
	case foreign > 0:
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d of %d recorded orq skills are no longer ours — %s holds something orq did not put there, and orq will not update or delete it. Run 'orq disconnect skills' to stop tracking it",
			foreign, recorded, strings.Join(skillDirsIn(status, skills.LinkForeign, ""), ", "))
	default:
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d orq skills are from an older CLI version — run 'orq connect skills' to update them", recorded)
	}
	if elsewhere > 0 {
		check.Message += fmt.Sprintf(" (%d links in other directories are not checked from here)", elsewhere)
	}
	return check, true
}

// skillDirsIn names the directories holding links in the given state, sorted
// and home-abbreviated for display. An empty place means any place.
func skillDirsIn(status *skills.Status, state skills.LinkState, place skills.Place) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range status.Links {
		dir := filepath.Dir(l.Path)
		if l.State != state || seen[dir] || (place != "" && l.Place != place) {
			continue
		}
		seen[dir] = true
		out = append(out, tilde(dir))
	}
	sort.Strings(out)
	return out
}
