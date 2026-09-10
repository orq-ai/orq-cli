package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

const (
	sessionDirName     = ".orq"
	sessionsSubdirName = "sessions"
	legacyFileName     = "session.json"
)

type StoredAccessToken struct {
	Token       string `json:"token"`
	ExpiresAt   string `json:"expiresAt"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type SessionUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type Session struct {
	Version            int              `json:"version"`
	APIBaseURL         string           `json:"apiBaseUrl"`
	V1BaseURL          string           `json:"v1BaseUrl"`
	AuthBaseURL        string           `json:"authBaseUrl"`
	ProfileBaseURL     string           `json:"profileBaseUrl"`
	User               *SessionUser     `json:"user"`
	Workspaces         []map[string]any `json:"workspaces"`
	ActiveWorkspaceKey *string          `json:"activeWorkspaceKey"`
	ActiveProjectID    string           `json:"activeProjectId,omitempty"`
	ActiveProjectName  string           `json:"activeProjectName,omitempty"`

	// The secrets. Embedded anonymously, so they marshal to the same top-level
	// JSON keys they always have when they are written inline, and so every
	// `session.RefreshToken` / `session.WorkspaceTokens[k]` in the codebase
	// still resolves. When SecretStore is set they live there instead and this
	// is zero on disk; ReadSession fills it back in before any caller sees it.
	SessionSecrets

	// SecretStore names the store holding SessionSecrets, and is empty when
	// they are inline in this file. Set together with Version 2 — see
	// sessionVersionExternal for why the version moves too.
	SecretStore string `json:"secretStore,omitempty"`

	// ProfileTransport records which profile endpoint this host answered on
	// ("rpc" or "rest"), so a deployment whose ingress does not route the
	// identity RPC is not re-probed before every profile fetch. Empty on
	// sessions written before it existed, and on those the RPC is tried first
	// exactly as it was then.
	ProfileTransport string `json:"profileTransport,omitempty"`

	// The id of the gateway key `orq setup` minted from this login for coding
	// agents (the handle for revoking it), its expiry, and the workspace and
	// project it was minted for. The key itself is a secret and lives in
	// SessionSecrets; this metadata deliberately stays in the file, so
	// doctor's expiry check and logout's "revoke it with
	// `orq api-keys delete <id>`" keep working when the keychain is locked or
	// the item is gone.
	GatewayKeyID        string `json:"gatewayKeyId,omitempty"`
	GatewayKeyExpiresAt string `json:"gatewayKeyExpiresAt,omitempty"`
	GatewayWorkspace    string `json:"gatewayWorkspace,omitempty"`
	GatewayProject      string `json:"gatewayProject,omitempty"`
}

type SessionInspectStatus string

const (
	StatusOK         SessionInspectStatus = "ok"
	StatusMissing    SessionInspectStatus = "missing"
	StatusInvalid    SessionInspectStatus = "invalid"
	StatusUnreadable SessionInspectStatus = "unreadable"
)

type SessionInspectResult struct {
	Status  SessionInspectStatus
	Path    string
	Session *Session
	Code    string
	Message string
}

// SessionHost names the session file for a server: the host, lowercased, with
// `_<port>` when one is present and anything outside [a-z0-9.-] replaced by
// `_`. No scheme — http and https to one host are one login. The hosted
// service answers under two names, and those are one login too.
func SessionHost(apiBase string) string {
	apiBase = strings.TrimSpace(apiBase)
	if IsHostedAPIBase(apiBase) {
		apiBase = DefaultAPIBaseURL
	}
	u, err := url.Parse(apiBase)
	if err != nil || u.Hostname() == "" {
		return sanitizeHost(apiBase)
	}
	name := u.Hostname()
	if p := u.Port(); p != "" {
		name += "_" + p
	}
	return sanitizeHost(name)
}

func sanitizeHost(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func sessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, sessionDirName, sessionsSubdirName)
}

func sessionPathFor(host string) string {
	return filepath.Join(sessionsDir(), host+".json")
}

// sessionHostForPath is sessionPathFor backwards. Every reader and writer of
// secrets keys the store item off the session file's own name, so a caller
// holding a path (migration, attachToSession) and a caller holding a resolved
// server (SaveSession) always agree on the account.
func sessionHostForPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json")
}

// SessionFilePath is the session for the server this invocation resolved
// (custom.resolveServer → SetServer), so `--server https://my.staging.orq.ai`
// reads the staging login and a bare `orq` reads the hosted one.
func SessionFilePath() string {
	return sessionPathFor(SessionHost(ResolveURLs("").APIBaseURL))
}

func legacySessionFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, sessionDirName, legacyFileName)
}

// SessionsDir exposes the directory holding the per-server session files, so
// a caller outside this package (doctor's permission check) can enumerate it
// without reverse-engineering the layout from SessionFilePath.
func SessionsDir() string {
	return sessionsDir()
}

// LegacySessionFilePath exposes the pre-multi-profile `~/.orq/session.json`
// path. It normally disappears into the host-keyed layout the first time
// MigrateLayout runs, but a caller auditing credentials on disk should not
// have to assume that migration already ran.
func LegacySessionFilePath() string {
	return legacySessionFilePath()
}

// supportedSessionVersion is the one predicate InspectSession, ListSessions and
// doctor share, because a login one of them calls readable and another calls
// broken is worse than either answer on its own.
func supportedSessionVersion(v int) bool {
	return v == sessionVersionInline || v == sessionVersionExternal
}

func validateSession(s *Session) error {
	if !supportedSessionVersion(s.Version) {
		return errors.New("unsupported session version")
	}
	if s.APIBaseURL == "" || s.AuthBaseURL == "" || s.V1BaseURL == "" || s.ProfileBaseURL == "" {
		return errors.New("session is missing required URL fields")
	}
	if s.RefreshToken == "" {
		return errors.New("session is missing refresh token")
	}
	if s.BootstrapToken.Token == "" || s.BootstrapToken.ExpiresAt == "" {
		return errors.New("session is missing bootstrap token")
	}
	if s.WorkspaceTokens == nil {
		s.WorkspaceTokens = map[string]StoredAccessToken{}
	}
	return nil
}

// loadExternalSecrets fills s.SessionSecrets from the secret store when the
// file says they live outside it, and is a no-op when they are inline.
//
// The three outcomes are deliberately distinct. A non-nil error means the store
// itself could not answer — a locked keychain, a keyring daemon that died — and
// the login may well be intact, so callers report it as unreadable rather than
// invalid. found=false means the store answered and has no such item, which is
// a session whose secrets are genuinely gone. Anything else is a usable login.
func loadExternalSecrets(s *Session, host string) (found bool, err error) {
	if s.SecretStore == "" {
		return true, nil
	}
	store, err := resolveStore()
	if err != nil {
		return false, err
	}
	raw, err := store.Get(secretAccount(host))
	if err != nil {
		return false, err
	}
	if raw == "" {
		return false, nil
	}
	var secrets SessionSecrets
	if err := json.Unmarshal([]byte(raw), &secrets); err != nil {
		return false, fmt.Errorf("session secrets in %s are not valid JSON: %w", s.SecretStore, err)
	}
	s.SessionSecrets = secrets
	return true, nil
}

func InspectSession() SessionInspectResult {
	path := SessionFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SessionInspectResult{Status: StatusMissing, Path: path}
		}
		return SessionInspectResult{
			Status:  StatusUnreadable,
			Path:    path,
			Code:    "session_unreadable",
			Message: err.Error(),
		}
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return SessionInspectResult{
			Status:  StatusInvalid,
			Path:    path,
			Code:    "session_invalid",
			Message: "Session file contains invalid JSON",
		}
	}
	switch found, err := loadExternalSecrets(&session, sessionHostForPath(path)); {
	case err != nil:
		return SessionInspectResult{
			Status:  StatusUnreadable,
			Path:    path,
			Code:    "session_secrets_unreadable",
			Message: err.Error(),
		}
	case !found:
		return SessionInspectResult{
			Status:  StatusInvalid,
			Path:    path,
			Code:    "session_secrets_missing",
			Message: fmt.Sprintf("Session credentials are no longer in %s; sign in again with 'orq auth login'", session.SecretStore),
		}
	}
	if err := validateSession(&session); err != nil {
		return SessionInspectResult{
			Status:  StatusInvalid,
			Path:    path,
			Code:    "session_invalid",
			Message: fmt.Sprintf("Session file is malformed: %s", err.Error()),
		}
	}
	return SessionInspectResult{Status: StatusOK, Path: path, Session: &session}
}

func ReadSession() (*Session, error) {
	r := InspectSession()
	switch r.Status {
	case StatusOK:
		return r.Session, nil
	case StatusMissing:
		return nil, nil
	default:
		return nil, fmt.Errorf("%s: %s", r.Code, r.Message)
	}
}

// pruneExpiredWorkspaceTokens drops cache entries whose token has actually
// expired, so a user cycling through (workspace, project) pairs — each one its
// own WorkspaceTokens entry, see Client.tokenKey — doesn't accumulate dead
// JWTs in the session file forever.
//
// Zero skew, not the 60s skew the read paths use: EnsureWorkspaceToken and
// WorkspaceToken already re-exchange anything expiring within that window, so
// nothing a concurrent invocation could still treat as usable is ever evicted
// here — only entries no reader would accept regardless.
//
// An absent or unparseable ExpiresAt is left alone. isExpired answers "yes" on
// a parse error, which is the right default for a read path about to use the
// token, but hygiene must not be the thing that destroys an entry a caller was
// about to judge for itself — including entries written by a CLI older than
// this field.
//
// Returns a new map rather than deleting in place: SaveSession's caller keeps
// using its *Session afterwards, and entries vanishing from underneath it is a
// change it never asked for.
func pruneExpiredWorkspaceTokens(tokens map[string]StoredAccessToken) map[string]StoredAccessToken {
	if tokens == nil {
		return nil
	}
	kept := make(map[string]StoredAccessToken, len(tokens))
	for key, tok := range tokens {
		if _, err := parseISO(tok.ExpiresAt); err != nil {
			kept[key] = tok
			continue
		}
		if isExpired(tok.ExpiresAt, 0) {
			continue
		}
		kept[key] = tok
	}
	return kept
}

// SaveSession writes the session atomically: temp file in the same directory,
// then rename. A concurrent reader can never observe a torn/interleaved file,
// and two concurrent writers end with one intact winner (last writer wins on
// the token cache, costing at most one extra token exchange) instead of
// corrupted JSON from a shorter write racing a longer one.
func SaveSession(s *Session) error {
	return saveSessionTo(SessionFilePath(), s)
}

// saveSessionTo writes the metadata through WriteSecretFile — temp file in the
// same directory, 0600, then rename — so a session file gets the same atomicity
// and permissions as credentials.json, from one implementation.
//
// The secrets go to the store first, the file second. A crash between the two
// leaves fresh tokens under stale metadata, which the next EnsureWorkspaceToken
// repairs; the reverse order would leave a file claiming secrets that were
// never written, which is a broken login. Version and SecretStore are set here
// rather than by callers, because only this function knows where the secrets
// ended up.
func saveSessionTo(path string, s *Session) error {
	store, err := resolveStore()
	if err != nil {
		// Only reachable under ORQ_CREDENTIAL_STORE=keychain, which asked for
		// exactly this rather than a silent downgrade.
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Shallow copy so the pruned token map is this write's alone; every other
	// field is shared with the caller unchanged.
	written := *s
	written.WorkspaceTokens = pruneExpiredWorkspaceTokens(s.WorkspaceTokens)
	written.Version = sessionVersionInline
	written.SecretStore = ""

	if _, inline := store.(fileStore); !inline {
		blob, err := json.Marshal(written.SessionSecrets)
		if err != nil {
			return err
		}
		if err := store.Set(secretAccount(sessionHostForPath(path)), string(blob)); err != nil {
			if keychainRequired() {
				// The user demanded the secure store. Nothing is written: a
				// half-saved login is worse than a failed one, and this is the
				// hard error `=keychain` exists to produce.
				return fmt.Errorf("%s=keychain, but the session credentials could not be stored in %s: %w",
					CredentialStoreEnvVar, store.Name(), err)
			}
			// Degrade in the direction that keeps the user logged in: write the
			// secrets inline, as this CLI always did, and warn once. A later
			// save, once the keyring is unlocked or the daemon is up, moves
			// them back out on its own.
			warnFallbackOnce(err)
		} else {
			written.SessionSecrets = SessionSecrets{}
			written.Version = sessionVersionExternal
			written.SecretStore = store.Name()
		}
	}

	data, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecretFile(path, data)
}

// ClearSession removes the login: the store item first, best effort, then the
// file. A missing item is success, exactly as fs.ErrNotExist already is — and a
// store that cannot answer must not stop a logout, because the file is what
// every read path starts from.
func ClearSession() error {
	path := SessionFilePath()
	if store, err := resolveStore(); err == nil {
		_ = store.Delete(secretAccount(sessionHostForPath(path)))
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SavedAgentKey returns the credential agent configs are wired with, and the
// workspace it was minted for. A bartolo profile in force is authoritative: its
// key is returned with an unknowable workspace and the login session is ignored
// completely. With no profile in force, the gateway key belongs to the login
// session. It lives here because launch needs it too and launch cannot import
// commands.
func SavedAgentKey() (key, workspace string) {
	if bartolocli.ActiveProfileName() != "" {
		if bartolocli.Creds == nil {
			return "", ""
		}
		return strings.TrimSpace(bartolocli.GetProfile()["api_key"]), ""
	}
	if session, err := ReadSession(); err == nil && session != nil {
		return session.GatewayKey, session.GatewayWorkspace
	}
	return "", ""
}

// GatewayKeyExpiry reports when the session's gateway key expires. Not-ok means
// no expiry is recorded — a key minted before expiry existed — and callers must
// treat that as "unknown", never as "expired". Nil-safe, so a caller that has
// no session to read gets "unknown" too. One definition for setup, doctor and
// launch; launch cannot import commands.
func (s *Session) GatewayKeyExpiry() (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	at, err := parseISO(s.GatewayKeyExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// EnvKeyShadowsWorkspace is the one definition of "the exported key conflicts
// with the login": a key we did not mint has an unknowable workspace and
// always conflicts; the minted key conflicts only on a recorded mismatch.
// Either side unknown means no mismatch, so an unrecorded workspace never
// invalidates a working credential.
func EnvKeyShadowsWorkspace(envKey, savedKey, savedWS, activeWS string) bool {
	if envKey == "" || activeWS == "" {
		return false
	}
	if envKey != savedKey {
		return true
	}
	return savedWS != "" && savedWS != activeWS
}

// Session states reported by ListSessions. A login is never reported as
// "expired": the only date on disk belongs to the bootstrap token, which the
// client re-mints from the refresh token on any call that needs it, so a stale
// one means the next command does one extra round-trip — not that the login is
// dead. Only the server can end a login, by rejecting the refresh token.
const (
	SessionStatusOK           = "ok"
	SessionStatusNeedsRefresh = "needs-refresh"
	SessionStatusInvalid      = "invalid"
	SessionStatusUnreadable   = "unreadable"
)

// SessionListEntry is one login on disk, for `orq auth sessions`.
type SessionListEntry struct {
	Host      string `json:"host"`
	Server    string `json:"server"`
	User      string `json:"user,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status"`
	Active    bool   `json:"active"`
	Path      string `json:"path"`
}

// ListSessions reads every session in the sessions directory, newest layout
// only: the file name is the host (see sessionPathFor), so the listing needs
// no state beyond the directory itself.
//
// A file that will not decode is reported with its host and a status saying
// so, rather than dropped: a session too broken to read is exactly what someone
// runs this command to find, and a row of blank fields alone would be
// indistinguishable from a healthy login that has set no project.
// `.deprecated` files are skipped: they are what migrateSessionFiles parks a
// host collision's loser under, not a login anything will authenticate with.
func ListSessions() ([]SessionListEntry, error) {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []SessionListEntry{}, nil
		}
		return nil, err
	}

	activeHost := SessionHost(ResolveURLs("").APIBaseURL)

	sessions := make([]SessionListEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		host := strings.TrimSuffix(name, ".json")
		path := filepath.Join(sessionsDir(), name)
		row := SessionListEntry{Host: host, Active: host == activeHost, Path: path}

		data, err := os.ReadFile(path)
		if err != nil {
			row.Status = SessionStatusUnreadable
			sessions = append(sessions, row)
			continue
		}
		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			row.Status = SessionStatusInvalid
			sessions = append(sessions, row)
			continue
		}
		// The listing validates every row and reports needs-refresh from the
		// bootstrap token's expiry, both of which live in the store once the
		// secrets are externalized — so this walks the store once per session
		// file whose secrets are out of it. A row whose blob is gone is
		// reported invalid, which is what it is.
		switch found, err := loadExternalSecrets(&session, host); {
		case err != nil:
			row.Status = SessionStatusUnreadable
			sessions = append(sessions, row)
			continue
		case !found:
			row.Status = SessionStatusInvalid
			sessions = append(sessions, row)
			continue
		}
		// Keep listing validation consistent with InspectSession and doctor.
		if err := validateSession(&session); err != nil {
			row.Status = SessionStatusInvalid
			sessions = append(sessions, row)
			continue
		}

		row.Server = session.APIBaseURL
		if session.User != nil {
			row.User = session.User.Email
		}
		if session.ActiveWorkspaceKey != nil {
			row.Workspace = *session.ActiveWorkspaceKey
		}
		row.Project = session.ActiveProjectName
		// Match EnsureBootstrapToken's 60-second clock skew.
		row.Status = SessionStatusOK
		if isExpired(session.BootstrapToken.ExpiresAt, 60) {
			row.Status = SessionStatusNeedsRefresh
		}
		sessions = append(sessions, row)
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Host < sessions[j].Host })
	return sessions, nil
}
