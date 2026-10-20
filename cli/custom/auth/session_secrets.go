package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// The store-failure notices are held to once per process so they do not repeat
// on every read (hydrateSecrets runs the migration on each read, and Save runs on
// each write). A failed migrate and a failed save are distinct notices, so each
// gets its own Once — one firing must never silence the other.
var (
	migrateFailureWarned sync.Once
	saveFailureWarned    sync.Once
)

func warnStoreFailureOnce(once *sync.Once, format string, a ...any) {
	once.Do(func() { fmt.Fprintf(os.Stderr, format, a...) })
}

// session_secrets.go splits a Session's secret token material from its
// inspectable state. When a SecretStore is active the tokens live in the OS
// store and the JSON file keeps only non-secret state (URLs, user, active
// workspace/project, each token's expiry and workspace id, the gateway key's
// metadata, the stale-token map). With no store, or ORQ_CREDENTIAL_STORE=file,
// nothing here changes: the tokens stay inline in the JSON exactly as before.

// sessionSecrets is the secret half of a Session, stored as one JSON blob under
// the session host. Only the token strings move; every token's metadata stays
// in the session file, so `orq auth sessions` still reads from the file alone.
type sessionSecrets struct {
	RefreshToken    string            `json:"refreshToken,omitempty"`
	BootstrapToken  string            `json:"bootstrapToken,omitempty"`
	WorkspaceTokens map[string]string `json:"workspaceTokens,omitempty"`
	GatewayKey      string            `json:"gatewayKey,omitempty"`
}

// activeStore is the store the session read/write paths consult. A package var
// so tests can inject a fake without a real keychain or a particular GOOS; it
// returns nil for the file fallback. Resolved once per process: on Linux the
// choice costs a secret-tool probe, and a read and the write after it in one
// command must not pick different stores.
var activeStore = sync.OnceValue(func() SecretStore { return ResolveSecretStore(os.Getenv) })

// hostFromSessionPath recovers the session host from a session file path. The
// file is named "<host>.json" (sessionPathFor), so the store is keyed the same
// way the files are and multi-profile keeps working.
func hostFromSessionPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json")
}

func (s *Session) extractSecrets() sessionSecrets {
	sec := sessionSecrets{
		RefreshToken:   s.RefreshToken,
		BootstrapToken: s.BootstrapToken.Token,
		GatewayKey:     s.GatewayKey,
	}
	for k, tok := range s.WorkspaceTokens {
		if tok.Token == "" {
			continue
		}
		if sec.WorkspaceTokens == nil {
			sec.WorkspaceTokens = make(map[string]string, len(s.WorkspaceTokens))
		}
		sec.WorkspaceTokens[k] = tok.Token
	}
	return sec
}

// hasInlineSecrets reports whether any token string sits in the struct. True on
// a fresh login and on a pre-migration file; false once the secrets have moved
// to the store and the file was rewritten without them.
func (s *Session) hasInlineSecrets() bool {
	if s.RefreshToken != "" || s.BootstrapToken.Token != "" || s.GatewayKey != "" {
		return true
	}
	for _, tok := range s.WorkspaceTokens {
		if tok.Token != "" {
			return true
		}
	}
	return false
}

// stripSecrets blanks the token strings on the receiver, leaving every token's
// metadata. WorkspaceTokens is reassigned to a fresh map rather than edited in
// place: a shallow Session copy shares that map with the caller, and blanking it
// in place would wipe the tokens from the live session mid-migration.
func (s *Session) stripSecrets() {
	s.RefreshToken = ""
	s.BootstrapToken.Token = ""
	s.GatewayKey = ""
	if len(s.WorkspaceTokens) == 0 {
		return
	}
	stripped := make(map[string]StoredAccessToken, len(s.WorkspaceTokens))
	for k, tok := range s.WorkspaceTokens {
		tok.Token = ""
		stripped[k] = tok
	}
	s.WorkspaceTokens = stripped
}

// applySecrets copies stored token strings back onto a Session read from a
// secret-free file, matching each workspace token to its metadata slot.
func (s *Session) applySecrets(sec sessionSecrets) {
	s.RefreshToken = sec.RefreshToken
	s.BootstrapToken.Token = sec.BootstrapToken
	s.GatewayKey = sec.GatewayKey
	if len(sec.WorkspaceTokens) > 0 && s.WorkspaceTokens == nil {
		s.WorkspaceTokens = make(map[string]StoredAccessToken, len(sec.WorkspaceTokens))
	}
	for k, tok := range sec.WorkspaceTokens {
		wt := s.WorkspaceTokens[k]
		wt.Token = tok
		s.WorkspaceTokens[k] = wt
	}
}

// hydrateSecrets makes a Session read from disk whole: with a store active it
// either migrates inline secrets into the store (rewriting the file without
// them) or loads them from the store onto the secret-free file. With no store
// it is a no-op for inline tokens, and an error for a file whose tokens were
// moved to a store this process is not using.
func hydrateSecrets(s *Session, path string) error {
	host := hostFromSessionPath(path)
	store := activeStore()
	if store == nil {
		if s.SecretStore != "" && !s.hasInlineSecrets() {
			remedy := "make the store reachable"
			if credentialStoreForcedFile() {
				remedy = "unset " + CredentialStoreEnv
			}
			return fmt.Errorf("the %s session secrets are in the %s, which this command is not using (%s). "+
				"To use them, %s; or run `orq auth login` to sign in again with the secrets in the session file",
				host, s.SecretStore, storeUnusedReason(), remedy)
		}
		return nil
	}
	defer lockSecretStore(path)()
	// Re-read under the lock: s was read before it, so another process may have
	// migrated or saved since, and the file and store must come from one write.
	if cur, err := readSessionFile(path); err == nil && cur != nil {
		*s = *cur
	}
	if s.hasInlineSecrets() {
		return migrateSecretsToStore(s, path, host, store)
	}
	blob, ok, err := store.Load(host)
	if err != nil {
		return fmt.Errorf("reading credential store: %w", err)
	}
	if !ok {
		if s.SecretStore != "" {
			return fmt.Errorf("the %s holds no session secrets for %s (removed outside orq?); run `orq auth login`", store.Name(), host)
		}
		// Secret-free file and nothing in the store: the login is incomplete.
		// Leave the tokens blank so validateSession reports it, rather than
		// pretending it is usable.
		return nil
	}
	var sec sessionSecrets
	if err := json.Unmarshal([]byte(blob), &sec); err != nil {
		return fmt.Errorf("credential store holds malformed secrets: %w", err)
	}
	s.applySecrets(sec)
	return nil
}

// storeUnusedReason says why ResolveSecretStore came back empty, for the
// message that points a user at secrets this process cannot reach.
func storeUnusedReason() string {
	if credentialStoreForcedFile() {
		return CredentialStoreEnv + "=file is set"
	}
	return "no credential store is reachable from this process, for example over SSH without a desktop session"
}

// migrateSecretsToStore moves a file's inline secrets into the store and
// rewrites the file without them, once, on the first read after the store
// becomes available. The store write comes first: if it fails the file is left
// untouched with its secrets, so a login is never lost to a half-migration. The
// in-memory Session keeps its secrets either way, so the command that triggered
// the migration authenticates normally.
func migrateSecretsToStore(s *Session, path, host string, store SecretStore) error {
	blob, err := json.Marshal(s.extractSecrets())
	if err != nil {
		return err
	}
	if err := store.Save(host, string(blob)); err != nil {
		// Could not move the secrets into the store. The file keeps them inline
		// and the in-memory session already holds them, so the login still works;
		// warn and leave the file untouched rather than failing the command. A
		// Load failure (secrets only in an unreachable store) is different and
		// does surface as an error in hydrateSecrets.
		warnStoreFailureOnce(&migrateFailureWarned,
			"orq: could not move %s session secrets into the %s (%v); they remain in %s. Run orq doctor.\n",
			host, store.Name(), err, path)
		return nil
	}
	stripped := *s
	stripped.stripSecrets()
	stripped.SecretStore = store.Name()
	data, err := json.MarshalIndent(stripped, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteSecretFile(path, data); err != nil {
		return err
	}
	s.SecretStore = store.Name()
	fmt.Fprintf(os.Stderr, "orq: moved %s session secrets into the %s\n", host, store.Name())
	return nil
}

func credentialStoreForcedFile() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(CredentialStoreEnv)), "file")
}

// MigrateSessionSecrets moves the inline secrets of every host's session file
// into the store, not just the active host's, so a login to a server you no
// longer use does not keep its refresh token in plaintext until you next use
// it. MigrateLayout calls it before every command; it never fails the command.
// Once everything has moved it costs a directory read. While some file still
// holds inline secrets it also resolves the store (on Linux a secret-tool
// probe, which any session read pays anyway), and a store that refuses the
// write warns once and stops the sweep for this process, leaving the files as
// they were; the next command tries again, as the migrate-on-read always has.
func MigrateSessionSecrets() {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return
	}
	var store SecretStore
	resolved := false
	for _, e := range entries {
		path, s := inlineSessionFile(e)
		if s == nil {
			continue
		}
		if !resolved {
			store, resolved = activeStore(), true
		}
		if store == nil {
			return
		}
		func() {
			defer lockSecretStore(path)()
			// Re-read under the lock: another process may have just migrated it.
			cur, err := readSessionFile(path)
			if err != nil || cur == nil || !cur.hasInlineSecrets() {
				return
			}
			if err := migrateSecretsToStore(cur, path, hostFromSessionPath(path), store); err != nil {
				fmt.Fprintf(os.Stderr, "orq: could not rewrite %s without its secrets (%v)\n", path, err)
			}
		}()
		if cur, err := readSessionFile(path); err == nil && cur != nil && cur.hasInlineSecrets() {
			return // the store refused the write; do not repeat it per host
		}
	}
}

// inlineSessionFile returns a host session file that still holds token
// strings, or nil. Only files already named for their host qualify: the store
// is keyed by that name, and MigrateLayout renames the rest first.
func inlineSessionFile(e os.DirEntry) (string, *Session) {
	name := e.Name()
	if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, apiKeyLoginSuffix) {
		return "", nil
	}
	path := filepath.Join(sessionsDir(), name)
	s, err := readSessionFile(path)
	if err != nil || s == nil || s.APIBaseURL == "" || !s.hasInlineSecrets() {
		return "", nil
	}
	if sessionPathFor(SessionHost(s.APIBaseURL)) != path {
		return "", nil
	}
	return path, s
}

// SecretsLocation reports where login secrets actually sit, for `orq doctor`.
// It reads the session files as stored, without hydrating: inline token
// material means the secrets are in a file whatever store resolves, so a
// failed store write or a not-yet-migrated file is reported honestly instead
// of as the store. Every host's file is checked, plus the `.deprecated` copies
// migrateSessionFiles parks, since each can hold a working refresh token.
// degraded marks the states worth a warning: a store is available but some
// secrets are still in plaintext, or no store resolved and some login's
// secrets are in a store this process is not using.
func SecretsLocation() (desc string, degraded bool) {
	files := scanSessionFiles()
	store := activeStore()
	if store == nil {
		desc = "plaintext session file (0600)"
		if runtime.GOOS == "linux" && toolInstalled("secret-tool") && !credentialStoreForcedFile() {
			desc += "; secret-tool is installed but no Secret Service answered"
		}
		if len(files.inStore) > 0 {
			return fmt.Sprintf("%s; %s keep their secrets in a credential store this command is not using",
				desc, strings.Join(files.inStore, ", ")), true
		}
		return desc, false
	}
	if len(files.plaintext) == 0 {
		return store.Name(), false
	}
	return fmt.Sprintf("%s, but plaintext tokens remain in %s (a store write may have failed; delete a .deprecated file you no longer need)",
		store.Name(), strings.Join(files.plaintext, ", ")), true
}

type sessionFileScan struct {
	plaintext []string // hold inline token strings
	inStore   []string // stripped, with a SecretStore marker
}

// scanSessionFiles sorts the session files, by base name, into those holding
// inline token strings and those whose tokens went to a store.
func scanSessionFiles() sessionFileScan {
	var out sessionFileScan
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, apiKeyLoginSuffix) ||
			!(strings.HasSuffix(name, ".json") || strings.Contains(name, ".json.deprecated")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionsDir(), name))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(data, &s) != nil || s.APIBaseURL == "" {
			continue
		}
		switch {
		case s.hasInlineSecrets():
			out.plaintext = append(out.plaintext, name)
		case s.SecretStore != "":
			out.inStore = append(out.inStore, name)
		}
	}
	return out
}
