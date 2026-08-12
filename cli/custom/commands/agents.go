package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orq/cli/custom/auth"
)

// mcpServerName is the key every agent config registers orq under. It matches
// the name used by the orq-mcp plugin so both installs look identical.
const mcpServerName = "orq-workspace"

// providerName is the key orq is registered under in an agent's LLM provider
// config, so its model calls route through the orq gateway.
const providerName = "orq"

// kimi declares one provider per API shape. The names and types match
// launch.KimiChatProvider / KimiResponsesProvider so a config written by
// `orq setup` and one injected by `orq launch` describe the same thing.
const (
	kimiChatProvider      = providerName
	kimiResponsesProvider = "orq-responses"
	kimiChatType          = "openai"
	kimiResponsesType     = "openai_responses"
)

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
	// writeProvider registers the provider and a set of models in that file.
	// apiKey is written literally when the agent cannot read it from env
	// (kimi); the file must be 0600.
	// defaultModel is the model the agent should open with, already proven to
	// answer. Writers fill it only when the config has none: the agent persists
	// the user's own pick there, and overwriting it would silently undo a
	// choice they made in the agent's UI.
	writeProvider func(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) error
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
			// Codex reads MCP servers only from the home-directory TOML. A
			// project-relative copy would be written and never loaded, so both
			// scopes resolve to the same absolute path.
			mcpConfig:     alwaysGlobalPath(".codex/config.toml"),
			writeMCP:      writeMCPCodexTOML,
			manualSnippet: snippetMCPCodexTOML,
			detect:        detectAny(".codex"),
		},
		{
			ID:            "opencode",
			Label:         "opencode",
			mcpConfig:     pathFor("opencode.json", ".config/opencode/opencode.json"),
			writeMCP:      writeMCPRemoteJSON,
			manualSnippet: snippetMCPRemoteJSON,
			detect:        detectAny(".config/opencode"),
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
			ID:            "kilo",
			Label:         "Kilo Code",
			mcpConfig:     pathFor(".kilo/kilo.json", ".config/kilo/kilo.json"),
			writeMCP:      writeMCPRemoteJSON,
			manualSnippet: snippetMCPRemoteJSON,
			detect:        detectAny(".config/kilo"),
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

// writeKimiProviderTOML registers orq as an OpenAI-compatible provider and adds
// the given models. Only the tables this command owns are rewritten; unrelated
// TOML survives untouched, for the same reason as the Codex config.
//
// The orq tables are regenerated on every run rather than skipped when present.
// Setup mints a fresh key each time, and builds before v0.2 wrote an
// `api_key = "${ORQ_API_KEY}"` placeholder that kimi never interpolates — a
// skip-if-present leaves that dead credential in place forever, and the user
// gets a 401 on their first prompt with no hint why.
func writeKimiProviderTOML(path, routerURL, apiKey string, models []auth.RouterModel, defaultModel string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
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
	// The file carries a literal credential; a pre-existing copy may have been
	// created with looser permissions, so chmod as well as write at 0600.
	if err := os.WriteFile(path, []byte(kept+kimiProviderBlock(routerURL, apiKey, models)), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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
// the orq provider (including sub-tables such as the `env` map older builds
// emitted), or a model routed through it.
func orqOwnedKimiTable(header, body string) bool {
	switch {
	case header == "[providers."+providerName+"]",
		strings.HasPrefix(header, "[providers."+providerName+"."):
		return true
	case strings.HasPrefix(header, "[models."):
		return strings.Contains(body, `provider = "`+providerName+`"`)
	}
	return false
}

func kimiProviderBlock(routerURL, apiKey string, models []auth.RouterModel) string {
	var b strings.Builder

	// Two providers, one per API shape, matching what `orq launch` writes:
	// models the gateway serves via the Responses API belong on a provider of
	// type openai_responses. Writing everything as chat completions still
	// works — chat serves every model — but loses the tools+reasoning path
	// that gpt-5.x and the azure gpt-5 line actually want.
	chat, responses := []auth.RouterModel{}, []auth.RouterModel{}
	for _, m := range models {
		if m.Metadata.SupportsResponses {
			responses = append(responses, m)
		} else {
			chat = append(chat, m)
		}
	}

	provider := func(name, typ string) {
		fmt.Fprintf(&b, "[providers.%s]\n", name)
		fmt.Fprintf(&b, "type = %q\n", typ)
		fmt.Fprintf(&b, "base_url = %q\n", routerURL)
		// Kimi 0.34 has no env fallback or ${VAR} interpolation for provider
		// credentials; the literal key is the only working form.
		fmt.Fprintf(&b, "api_key = %q\n", apiKey)
	}
	if len(chat) > 0 {
		provider(kimiChatProvider, kimiChatType)
	}
	if len(responses) > 0 {
		if len(chat) > 0 {
			b.WriteString("\n")
		}
		provider(kimiResponsesProvider, kimiResponsesType)
	}

	writeModel := func(m auth.RouterModel, providerKey string) {
		context := m.Metadata.ContextWindow
		if context <= 0 {
			// Kimi requires the field; a conservative floor beats omitting it.
			context = 128000
		}
		output := m.Metadata.MaxOutputTokens
		if output <= 0 {
			// Kimi sends max_tokens = max_output_size on every call, so a guess
			// above the model's real cap makes the upstream reject every
			// request. Under-guessing only truncates.
			output = 8192
		}
		// Keyed by the full provider/model ref, not ModelID: several providers
		// serve the same id (google/gemini-2.5-flash and
		// google-ai/gemini-2.5-flash, azure/gpt-4o and openai/gpt-4o), and
		// duplicate table keys make the whole file invalid TOML — kimi then
		// discards every model, not just the clashing ones.
		fmt.Fprintf(&b, "\n[models.%q]\n", m.Ref())
		fmt.Fprintf(&b, "provider = %q\n", providerKey)
		fmt.Fprintf(&b, "model = %q\n", m.Ref())
		fmt.Fprintf(&b, "max_context_size = %d\n", context)
		fmt.Fprintf(&b, "max_output_size = %d\n", output)
	}
	for _, m := range chat {
		writeModel(m, kimiChatProvider)
	}
	for _, m := range responses {
		writeModel(m, kimiResponsesProvider)
	}
	return b.String()
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
