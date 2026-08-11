package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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

func SaveSession(s *Session) error {
	if err := ensureSessionDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(SessionFilePath(), data, 0o600); err != nil {
		return err
	}
	return os.Chmod(SessionFilePath(), 0o600)
}

func ClearSession() error {
	err := os.Remove(SessionFilePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
