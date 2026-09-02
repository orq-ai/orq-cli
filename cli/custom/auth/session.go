package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

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
	Version            int                          `json:"version"`
	APIBaseURL         string                       `json:"apiBaseUrl"`
	V1BaseURL          string                       `json:"v1BaseUrl"`
	AuthBaseURL        string                       `json:"authBaseUrl"`
	ProfileBaseURL     string                       `json:"profileBaseUrl"`
	User               *SessionUser                 `json:"user"`
	Workspaces         []map[string]any             `json:"workspaces"`
	ActiveWorkspaceKey *string                      `json:"activeWorkspaceKey"`
	ActiveProjectID    string                       `json:"activeProjectId,omitempty"`
	ActiveProjectName  string                       `json:"activeProjectName,omitempty"`
	RefreshToken       string                       `json:"refreshToken"`
	BootstrapToken     StoredAccessToken            `json:"bootstrapToken"`
	WorkspaceTokens    map[string]StoredAccessToken `json:"workspaceTokens"`

	// The gateway key `orq setup` minted from this login for coding agents,
	// its id (the handle for revoking it), its expiry, and the workspace it
	// was minted for. Not a credential for the platform API, so never a
	// bartolo profile.
	GatewayKey          string `json:"gatewayKey,omitempty"`
	GatewayKeyID        string `json:"gatewayKeyId,omitempty"`
	GatewayKeyExpiresAt string `json:"gatewayKeyExpiresAt,omitempty"`
	GatewayWorkspace    string `json:"gatewayWorkspace,omitempty"`
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

// SessionFilePath is the session for the server this invocation resolved
// (custom.resolveServer → SetServer), so `--server https://my.staging.orq.ai`
// reads the staging login and a bare `orq` reads the hosted one.
func SessionFilePath() string {
	return sessionPathFor(SessionHost(ResolveURLs("").APIBaseURL))
}

// sessionFilesDir is an unexported alias of sessionsDir for callers within
// this package that want the directory without going through SessionFilePath.
func sessionFilesDir() string {
	return sessionsDir()
}

func legacySessionFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, sessionDirName, legacyFileName)
}

// SessionsDir exposes the directory backing every profile's session file, so
// a caller outside this package (doctor's permission check) can enumerate it
// without reverse-engineering the layout from SessionFilePath.
func SessionsDir() string {
	return sessionsDir()
}

// LegacySessionFilePath exposes the pre-multi-profile `~/.orq/session.json`
// path. It normally disappears into the per-profile layout the first time
// InspectSession runs, but a caller auditing credentials on disk should not
// have to assume that migration already ran.
func LegacySessionFilePath() string {
	return legacySessionFilePath()
}

func ensureSessionDir() error {
	return os.MkdirAll(filepath.Dir(SessionFilePath()), 0o700)
}

func validateSession(s *Session) error {
	if s.Version != 1 {
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
	if err := ensureSessionDir(); err != nil {
		return err
	}
	// Shallow copy so the pruned token map is this write's alone; every other
	// field is shared with the caller unchanged.
	written := *s
	written.WorkspaceTokens = pruneExpiredWorkspaceTokens(s.WorkspaceTokens)
	data, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return err
	}
	path := SessionFilePath()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ClearSession() error {
	err := os.Remove(SessionFilePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SavedAgentKey returns the credential agent configs are wired with, and the
// workspace it was minted for: the gateway key on the login session, else the
// API key of the bartolo profile in force (a key the user brought, whose
// workspace is unknowable). It lives here because launch needs it too and
// launch cannot import commands.
func SavedAgentKey() (key, workspace string) {
	if session, err := ReadSession(); err == nil && session != nil && session.GatewayKey != "" {
		return session.GatewayKey, session.GatewayWorkspace
	}
	if bartolocli.Creds == nil {
		return "", ""
	}
	return strings.TrimSpace(bartolocli.GetProfile()["api_key"]), ""
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
