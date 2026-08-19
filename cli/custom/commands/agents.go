package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"

	"github.com/pelletier/go-toml"
	"github.com/spf13/viper"
)

const mcpServerName = launch.MCPServerName

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
	ID            string
	Label         string
	mcpConfig     func(global bool) (string, error)
	writeMCP      func(path, url string) error
	manualSnippet func(url string) string
	detect        func() bool
	// providerConfig returns the file registering orq as an LLM provider; empty when we configure none.
	providerConfig func(global bool) (string, error)
	// writeProvider registers the provider and its models, returning how many models it listed.
	writeProvider func(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) (int, error)
	// providerPresent is the read-side pair of writeProvider, in that agent's own format; required whenever writeProvider is set.
	providerPresent func(path string) bool
	providerUsage   string
	// runCommand starts the agent against what setup wrote; empty means the bare ID does.
	runCommand string
}

func (s agentSpec) startCommand() string {
	if s.runCommand != "" {
		return s.runCommand
	}
	return s.ID
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
			ID:            "claude",
			Label:         "Claude Code",
			mcpConfig:     pathFor(".mcp.json", ".claude.json"),
			writeMCP:      writeMCPServersJSON,
			manualSnippet: snippetMCPServersJSON,
			detect:        detectAny(".claude", ".claude.json"),
		},
		{
			ID:    "codex",
			Label: "Codex",
			// Codex reads MCP servers only from its config directory, so both scopes resolve there.
			mcpConfig:     codexPath("config.toml"),
			writeMCP:      writeMCPCodexTOML,
			manualSnippet: snippetMCPCodexTOML,
			// Same resolution the writers use, or a CODEX_HOME machine is never offered codex.
			detect:          detectPath(codexPath("")),
			providerConfig:  codexPath(codexProfileName + ".config.toml"),
			writeProvider:   writeCodexProviderTOML,
			providerPresent: tomlTablePresent("model_providers." + launch.CodexProvider),
			providerUsage:   "run 'codex --profile " + codexProfileName + "'",
			runCommand:      "codex --profile " + codexProfileName,
		},
		{
			ID:    "opencode",
			Label: "opencode",
			// Global only: opencode and kilo reject {env:...} references in a project config.
			mcpConfig:       alwaysGlobalPath(".config/opencode/opencode.json"),
			writeMCP:        writeMCPRemoteJSON,
			manualSnippet:   snippetMCPRemoteJSON,
			detect:          detectAny(".config/opencode"),
			providerConfig:  alwaysGlobalPath(".config/opencode/opencode.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
			providerUsage:   "pick an " + launch.ProviderDisplayName + " model",
		},
		{
			ID:            "kimi",
			Label:         "Kimi Code",
			mcpConfig:     pathFor(".kimi-code/mcp.json", ".kimi-code/mcp.json"),
			writeMCP:      writeMCPKimiJSON,
			manualSnippet: snippetMCPKimiJSON,
			detect:        detectAny(".kimi-code", ".kimi"),
			// Kimi reads config.toml only from the home directory.
			providerConfig:  alwaysGlobalPath(".kimi-code/config.toml"),
			writeProvider:   writeKimiProviderTOML,
			providerPresent: tomlTablePresent("providers." + launch.KimiChatProvider),
		},
		{
			ID:    "kilo",
			Label: "Kilo Code",
			// Global only — same env-reference restriction as opencode above.
			mcpConfig:       alwaysGlobalPath(".config/kilo/kilo.json"),
			writeMCP:        writeMCPRemoteJSON,
			manualSnippet:   snippetMCPRemoteJSON,
			detect:          detectAny(".config/kilo"),
			providerConfig:  alwaysGlobalPath(".config/kilo/kilo.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
			providerUsage:   "pick an " + launch.ProviderDisplayName + " model",
		},
		{
			ID:    "pi",
			Label: "Pi Coding Agent",
			// Gateway only: pi has no native MCP, extensions only.
			detect: detectAny(".pi/agent", ".pi"),
			// pi reads models.json from its agent dir ($PI_CODING_AGENT_DIR, ~/.pi/agent) only.
			providerConfig:  alwaysGlobalPath(".pi/agent/models.json"),
			writeProvider:   writePiProviderJSON,
			providerPresent: jsonProviderPresentAt("providers", launch.PiProvider),
			// The model has to be named. Verified against pi 0.83.0: a bare
			// `pi --provider orq` answers from whatever model pi already had
			// selected, so a hint stopping at the provider teaches a command that
			// silently routes nowhere near orq.
			providerUsage: "run 'pi --model " + launch.PiProvider + "/<model>', or pick one in /model",
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

func pathFor(project, global string) func(bool) (string, error) {
	return func(useGlobal bool) (string, error) {
		if useGlobal {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, global), nil
		}
		return project, nil
	}
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
// MCP config writers
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
		return nil, fmt.Errorf("%s is not valid JSON — left untouched", path)
	}
	return out, nil
}

// writeJSONConfig writes atomically and backs the target up once: some of these files (~/.claude.json) hold the agent's entire user state.
func writeJSONConfig(path string, cfg map[string]any) error {
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
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
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
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func nestedMap(cfg map[string]any, key string) map[string]any {
	if existing, ok := cfg[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	cfg[key] = created
	return created
}

func writeMCPServersJSON(path, url string) error {
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	nestedMap(cfg, "mcpServers")[mcpServerName] = map[string]any{
		"type":    "http",
		"url":     url,
		"headers": map[string]any{"Authorization": "Bearer ${ORQ_API_KEY}"},
	}
	return writeJSONConfig(path, cfg)
}

func writeMCPRemoteJSON(path, url string) error {
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	nestedMap(cfg, "mcp")[mcpServerName] = map[string]any{
		"type":    "remote",
		"url":     url,
		"enabled": true,
		"headers": map[string]any{"Authorization": "Bearer {env:ORQ_API_KEY}"},
	}
	return writeJSONConfig(path, cfg)
}

func writeMCPKimiJSON(path, url string) error {
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	nestedMap(cfg, "mcpServers")[mcpServerName] = map[string]any{
		"url":               url,
		"bearerTokenEnvVar": "ORQ_API_KEY",
	}
	return writeJSONConfig(path, cfg)
}

// writeMCPCodexTOML appends rather than parses, so unrelated TOML is never rewritten.
func writeMCPCodexTOML(path, url string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if strings.Contains(string(existing), "[mcp_servers."+mcpServerName+"]") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	block := snippetMCPCodexTOML(url)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		block = "\n" + block
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + block)
	return err
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

	providers := nestedMap(cfg, "provider")
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

	providers := nestedMap(cfg, "providers")
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
	// The file carries a literal credential, so chmod an existing copy as well as writing 0600.
	if err := os.WriteFile(path, []byte(kept+block), 0o600); err != nil {
		return 0, err
	}
	return len(refs), os.Chmod(path, 0o600)
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

func snippetMCPServersJSON(url string) string {
	return fmt.Sprintf(`"mcpServers": {
  "%s": {
    "type": "http",
    "url": "%s",
    "headers": { "Authorization": "Bearer ${ORQ_API_KEY}" }
  }
}`, mcpServerName, url)
}

func snippetMCPRemoteJSON(url string) string {
	return fmt.Sprintf(`"mcp": {
  "%s": {
    "type": "remote",
    "url": "%s",
    "enabled": true,
    "headers": { "Authorization": "Bearer {env:ORQ_API_KEY}" }
  }
}`, mcpServerName, url)
}

func snippetMCPKimiJSON(url string) string {
	return fmt.Sprintf(`"mcpServers": {
  "%s": {
    "url": "%s",
    "bearerTokenEnvVar": "ORQ_API_KEY"
  }
}`, mcpServerName, url)
}

func snippetMCPCodexTOML(url string) string {
	return fmt.Sprintf(`[mcp_servers.%s]
url = "%s"
bearer_token_env_var = "ORQ_API_KEY"
`, mcpServerName, url)
}

// ============================================================================
// Doctor checks
// ============================================================================

// codingAgentChecks reports, per detected agent, whether MCP and the provider are wired. Detected-but-unwired is a warn; statuses never drive doctor's exit code.
func codingAgentChecks() []doctorCheck {
	var checks []doctorCheck
	// Read once, both are per-process: detectShell spells the source line for fish and honours a non-default config directory.
	keyExported := agentKeyExported()
	sourceLine := detectShell(viper.GetString("config-directory")).displayLine()
	for _, spec := range agentRegistry() {
		if !spec.detect() {
			continue // nothing to say about an agent that is not installed
		}
		details := map[string]any{}
		wired := 0
		offered := 0

		if spec.writeMCP != nil {
			offered++
			if path, ok := wiredPath(spec.mcpConfig, mcpEntryPresent); ok {
				wired++
				details["mcp"] = path
			}
		}
		if spec.writeProvider != nil {
			offered++
			if path, ok := wiredPath(spec.providerConfig, spec.providerPresent); ok {
				wired++
				details["provider"] = path
			}
		}

		check := doctorCheck{ID: "coding_agent_" + spec.ID, Details: details}
		// Every config except kimi's provider TOML reaches the key through ORQ_API_KEY, so a wired agent in a shell without it 401s on every call.
		details["api_key_in_env"] = keyExported

		switch {
		case offered == 0:
			continue
		case wired == offered && !keyExported:
			check.Status = "warn"
			check.Message = spec.Label + " is wired, but ORQ_API_KEY is not set in this shell — " +
				"agents started from here will fail to authenticate. Run '" + sourceLine +
				"', or start them from a new shell"
		case wired == offered:
			check.Status = "pass"
			check.Message = spec.Label + " is wired to orq"
		case wired == 0:
			check.Status = "warn"
			check.Message = spec.Label + " detected but not wired — run 'orq setup coding-agents'"
		default:
			check.Status = "warn"
			check.Message = spec.Label + " is partially wired — run 'orq setup coding-agents'"
		}
		checks = append(checks, check)
	}
	return checks
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

func mcpEntryPresent(path string) bool {
	cfg, err := readJSONConfig(path)
	if err != nil {
		// Codex keeps MCP servers in TOML, not JSON.
		return tomlTablePresent("mcp_servers." + mcpServerName)(path)
	}
	for _, key := range []string{"mcpServers", "mcp"} {
		if servers, ok := cfg[key].(map[string]any); ok {
			if _, present := servers[mcpServerName]; present {
				return true
			}
		}
	}
	return false
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
func agentKeyExported() bool {
	return strings.TrimSpace(os.Getenv("ORQ_API_KEY")) != ""
}
