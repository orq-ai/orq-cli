package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

const (
	sessionDirName     = ".orq"
	sessionsSubdirName = "sessions"
	legacyFileName     = "session.json"
	defaultProfile     = "default"
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

// ActiveProfile returns the profile name the user passed via --profile (or the
// ORQ_PROFILE env var bartolo wires up via viper). Defaults to "default".
func ActiveProfile() string {
	name := viper.GetString("profile")
	if name == "" {
		return defaultProfile
	}
	return name
}

func sessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, sessionDirName, sessionsSubdirName)
}

// SessionFilePath returns the per-profile session file path. Each profile
// stores its own credentials at ~/.orq/sessions/<profile>.json so that
// `orq --profile acme` and `orq --profile default` don't share state.
func SessionFilePath() string {
	return filepath.Join(sessionsDir(), ActiveProfile()+".json")
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
// InspectSession runs (see migrateLegacySession), but a caller auditing
// credentials on disk should not have to assume that migration already ran.
func LegacySessionFilePath() string {
	return legacySessionFilePath()
}

// migrateLegacySession moves a pre-multi-profile ~/.orq/session.json into the
// per-profile layout under ~/.orq/sessions/default.json the first time we see
// one, so existing logged-in users aren't logged out by the upgrade.
func migrateLegacySession() {
	legacy := legacySessionFilePath()
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	target := filepath.Join(sessionsDir(), defaultProfile+".json")
	if _, err := os.Stat(target); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return
	}
	_ = os.Rename(legacy, target)
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
	migrateLegacySession()
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

// SaveSession writes the session atomically: temp file in the same directory,
// then rename. A concurrent reader can never observe a torn/interleaved file,
// and two concurrent writers end with one intact winner (last writer wins on
// the token cache, costing at most one extra token exchange) instead of
// corrupted JSON from a shorter write racing a longer one.
func SaveSession(s *Session) error {
	if err := ensureSessionDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
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
// workspace it was minted for. gateway_key is what `orq setup` writes now;
// api_key is the fallback for keys minted before the split and for keys the
// user brought themselves.
//
// It lives here rather than in commands because launch needs it too, and
// launch cannot import commands. Three copies of this lookup drifted apart
// once already: the split moved the key and one caller kept reading api_key,
// which made every `orq launch` warn about a workspace mismatch that was not
// there.
func SavedAgentKey() (key, workspace string) {
	if bartolocli.Creds == nil {
		return "", ""
	}
	profile := ActiveProfile()
	workspace = StateValueOf(profile, "workspace")
	if key = StateValueOf(profile, "gateway_key"); key != "" {
		return key, workspace
	}
	return strings.TrimSpace(bartolocli.Creds.GetString("profiles." + profile + ".api_key")), workspace
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
