package launch

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveCredentialsEnvKey(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"ORQ_API_KEY": "env-key"}[k]
	}
	creds, err := ResolveCredentials(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if creds.APIKey != "env-key" || creds.APIBaseURL != DefaultGatewayAPIBaseURL {
		t.Fatalf("got %+v", creds)
	}
}

// TestResolveCredentialsSession exercises the `orq auth login` path with a
// fake session file (HOME redirected) and a stub profile endpoint. Bootstrap
// and workspace tokens are unexpired so the only HTTP call is the profile
// fetch.
func TestResolveCredentialsSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer bootstrap-token" {
			t.Errorf("profile fetch auth header: %s", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"profile": map[string]any{
			"id":         "u1",
			"email":      "user@example.com",
			"workspaces": []map[string]any{{"key": "ws1"}},
		}})
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", "")
	t.Setenv("ORQ_PROFILE_BASE_URL", srv.URL)

	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	session := map[string]any{
		"version":            1,
		"apiBaseUrl":         srv.URL,
		"v1BaseUrl":          srv.URL,
		"authBaseUrl":        srv.URL,
		"profileBaseUrl":     srv.URL,
		"activeWorkspaceKey": "ws1",
		"refreshToken":       "refresh-token",
		"bootstrapToken":     map[string]any{"token": "bootstrap-token", "expiresAt": future},
		"workspaceTokens":    map[string]any{"ws1": map[string]any{"token": "workspace-token", "expiresAt": future}},
	}
	dir := filepath.Join(home, ".orq", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(session)
	if err := os.WriteFile(filepath.Join(dir, "default.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := ResolveCredentials(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if creds.APIKey != "workspace-token" {
		t.Fatalf("expected workspace token, got %+v", creds)
	}
	if creds.APIBaseURL != srv.URL {
		t.Fatalf("expected session api base, got %s", creds.APIBaseURL)
	}
}

func TestResolveCredentialsNotLoggedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORQ_API_KEY", "")
	if _, err := ResolveCredentials(os.Getenv); err == nil {
		t.Fatal("expected not-logged-in error")
	}
}

// A dead-end "not logged in" error on an interactive run should become an
// offer to log in; everything else must pass through untouched.
func TestResolveCredentialsOrLogin(t *testing.T) {
	origTTY, origHook := isTerminalFd, LoginHook
	t.Cleanup(func() { isTerminalFd, LoginHook = origTTY, origHook })
	isTerminalFd = func(int) bool { return true }

	emptyEnv := func(string) string { return "" }

	t.Run("hook absent keeps the plain error", func(t *testing.T) {
		LoginHook = nil
		home := t.TempDir()
		t.Setenv("HOME", home)
		if _, err := resolveCredentialsOrLogin(emptyEnv, true); err == nil {
			t.Fatal("expected not-logged-in error")
		}
	})

	t.Run("prompt suppressed when not allowed", func(t *testing.T) {
		called := false
		LoginHook = func() error { called = true; return nil }
		home := t.TempDir()
		t.Setenv("HOME", home)
		if _, err := resolveCredentialsOrLogin(emptyEnv, false); err == nil {
			t.Fatal("expected not-logged-in error")
		}
		if called {
			t.Error("login hook ran without permission to prompt")
		}
	})

	t.Run("env key short-circuits before any prompt", func(t *testing.T) {
		LoginHook = func() error { t.Fatal("hook must not run"); return nil }
		creds, err := resolveCredentialsOrLogin(func(k string) string {
			if k == "ORQ_API_KEY" {
				return "sk-orq-test"
			}
			return ""
		}, true)
		if err != nil || creds.APIKey != "sk-orq-test" {
			t.Fatalf("got (%v, %v)", creds, err)
		}
	})

	t.Run("non-interactive env suppresses the prompt", func(t *testing.T) {
		LoginHook = func() error { t.Fatal("hook must not run"); return nil }
		home := t.TempDir()
		t.Setenv("HOME", home)
		env := func(k string) string {
			if k == "ORQ_LAUNCH_NON_INTERACTIVE" {
				return "1"
			}
			return ""
		}
		if _, err := resolveCredentialsOrLogin(env, true); err == nil {
			t.Fatal("expected not-logged-in error")
		}
	})
}

// The gateway_key split made "no api_key configured" the ordinary state, so the
// PreRun now injects the session's own workspace token into ORQ_API_KEY on
// nearly every run. Reading that as a user-supplied key made Kind
// CredentialAPIKey, which is what ShadowsSession and every other
// session-vs-key decision keys on.
func TestInjectedSessionTokenIsRecognisedAsTheSession(t *testing.T) {
	const active = "acme"
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"scope":["openid"]}`))
	stale := "header." + payload + ".sig"

	home := t.TempDir()
	t.Setenv("HOME", home)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	writeSessionFile(t, home, map[string]any{
		"version": 1, "activeWorkspaceKey": active,
		"apiBaseUrl": "https://api.orq.ai", "v1BaseUrl": "https://api.orq.ai",
		"authBaseUrl": "https://api.orq.ai", "profileBaseUrl": "https://api.orq.ai/me",
		"refreshToken":    "rt",
		"bootstrapToken":  map[string]any{"token": "bt", "expiresAt": future},
		"workspaceTokens": map[string]any{active: map[string]any{"token": stale, "expiresAt": future}},
	})

	creds, err := ResolveCredentials(func(k string) string {
		if k == "ORQ_API_KEY" {
			return stale // as installSessionPreRun leaves it
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Kind != CredentialSessionToken {
		t.Error("our own injected session token was treated as a user-supplied key")
	}

	// A key the user really did export still counts as a real key.
	creds, err = ResolveCredentials(func(k string) string {
		if k == "ORQ_API_KEY" {
			return "sk-orq-REAL"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Kind != CredentialAPIKey {
		t.Errorf("a real API key must not be read as the session's own token: %+v", creds)
	}
}

// writeSessionFile drops a session at the path auth.ReadSession looks in.
func writeSessionFile(t *testing.T, home string, session map[string]any) {
	t.Helper()
	dir := filepath.Join(home, ".orq", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "default.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
