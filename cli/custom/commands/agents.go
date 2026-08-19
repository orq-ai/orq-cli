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
)

// mcpServerName must equal launch's key: launch injects a session-scoped entry
// under the same name, and two different keys make an agent load both servers.
const mcpServerName = launch.MCPServerName

// errNoModelsToOffer guards a shipped bug: the writers clear their own keys
// before emitting, so writing an empty catalogue deletes a working config.
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
	ID    string
	Label string
	// mcpConfig returns the config path for the requested scope, or "" when the
	// agent has no MCP support at all.
	mcpConfig func(global bool) (string, error)
	// writeMCP registers the server in an existing-or-new config file.
	writeMCP func(path, url string) error
	// manualSnippet is printed when writeMCP fails so the user can finish by hand.
	manualSnippet func(url string) string
	// detect reports whether the agent looks installed on this machine.
	detect func() bool
	// providerConfig returns the file that registers orq as an LLM provider, so
	// the agent's own model calls route through the orq gateway. Empty for
	// agents where we do not configure one.
	providerConfig func(global bool) (string, error)
	// writeProvider registers the provider and a set of models in that file,
	// returning how many models it actually listed — not every format takes a
	// catalogue, and reporting the number we were handed rather than the number
	// we wrote would overstate what the user got.
	// apiKey is written literally when the agent cannot read it from env
	// (kimi); the file must be 0600.
	// defaultModel is the model the agent should open with, already proven to
	// answer. Writers fill it only when the config has none: the agent persists
	// the user's own pick there, and overwriting it would silently undo a
	// choice they made in the agent's UI.
	writeProvider func(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) (int, error)
	// providerUsage is the step the user must take to reach the gateway, empty when
	// the agent needs none.
	providerUsage string
}

// preferredCodingModels are matched as prefixes against the live catalogue, in
// order. Anything not currently offered is skipped rather than written as a
// dead entry.
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
			// Codex reads MCP servers only from its config directory, so both scopes
			// resolve to the same absolute path.
			mcpConfig:      codexPath("config.toml"),
			writeMCP:       writeMCPCodexTOML,
			manualSnippet:  snippetMCPCodexTOML,
			detect:         detectPath(codexPath("")),
			providerConfig: codexPath(codexProfileName + ".config.toml"),
			writeProvider:  writeCodexProviderTOML,
			providerUsage:  "run 'codex --profile " + codexProfileName + "' to route through the gateway",
		},
		{
			ID:    "opencode",
			Label: "opencode",
			// Global only: opencode and kilo reject {env:ORQ_API_KEY} references in a
			// project config.
			mcpConfig:      alwaysGlobalPath(".config/opencode/opencode.json"),
			writeMCP:       writeMCPRemoteJSON,
			manualSnippet:  snippetMCPRemoteJSON,
			detect:         detectAny(".config/opencode"),
			providerConfig: alwaysGlobalPath(".config/opencode/opencode.json"),
			writeProvider:  writeOpenCodeProviderJSON,
			providerUsage:  "pick an " + launch.ProviderDisplayName + " model in opencode's model list",
		},
		{
			ID:            "kimi",
			Label:         "Kimi Code",
			mcpConfig:     pathFor(".kimi-code/mcp.json", ".kimi-code/mcp.json"),
			writeMCP:      writeMCPKimiJSON,
			manualSnippet: snippetMCPKimiJSON,
			detect:        detectAny(".kimi-code", ".kimi"),
			// Kimi reads config.toml only from the home directory.
			providerConfig: alwaysGlobalPath(".kimi-code/config.toml"),
			writeProvider:  writeKimiProviderTOML,
		},
		{
			ID:    "kilo",
			Label: "Kilo Code",
			// Global only — same env-reference restriction as opencode above.
			mcpConfig:      alwaysGlobalPath(".config/kilo/kilo.json"),
			writeMCP:       writeMCPRemoteJSON,
			manualSnippet:  snippetMCPRemoteJSON,
			detect:         detectAny(".config/kilo"),
			providerConfig: alwaysGlobalPath(".config/kilo/kilo.json"),
			writeProvider:  writeOpenCodeProviderJSON,
			providerUsage:  "pick an " + launch.ProviderDisplayName + " model in kilo's model list",
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

// pathFor builds a scope-aware path resolver. Project paths are relative to the
// working directory, global paths to $HOME.
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

// alwaysGlobalPath resolves under $HOME whatever scope was requested, for
// agents that only ever read a home-directory config.
func alwaysGlobalPath(rel string) func(bool) (string, error) {
	return func(bool) (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rel), nil
	}
}

// codexPath resolves against $CODEX_HOME when set, ~/.codex otherwise: codex
// resolves config.toml and profiles the same way.
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

// codexBaseConfigPath is the config.toml codex loads, named in the header we write.
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

// readJSONConfig loads an existing config, tolerating a missing file. A file
// that exists but does not parse is an error: we never clobber something we do
// not understand.
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

// writeJSONConfig writes atomically via a temp file in the same directory. When
// the target already exists it is backed up once, because some of these files
// (notably ~/.claude.json) hold the agent's entire user state.
func writeJSONConfig(path string, cfg map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		backup := path + ".orq-bak"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			// Fail rather than overwrite: the whole point of the backup is to
			// survive this write, so a backup that silently did not happen is
			// worse than not starting.
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

// nestedMap fetches cfg[key] as a map, creating it when absent. A non-map value
// under that key is replaced rather than merged.
func nestedMap(cfg map[string]any, key string) map[string]any {
	if existing, ok := cfg[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	cfg[key] = created
	return created
}

// writeMCPServersJSON handles the `mcpServers` shape used by Claude Code.
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

// writeMCPRemoteJSON handles the `mcp` shape used by opencode and kilo.
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

// writeMCPKimiJSON handles Kimi Code, which takes the env var by name rather
// than interpolating it into a header.
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

// writeMCPCodexTOML appends to Codex's config.toml. We append rather than parse
// so unrelated TOML is never rewritten; an existing section means we are done.
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

// writeOpenCodeProviderJSON never writes the top-level "model": orq is offered as
// an option, never made the user's default, since setup cannot guarantee
// ORQ_API_KEY is exported.
func writeOpenCodeProviderJSON(path, routerURL, _ string, models []auth.RouterModel, defaultModel string) (int, error) {
	// Every writer below clears the keys it owns before emitting new ones, so
	// an empty catalogue would delete a working block and report success. The
	// caller skips this case; this is the backstop for any other caller.
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
	// Drop ours first so a provider left with no models does not survive as a
	// stale entry.
	for _, name := range []string{launch.OpenCodeChatProvider, launch.OpenCodeResponsesProvider} {
		delete(providers, name)
	}
	for name, block := range generated.Provider {
		providers[name] = block
	}
	return len(refs), writeJSONConfig(path, cfg)
}

// codexProfileName is both the file stem and the value the user passes:
// $CODEX_HOME/<name>.config.toml is loaded by `codex --profile <name>`.
const codexProfileName = "orq"

// codexDefaultModel: codex speaks only the Responses API, so its default must be
// a model the gateway serves natively over Responses, not one it translates.
func codexDefaultModel(models []auth.RouterModel, proven string) string {
	responses := make([]auth.RouterModel, 0, len(models))
	for _, m := range models {
		if m.Metadata.SupportsResponses {
			responses = append(responses, m)
		}
	}
	for _, m := range responses {
		if m.Ref() == proven {
			return proven
		}
	}
	for _, group := range auth.CandidateCodingModels(responses, preferredCodingModels) {
		return group[0].Ref()
	}
	// Fallback: same rank-then-lexical rule as CandidateCodingModels, tiebroken on
	// the full ref so re-runs are stable.
	best, bestRank := "", 0
	for _, m := range responses {
		ref, rank := m.Ref(), auth.SizeVariantRank(m.ModelID)
		if best == "" || rank < bestRank || (rank == bestRank && ref > best) {
			best, bestRank = ref, rank
		}
	}
	return best
}

// writeCodexProviderTOML writes the codex profile. The file is ours alone, plain
// `codex` never reads it, so it is rewritten wholesale rather than merged.
//
// No model catalog: generating one needs codex installed (`codex debug models
// --bundled`). The cost is cosmetic, its picker lists codex's bundled models and
// it warns "model metadata not found", while requests still route.
func writeCodexProviderTOML(path, routerURL, _ string, models []auth.RouterModel, defaultModel string) (int, error) {
	// Every writer below clears the keys it owns before emitting new ones, so
	// an empty catalogue would delete a working block and report success. The
	// caller skips this case; this is the backstop for any other caller.
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

// renderTOMLSettings hoists undotted keys above the tables: a root key written
// after a table header is silently swallowed into that table.
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

// writeKimiProviderTOML registers orq as an OpenAI-compatible provider and adds
// the given models. Only the tables this command owns are rewritten; unrelated
// TOML survives untouched, for the same reason as the Codex config.
//
// The orq tables are regenerated on every run rather than skipped when present.
// Setup mints a fresh key each time, and builds before v0.2 wrote an
// `api_key = "${ORQ_API_KEY}"` placeholder that kimi never interpolates — a
// skip-if-present leaves that dead credential in place forever, and the user
// gets a 401 on their first prompt with no hint why.
func writeKimiProviderTOML(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) (int, error) {
	// Every writer below clears the keys it owns before emitting new ones, so
	// an empty catalogue would delete a working block and report success. The
	// caller skips this case; this is the backstop for any other caller.
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
	// Fill the default only when the file has none. kimi writes this key itself
	// when the user picks a model, so a value here is their preference, not
	// ours to replace. Keyed by ModelID to match the [models."<id>"] tables
	// below — the full provider/model ref would resolve to nothing.
	prefix := ""
	if defaultModel != "" && !hasKimiDefaultModel(kept) {
		prefix = fmt.Sprintf("default_model = %q\n\n", defaultModel)
	}
	if kept = strings.TrimRight(kept, "\n"); kept != "" {
		kept += "\n\n"
	}
	kept = prefix + kept
	// No default model: emitted here it would land after the user's tables, where
	// TOML reads a root key as a member of the last one. `prefix` hoists it instead.
	refs, infos := launchCatalog(models)
	block := launch.BuildKimiConfigTOML(routerURL, apiKey, "", refs, infos)
	// The file carries a literal credential; a pre-existing copy may have been
	// created with looser permissions, so chmod as well as write at 0600.
	if err := os.WriteFile(path, []byte(kept+block), 0o600); err != nil {
		return 0, err
	}
	return len(refs), os.Chmod(path, 0o600)
}

// hasKimiDefaultModel reports whether the config already names a default model.
// Only top-level assignments count: a `default_model` inside some other table
// belongs to that table, not to kimi's root config.
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

// stripOrqKimiTables removes the TOML tables this command owns so they can be
// written from scratch. Dropping them is not optional: re-appending would give
// the file duplicate tables, which kimi refuses to parse.
//
// ponytail: a `default_model` naming a removed orq model is left dangling — if
// that model is gone from the catalogue the config is broken either way. Rewrite
// default_model when its target disappears if that turns out to bite.
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

// orqOwnedKimiTable reports whether a TOML table was written by this command:
// either orq provider (including sub-tables such as the `env` map older builds
// emitted), or a model routed through one of them.
//
// Both provider names must be listed. When the Responses provider was added,
// this function still knew only about "orq", so on every re-run the stale
// [providers.orq-responses] block and every model pointing at it survived the
// strip and were written again — duplicate tables, which makes the whole file
// undecodable and leaves kimi with no models at all. Anything a new provider
// adds to launch.BuildKimiConfigTOML has to be recognised here in the same
// change; the names are read from that package so at least they cannot drift.
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
