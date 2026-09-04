package launch

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"

	"orq/cli/custom/auth"
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
	writeSessionFile(t, home, session)

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

// TestResolveCredentialsSupersession covers ResolveCredentials' fall-through
// decision for an exported ORQ_API_KEY that matches the key we minted: it
// must lose only when the session has since moved to a different workspace.
func TestResolveCredentialsSupersession(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)

	for _, tc := range []struct {
		name     string
		savedWS  string
		activeWS string
		// savedKey is the key recorded on the session as minted; empty means
		// "same as the exported ORQ_API_KEY" (sk-minted), the ordinary case.
		savedKey string
		// envKey overrides the exported ORQ_API_KEY; empty means "sk-minted",
		// the ordinary case.
		envKey string
		// extraTokens caches workspace tokens beyond the active one.
		extraTokens map[string]string
		// newServer builds the profile-fetch stub this case needs, or nil when
		// the case never reaches the network (the same-workspace short-circuit
		// returns before ResolveCredentials reads the session at all).
		newServer      func(t *testing.T) *httptest.Server
		wantKey        string
		wantKind       CredentialKind
		wantWorkspace  string
		wantSuperseded string
		wantShadows    bool
	}{
		{
			// exported key == saved key, saved workspace != active: the
			// session's token for the active workspace wins.
			name:     "superseded workspace",
			savedWS:  "acme",
			activeWS: "other",
			newServer: func(t *testing.T) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{"profile": map[string]any{
						"id":         "u1",
						"email":      "user@example.com",
						"workspaces": []map[string]any{{"key": "other"}},
					}})
				}))
			},
			wantKey:        "session-token-for-other",
			wantKind:       CredentialSessionToken,
			wantWorkspace:  "other",
			wantSuperseded: "acme",
		},
		{
			// exported key == saved key, saved workspace == active: the
			// env key wins outright, nothing superseded.
			name:     "saved key matches active workspace",
			savedWS:  "acme",
			activeWS: "acme",
			// The env key must win before ResolveCredentials ever dials out;
			// if the short-circuit regresses, failing the request here beats
			// silently dialing the real production API.
			newServer: func(t *testing.T) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("unexpected request to %s; env key should have short-circuited", r.URL.Path)
				}))
			},
			wantKey:       "sk-minted",
			wantKind:      CredentialAPIKey,
			wantWorkspace: "acme",
		},
		{
			// exported key != saved key: a key we did not mint has an
			// unknowable workspace, so it wins outright and shadows the
			// session rather than getting superseded.
			name:          "exported key not the one we minted",
			savedKey:      "sk-different",
			savedWS:       "acme",
			activeWS:      "acme",
			wantKey:       "sk-minted",
			wantKind:      CredentialAPIKey,
			wantWorkspace: "",
			wantShadows:   true,
		},
		{
			// exported key == saved key, but the saved workspace is empty:
			// provenance is unknown, so the env key wins outright and
			// nothing is superseded.
			name:     "saved key matches but saved workspace is empty",
			savedWS:  "",
			activeWS: "acme",
			newServer: func(t *testing.T) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("unexpected request to %s; env key should have short-circuited", r.URL.Path)
				}))
			},
			wantKey:       "sk-minted",
			wantKind:      CredentialAPIKey,
			wantWorkspace: "",
		},
		{
			// the exported key is one of the session's own cached workspace
			// tokens (installSessionPreRun's doing, not a user export): it
			// wins outright as the session, not as a superseded key.
			name:     "exported key is the session's own cached token",
			envKey:   "session-token-for-acme",
			savedWS:  "beta",
			activeWS: "acme",
			newServer: func(t *testing.T) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("unexpected request to %s; the session's own token should have short-circuited", r.URL.Path)
				}))
			},
			wantKey:       "session-token-for-acme",
			wantKind:      CredentialSessionToken,
			wantWorkspace: "",
		},
		{
			// A token cached for a workspace the session has left still wins the
			// run, but must be reported as an exported key that shadows the
			// session rather than as the session's own.
			name:        "exported key is a cached token for a workspace the session left",
			envKey:      "session-token-for-alpha",
			extraTokens: map[string]string{"alpha": "session-token-for-alpha"},
			savedWS:     "acme",
			activeWS:    "other",
			newServer: func(t *testing.T) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("unexpected request to %s; the exported key should have short-circuited", r.URL.Path)
				}))
			},
			wantKey:       "session-token-for-alpha",
			wantKind:      CredentialAPIKey,
			wantWorkspace: "",
			wantShadows:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiBase := "https://api.orq.ai"
			if tc.newServer != nil {
				srv := tc.newServer(t)
				defer srv.Close()
				apiBase = srv.URL
				t.Setenv("ORQ_PROFILE_BASE_URL", srv.URL)
			}

			envKey := tc.envKey
			if envKey == "" {
				envKey = "sk-minted"
			}

			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ORQ_API_KEY", envKey)
			t.Setenv("ORQ_API_BASE_URL", "")

			savedKey := tc.savedKey
			if savedKey == "" {
				savedKey = "sk-minted"
			}
			tokens := map[string]any{tc.activeWS: map[string]any{"token": "session-token-for-" + tc.activeWS, "expiresAt": future}}
			for ws, tok := range tc.extraTokens {
				tokens[ws] = map[string]any{"token": tok, "expiresAt": future}
			}
			writeSessionFile(t, home, map[string]any{
				"version":            1,
				"apiBaseUrl":         apiBase,
				"v1BaseUrl":          apiBase,
				"authBaseUrl":        apiBase,
				"profileBaseUrl":     apiBase,
				"activeWorkspaceKey": tc.activeWS,
				"refreshToken":       "refresh-token",
				"bootstrapToken":     map[string]any{"token": "bootstrap-token", "expiresAt": future},
				"workspaceTokens":    tokens,
				"gatewayKey":         savedKey,
				"gatewayWorkspace":   tc.savedWS,
			})

			creds, err := ResolveCredentials(os.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			if creds.APIKey != tc.wantKey {
				t.Errorf("APIKey = %q, want %q", creds.APIKey, tc.wantKey)
			}
			if creds.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", creds.Kind, tc.wantKind)
			}
			if creds.Workspace != tc.wantWorkspace {
				t.Errorf("Workspace = %q, want %q", creds.Workspace, tc.wantWorkspace)
			}
			if creds.SupersededWorkspace != tc.wantSuperseded {
				t.Errorf("SupersededWorkspace = %q, want %q", creds.SupersededWorkspace, tc.wantSuperseded)
			}
			if creds.ShadowsSession != tc.wantShadows {
				t.Errorf("ShadowsSession = %v, want %v", creds.ShadowsSession, tc.wantShadows)
			}
			if tc.wantSuperseded != "" && creds.ShadowsSession {
				t.Error("a superseded key must not also report ShadowsSession")
			}
		})
	}
}

// A failed token fetch names the way back only on the superseded path, where a
// working exported key was set aside. Elsewhere the error passes through.
func TestResolveCredentialsTokenFetchFailure(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	seed := func(t *testing.T, withEnvKey bool) {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("ORQ_PROFILE_BASE_URL", srv.URL)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ORQ_API_BASE_URL", "")
		if withEnvKey {
			t.Setenv("ORQ_API_KEY", "sk-minted")
		} else {
			t.Setenv("ORQ_API_KEY", "")
		}
		gatewayKey, gatewayWorkspace := "", ""
		if withEnvKey {
			gatewayKey, gatewayWorkspace = "sk-minted", "acme"
		}
		writeSessionFile(t, home, map[string]any{
			"version":            1,
			"apiBaseUrl":         srv.URL,
			"v1BaseUrl":          srv.URL,
			"authBaseUrl":        srv.URL,
			"profileBaseUrl":     srv.URL,
			"activeWorkspaceKey": "other",
			"refreshToken":       "refresh-token",
			"bootstrapToken":     map[string]any{"token": "bootstrap-token", "expiresAt": future},
			"gatewayKey":         gatewayKey,
			"gatewayWorkspace":   gatewayWorkspace,
		})
	}

	t.Run("superseded path names the way back", func(t *testing.T) {
		seed(t, true)
		_, err := ResolveCredentials(os.Getenv)
		if err == nil {
			t.Fatal("expected the failed token fetch to surface")
		}
		if !strings.Contains(err.Error(), "orq auth login") {
			t.Errorf("superseded error does not name the remedy: %q", err)
		}
	})

	t.Run("plain session path passes the error through", func(t *testing.T) {
		seed(t, false)
		_, err := ResolveCredentials(os.Getenv)
		if err == nil {
			t.Fatal("expected the failed token fetch to surface")
		}
		if strings.Contains(err.Error(), "re-authenticate") {
			t.Errorf("plain session-path error carries re-login advice it cannot stand behind: %q", err)
		}
	})
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
	resolved := auth.ResolveURLs("").APIBaseURL
	if err := os.WriteFile(filepath.Join(dir, auth.SessionHost(resolved)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// RemedyForWorkspace is the one place both the launch supersession note and
// the doctor/connect-status "pinned elsewhere" row get their fix-it command
// from, so it must never recommend 'orq connect' in the exact state
// resolveConnectAuth (cli/custom/commands/connect.go) hard-errors on: a saved
// key whose recorded workspace differs from the active one.
func TestRemedyForWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		savedWS string
		active  string
		want    string
	}{
		{
			name:    "saved key belongs to a different workspace",
			id:      "kimi",
			savedWS: "acme",
			active:  "beta",
			want:    "orq setup --workspace beta",
		},
		{
			name:    "saved key already belongs to the active workspace",
			id:      "kimi",
			savedWS: "acme",
			active:  "acme",
			want:    "orq connect kimi",
		},
		{
			name:    "no saved key at all",
			id:      "codex",
			savedWS: "",
			active:  "acme",
			want:    "orq connect codex",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemedyForWorkspace(tc.id, tc.savedWS, tc.active); got != tc.want {
				t.Errorf("RemedyForWorkspace(%q, %q, %q) = %q, want %q", tc.id, tc.savedWS, tc.active, got, tc.want)
			}
		})
	}
}

// The token cache is keyed on (workspace, project), so once a project is
// selected the session's own token is filed under "<workspace>#<project>".
// Looking it up under the bare workspace key found nothing, which classified
// the token the PreRun had just injected as a foreign API key and brought back
// the spurious "ORQ_API_KEY may not belong to the workspace" note.
func TestInjectedSessionTokenIsRecognisedWhenAProjectIsActive(t *testing.T) {
	const active = "acme"
	const project = "01JPROJECT"
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"scope":["openid"]}`))
	scoped := "header." + payload + ".sig"

	home := t.TempDir()
	t.Setenv("HOME", home)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	writeSessionFile(t, home, map[string]any{
		"version": 1, "activeWorkspaceKey": active, "activeProjectId": project,
		"apiBaseUrl": "https://api.orq.ai", "v1BaseUrl": "https://api.orq.ai",
		"authBaseUrl": "https://api.orq.ai", "profileBaseUrl": "https://api.orq.ai/me",
		"refreshToken":   "rt",
		"bootstrapToken": map[string]any{"token": "bt", "expiresAt": future},
		"workspaceTokens": map[string]any{
			active + "#" + project: map[string]any{"token": scoped, "expiresAt": future},
		},
	})

	creds, err := ResolveCredentials(func(k string) string {
		if k == "ORQ_API_KEY" {
			return scoped // as installSessionPreRun leaves it
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Kind != CredentialSessionToken {
		t.Error("a project-scoped session token was treated as a user-supplied key")
	}
	if creds.ShadowsSession {
		t.Error("our own project-scoped session token was reported as shadowing the session")
	}
}

// TestResolveCredentialsPrefersSavedGatewayKey covers the fallback between the
// env key and the session token: the key `orq setup` minted lives 90 days, the
// session token about an hour, and launch bakes whichever it picks into the
// child once. The minted key wins only for the workspace it was minted for and
// only while it is unexpired.
func TestResolveCredentialsPrefersSavedGatewayKey(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	// Only the session-token fallback talks to the server; the minted-key path
	// never touches the network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"profile": map[string]any{
			"id": "u1", "email": "user@example.com", "workspaces": []map[string]any{{"key": "ws1"}},
		}})
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		savedWS string
		expires string
		// injected mirrors a real run: installSessionPreRun has already put the
		// session token into ORQ_API_KEY before launch resolves credentials.
		injected bool
		wantKey  string
	}{
		{name: "same workspace", savedWS: "ws1", wantKey: "minted-key"},
		{name: "same workspace, token already injected", savedWS: "ws1", injected: true, wantKey: "minted-key"},
		{name: "no key, token already injected", savedWS: "", injected: true, wantKey: "workspace-token"},
		{name: "unexpired", savedWS: "ws1", expires: future, wantKey: "minted-key"},
		{name: "expired", savedWS: "ws1", expires: past, wantKey: "workspace-token"},
		{name: "other workspace", savedWS: "ws2", wantKey: "workspace-token"},
		{name: "no key", savedWS: "", wantKey: "workspace-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ORQ_API_KEY", "")
			t.Setenv("ORQ_API_BASE_URL", "")
			t.Setenv("ORQ_PROFILE_BASE_URL", srv.URL)
			key := "minted-key"
			if tc.savedWS == "" {
				key = ""
			}
			writeSessionFile(t, home, map[string]any{
				"version":             1,
				"apiBaseUrl":          srv.URL,
				"v1BaseUrl":           srv.URL,
				"authBaseUrl":         srv.URL,
				"profileBaseUrl":      srv.URL,
				"activeWorkspaceKey":  "ws1",
				"refreshToken":        "refresh-token",
				"bootstrapToken":      map[string]any{"token": "bootstrap-token", "expiresAt": future},
				"workspaceTokens":     map[string]any{"ws1": map[string]any{"token": "workspace-token", "expiresAt": future}},
				"gatewayKey":          key,
				"gatewayWorkspace":    tc.savedWS,
				"gatewayKeyExpiresAt": tc.expires,
			})
			if tc.injected {
				t.Setenv("ORQ_API_KEY", "workspace-token")
			}

			creds, err := ResolveCredentials(os.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			if creds.APIKey != tc.wantKey {
				t.Fatalf("want %q, got %+v", tc.wantKey, creds)
			}
			wantKind := CredentialSessionToken
			if tc.wantKey == "minted-key" {
				wantKind = CredentialAPIKey
			}
			if creds.Kind != wantKind {
				t.Fatalf("want kind %v, got %v", wantKind, creds.Kind)
			}
		})
	}
}

// useProfile puts an API-key profile in force the way --profile does, with the
// given key (empty for a keyless profile).
func useProfile(t *testing.T, name, key string) {
	t.Helper()
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds.Set("profiles."+name+".type", "apikey")
	creds.Set("profiles."+name+".api_key", key)
	prev := bartolocli.Creds
	bartolocli.Creds = creds
	viper.Set("profile", name)
	t.Cleanup(func() { bartolocli.Creds = prev; viper.Set("profile", "") })
}

// TestResolveCredentialsProfileInForce: a selected API-key profile is the
// credential. No session is read — there is none on disk here and no server
// to reach — and a keyless profile is an error, never a fall-through to the
// session or a browser login.
func TestResolveCredentialsProfileInForce(t *testing.T) {
	t.Run("profile key wins without a session", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ORQ_API_KEY", "sk-from-shell")
		useProfile(t, "acme", "sk-acme")

		creds, err := ResolveCredentials(os.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		if creds.APIKey != "sk-acme" || creds.Kind != CredentialAPIKey {
			t.Fatalf("profile key did not win: %+v", creds)
		}
		if creds.ShadowsSession || creds.SupersededWorkspace != "" {
			t.Fatalf("a profile key has no session to shadow: %+v", creds)
		}
	})

	t.Run("keyless profile errors and never reaches the login hook", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ORQ_API_KEY", "")
		useProfile(t, "acme", "")
		prev := LoginHook
		LoginHook = func() error { t.Fatal("LoginHook must not run with a profile in force"); return nil }
		t.Cleanup(func() { LoginHook = prev })

		_, err := resolveCredentialsOrLogin(os.Getenv, true)
		if err == nil || !strings.Contains(err.Error(), `"acme"`) {
			t.Fatalf("want an error naming the profile, got %v", err)
		}
	})
}
