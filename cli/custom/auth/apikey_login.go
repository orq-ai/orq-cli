package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// apiKeyLoginSuffix names the per-host file that stores an `orq auth login
// --api-key` credential. It sits beside the browser-session file in the same
// sessions directory, host-keyed the same way (SessionHost), so a key logged in
// against one server never authenticates a call to another.
//
// It is a SEPARATE file, not a field on Session, on purpose. A browser session
// carries a refresh token and a bootstrap token and dozens of readers treat a
// non-nil ReadSession() result as exactly that — one they can revoke, refresh,
// and mint workspace tokens from. An api-key login has none of those; folding
// it into Session would either fail validateSession or hand every one of those
// readers a session that looks live but cannot do any of the things they assume.
// Keeping it in its own file, read by its own narrow accessor, means the
// api-key credential can only ever be used the one way it is meant to be:
// injected as a bearer key when nothing more explicit is in force.
const apiKeyLoginSuffix = ".apikey.json"

// APIKeyLoginSource marks where an api-key login came from. Only "login" today
// — the credential `orq auth login --api-key` verified and stored — but the
// field exists so a reader can tell a CLI-stored login apart from anything a
// future writer adds, exactly as the gateway key records its own provenance.
const APIKeyLoginSource = "login"

// APIKeyLogin is an `orq auth login --api-key` credential on disk: the verified
// key, its provenance, and the identity the verification call returned, so
// `orq status` can name who the key belongs to without a second round-trip.
type APIKeyLogin struct {
	Version    int              `json:"version"`
	APIBaseURL string           `json:"apiBaseUrl"`
	Source     string           `json:"source"`
	APIKey     string           `json:"apiKey"`
	User       *SessionUser     `json:"user,omitempty"`
	Workspaces []map[string]any `json:"workspaces,omitempty"`
}

func apiKeyLoginPathFor(host string) string {
	return filepath.Join(sessionsDir(), host+apiKeyLoginSuffix)
}

// APIKeyLoginFilePath is the api-key login file for the server this invocation
// resolved, mirroring SessionFilePath for the browser session.
func APIKeyLoginFilePath() string {
	return apiKeyLoginPathFor(SessionHost(ResolveURLs("").APIBaseURL))
}

// SaveAPIKeyLogin writes the api-key login for the resolved host atomically and
// 0600, through the same WriteSecretFile path the session and credentials files
// use. It stamps Version and Source so a reader never has to infer them.
func SaveAPIKeyLogin(login *APIKeyLogin) error {
	if login == nil {
		return errors.New("no api-key login to save")
	}
	if strings.TrimSpace(login.APIKey) == "" {
		return errors.New("api-key login has no key")
	}
	written := *login
	written.Version = 1
	if written.Source == "" {
		written.Source = APIKeyLoginSource
	}
	if written.APIBaseURL == "" {
		written.APIBaseURL = ResolveURLs("").APIBaseURL
	}
	path := APIKeyLoginFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecretFile(path, data)
}

// ReadAPIKeyLogin returns the api-key login for the resolved host, or nil when
// there is none. A file that will not decode, one stamped with a version this
// build does not support, or one carrying no key, is reported as an error rather
// than silently treated as absent: a corrupt credential is exactly what a user
// debugging a failed auth needs surfaced. Callers on the auth-state paths
// (whoami/status, logout) surface that error; the PreRun injector warns and
// proceeds so a broken file cannot take down the commands that fix it.
func ReadAPIKeyLogin() (*APIKeyLogin, error) {
	data, err := os.ReadFile(APIKeyLoginFilePath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var login APIKeyLogin
	if err := json.Unmarshal(data, &login); err != nil {
		return nil, errors.New("api-key login file contains invalid JSON")
	}
	// Same version gate validateSession applies: a file stamped with a version
	// this build does not write is not one it can trust to read, so surface it
	// rather than authenticate off a shape that may have moved.
	if login.Version != 1 {
		return nil, fmt.Errorf("api-key login file has unsupported version %d", login.Version)
	}
	if strings.TrimSpace(login.APIKey) == "" {
		return nil, errors.New("api-key login file is missing its key")
	}
	return &login, nil
}

// ClearAPIKeyLogin removes the api-key login for the resolved host. A missing
// file is success: logout must not fail because there was nothing to clear.
func ClearAPIKeyLogin() error {
	err := os.Remove(APIKeyLoginFilePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
