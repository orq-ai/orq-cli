package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
// returns nil for the file fallback.
var activeStore = func() SecretStore { return ResolveSecretStore(os.Getenv) }

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
// it is a no-op — the tokens are already inline.
func hydrateSecrets(s *Session, path string) error {
	store := activeStore()
	if store == nil {
		return nil
	}
	host := hostFromSessionPath(path)
	if s.hasInlineSecrets() {
		return migrateSecretsToStore(s, path, host, store)
	}
	blob, ok, err := store.Load(host)
	if err != nil {
		return fmt.Errorf("reading credential store: %w", err)
	}
	if !ok {
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
		return fmt.Errorf("writing credential store: %w", err)
	}
	stripped := *s
	stripped.stripSecrets()
	data, err := json.MarshalIndent(stripped, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteSecretFile(path, data); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "orq: moved %s session secrets into the %s\n", host, store.Name())
	return nil
}

// ActiveStoreDescription names the store in use for `orq doctor`. getenv is
// injected so the diagnostic reports what this invocation would actually use.
func ActiveStoreDescription(getenv func(string) string) string {
	if s := ResolveSecretStore(getenv); s != nil {
		return s.Name()
	}
	return "plaintext file (0600)"
}
