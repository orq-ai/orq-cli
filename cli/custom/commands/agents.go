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
			ID:     "claude",
			Label:  "Claude Code",
			detect: detectAny(".claude", ".claude.json"),
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
		},
		{
			ID:    "opencode",
			Label: "opencode",
			// Global only: opencode and kilo reject {env:...} references in a project config.
			detect:          detectAny(".config/opencode"),
			providerConfig:  alwaysGlobalPath(".config/opencode/opencode.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			removeProvider:  removeOpenCodeProviders,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
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
		},
		{
			ID:    "kilo",
			Label: "Kilo Code",
			// Global only — same env-reference restriction as opencode above.
			detect:          detectAny(".config/kilo"),
			providerConfig:  alwaysGlobalPath(".config/kilo/kilo.json"),
			writeProvider:   writeOpenCodeProviderJSON,
			removeProvider:  removeOpenCodeProviders,
			providerPresent: jsonProviderPresentAt("provider", launch.OpenCodeChatProvider),
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
func codingAgentChecks() []doctorCheck {
	var checks []doctorCheck
	var wiredIDs []string
	detected, fullyWired := 0, 0
	// Read once, both are per-process: detectShell spells the source line for fish and honours a non-default config directory.
	keyExported := agentKeyExported()
	savedKey, _ := savedAPIKey()
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
				"Run 'orq connect " + spec.ID + "' to rewire it"
		// kimi's provider TOML holds the key literally, so an unexported ORQ_API_KEY breaks nothing.
		case keyExported || spec.providerEmbedsKey:
			check.Status = "pass"
			check.Message = spec.Label + " is wired to orq"
		default:
			check.Status = "warn"
			check.Message = spec.Label + " is wired, but ORQ_API_KEY is not set in this shell — " +
				"agents started from here will fail to authenticate. Run '" + sourceLine + "', or start them from a new shell"
		}
		checks = append(checks, check)
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
	return true, writeJSONConfig(path, cfg)
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
	return true, os.WriteFile(path, []byte(kept), 0o600)
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
	return true, os.WriteFile(path, []byte(kept), 0o600)
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
