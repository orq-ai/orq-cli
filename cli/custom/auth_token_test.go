package custom

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"orq/cli/custom/auth"
)

// `orq auth token` through the shipped binary, so the root PreRun that
// resolves --workspace and mints the session token runs exactly as it does
// for a user. The command must report the slot that PreRun chose.
func TestAuthTokenReportsTheSlotThePreRunResolved(t *testing.T) {
	bin := buildOrqBinary(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	var mu sync.Mutex
	var exchanged []string // workspace_key of each exchange, in order
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.ProfileRPCPath:
			fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","workspaces":[{"key":"acme"},{"key":"other"}]}}`)
		case "/v2/auth/access-token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			exchanged = append(exchanged, body["workspace_key"])
			n := len(exchanged)
			mu.Unlock()
			claims, _ := json.Marshal(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()})
			fmt.Fprintf(w, `{"access_token":"h.%s.%s-%d"}`, base64.RawURLEncoding.EncodeToString(claims), body["workspace_key"], n)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldServer, oldSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(oldServer, oldSource) })
	ws := "acme"
	if err := auth.SaveSession(&auth.Session{
		Version: 1, APIBaseURL: srv.URL, V1BaseURL: srv.URL,
		AuthBaseURL: srv.URL + "/v2/auth", ProfileBaseURL: srv.URL,
		RefreshToken: "refresh", ActiveWorkspaceKey: &ws,
		BootstrapToken: auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: "2099-01-01T00:00:00Z"},
		WorkspaceTokens: map[string]auth.StoredAccessToken{
			"acme": {Token: "tok-acme", ExpiresAt: "2099-01-01T00:00:00Z"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	run := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "HOME="+home, "ORQ_SERVER="+srv.URL, "ORQ_NO_UPDATE_CHECK=1",
			"ORQ_API_KEY=", "ORQ_TOKEN=", "ORQ_AUTHORIZATION=", "ORQ_PROFILE=", "ORQ_WORKSPACE=", "ORQ_PROJECT=")
		cmd.Env = append(cmd.Env, env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("orq %v: %v\n%s", args, err, stderr.String())
		}
		return string(out)
	}
	report := func(out string) map[string]string {
		t.Helper()
		var r map[string]string
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		return r
	}

	// A standing JSON default must not wrap the token $(orq auth token) captures.
	if out := run([]string{"ORQ_OUTPUT_FORMAT=json"}, "auth", "token"); out != "tok-acme\n" {
		t.Errorf("bare output under ORQ_OUTPUT_FORMAT=json = %q, want the cached active token alone", out)
	}

	r := report(run(nil, "--workspace", "other", "auth", "token", "-o", "json"))
	if r["workspace"] != "other" || !strings.HasSuffix(r["token"], ".other-1") || r["server"] != srv.URL {
		t.Errorf("--workspace other reported %v, want the token the PreRun exchanged for other", r)
	}

	// --refresh exchanges even though acme's token is cached and unexpired, and
	// records the replaced one as stale so a process still sending it moves on.
	r = report(run(nil, "auth", "token", "--refresh", "-o", "json"))
	if r["workspace"] != "acme" || !strings.HasSuffix(r["token"], ".acme-2") {
		t.Errorf("--refresh reported %v, want a fresh acme token", r)
	}
	if got := auth.CurrentToken("tok-acme"); got != r["token"] {
		t.Errorf("CurrentToken(old) = %q, want the refreshed token", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(exchanged, ",") != "other,acme" {
		t.Errorf("exchanges = %v, want one for --workspace other and one for --refresh", exchanged)
	}
}
