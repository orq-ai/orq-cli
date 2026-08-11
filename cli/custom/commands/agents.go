package commands

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orq/cli/custom/auth"
)

// mcpServerName is the key every agent config registers orq under. It matches
// the name used by the orq-mcp plugin so both installs look identical.
const mcpServerName = "orq-workspace"

// providerName is the key orq is registered under in an agent's LLM provider
// config, so its model calls route through the orq gateway.
const providerName = "orq"

// skillsTarballURL is the source of truth for orq skills. orq-ai/orq-skills
// redirects here.
const skillsTarballURL = "https://github.com/orq-ai/assistant-plugins/archive/refs/heads/main.tar.gz"

// sharedSkillsDir is the cross-agent skills convention (agentskills.io), read
// by pi, kimi and opencode. claude and codex need their own copies.
const sharedSkillsDir = ".agents/skills"

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
	// skillsDir is the agent-specific skills directory, or "" when the agent
	// reads the shared .agents/skills location.
	skillsDir func(global bool) (string, error)
	// detect reports whether the agent looks installed on this machine.
	detect func() bool
	// providerConfig returns the file that registers orq as an LLM provider, so
	// the agent's own model calls route through the orq gateway. Empty for
	// agents where we do not configure one.
	providerConfig func(global bool) (string, error)
	// writeProvider registers the provider and a set of models in that file.
	// apiKey is written literally when the agent cannot read it from env
	// (kimi); the file must be 0600.
	writeProvider func(path, routerURL, apiKey string, models []auth.RouterModel) error
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
			skillsDir:     pathFor(".claude/skills", ".claude/skills"),
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
			skillsDir:     pathFor(".codex/skills", ".codex/skills"),
			detect:        detectAny(".codex"),
		},
		{
			ID:            "opencode",
			Label:         "opencode",
			mcpConfig:     pathFor("opencode.json", ".config/opencode/opencode.json"),
			writeMCP:      writeMCPRemoteJSON,
			manualSnippet: snippetMCPRemoteJSON,
			skillsDir:     nil,
			detect:        detectAny(".config/opencode"),
		},
		{
			ID:            "kimi",
			Label:         "Kimi Code",
			mcpConfig:     pathFor(".kimi-code/mcp.json", ".kimi-code/mcp.json"),
			writeMCP:      writeMCPKimiJSON,
			manualSnippet: snippetMCPKimiJSON,
			skillsDir:     nil,
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
			skillsDir:     nil,
			detect:        detectAny(".config/kilo"),
		},
		{
			ID:    "pi",
			Label: "pi",
			// pi has no MCP support; skills only.
			mcpConfig: func(bool) (string, error) { return "", nil },
			skillsDir: nil,
			detect:    detectAny(".pi"),
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
			if original, err := os.ReadFile(path); err == nil {
				_ = os.WriteFile(backup, original, 0o600)
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
func writeKimiProviderTOML(path, routerURL, apiKey string, models []auth.RouterModel) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	kept := stripOrqKimiTables(string(existing))
	if kept = strings.TrimRight(kept, "\n"); kept != "" {
		kept += "\n\n"
	}
	// The file carries a literal credential; a pre-existing copy may have been
	// created with looser permissions, so chmod as well as write at 0600.
	if err := os.WriteFile(path, []byte(kept+kimiProviderBlock(routerURL, apiKey, models)), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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
	fmt.Fprintf(&b, "[providers.%s]\n", providerName)
	b.WriteString("type = \"openai\"\n")
	fmt.Fprintf(&b, "base_url = %q\n", routerURL)
	// Kimi 0.34 has no env fallback or ${VAR} interpolation for provider
	// credentials; the literal key is the only working form.
	fmt.Fprintf(&b, "api_key = %q\n", apiKey)
	for _, m := range models {
		context := m.Metadata.ContextWindow
		if context <= 0 {
			// Kimi requires the field; a conservative floor beats omitting it.
			context = 128000
		}
		fmt.Fprintf(&b, "\n[models.%q]\n", m.ModelID)
		fmt.Fprintf(&b, "provider = %q\n", providerName)
		fmt.Fprintf(&b, "model = %q\n", m.Ref())
		fmt.Fprintf(&b, "max_context_size = %d\n", context)
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

// ============================================================================
// Skills
// ============================================================================

// skillsCacheDir keeps one extracted copy per process run so instrumenting five
// agents does not download the tarball five times.
func skillsCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orq", "cache", "skills"), nil
}

// skillsCacheFresh reports whether fetchSkills will hit the day-old cache, so
// callers can announce the download that otherwise looks like a hang.
func skillsCacheFresh() bool {
	cache, err := skillsCacheDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(cache, ".fetched"))
	return err == nil && time.Since(info.ModTime()) < 24*time.Hour
}

// fetchSkills downloads and extracts the skills tarball, returning the
// directory holding the individual skill folders. A cached copy less than a day
// old is reused.
func fetchSkills() (string, error) {
	cache, err := skillsCacheDir()
	if err != nil {
		return "", err
	}
	marker := filepath.Join(cache, ".fetched")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		if dir, err := findSkillsRoot(cache); err == nil {
			return dir, nil
		}
	}

	if err := os.RemoveAll(cache); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Get(skillsTarballURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading skills failed with status %d", res.StatusCode)
	}
	if err := extractTarGz(res.Body, cache); err != nil {
		return "", err
	}
	_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
	return findSkillsRoot(cache)
}

// archiveSkillsDirs are the paths the skills tarball may keep its skill folders
// under, most current first. This is deliberately not sharedSkillsDir: that
// constant is where skills are *installed* (the agentskills.io convention), and
// conflating the two broke every install when assistant-plugins moved its
// skills to the repo root and left .agents/ holding only plugin metadata.
var archiveSkillsDirs = []string{"skills", ".agents/skills"}

// findSkillsRoot locates the skills directory inside the extracted tarball,
// whose top level is a single repo-name-and-ref folder.
func findSkillsRoot(cache string) (string, error) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, dir := range archiveSkillsDirs {
			candidate := filepath.Join(cache, entry.Name(), dir)
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("skills archive contained none of " + strings.Join(archiveSkillsDirs, ", "))
}

func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			// Cap each entry so a malicious archive cannot fill the disk.
			if _, err := io.Copy(out, io.LimitReader(tr, 32<<20)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
		// Symlinks and everything else are skipped by design.
	}
}

// safeJoin rejects archive entries that would escape the destination directory.
// Traversal is refused outright rather than silently normalised away: a well
// formed skills archive never contains such an entry, so one means the download
// is not what we think it is.
func safeJoin(dest, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to extract %q outside the destination", name)
	}
	target := filepath.Join(dest, cleaned)
	if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to extract %q outside the destination", name)
	}
	return target, nil
}

// installSkills copies every skill folder from src into dest, overwriting files
// that are already there. Returns the number of skills installed.
func installSkills(src, dest string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dest, entry.Name())); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func copyTree(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
