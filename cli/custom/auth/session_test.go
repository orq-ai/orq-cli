package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// validSession returns a minimal session that passes validateSession, with the
// active workspace set to activeKey.
func validSession(activeKey string) *Session {
	return &Session{
		Version:            1,
		APIBaseURL:         "https://api.example",
		V1BaseURL:          "https://api.example/v1",
		AuthBaseURL:        "https://auth.example",
		ProfileBaseURL:     "https://profile.example",
		RefreshToken:       "refresh-abc",
		BootstrapToken:     StoredAccessToken{Token: "boot", ExpiresAt: "2099-01-01T00:00:00Z"},
		ActiveWorkspaceKey: &activeKey,
		WorkspaceTokens:    map[string]StoredAccessToken{},
	}
}

// isolateHome points the session dir at a temp HOME so tests never touch the
// real ~/.orq. os.UserHomeDir (which sessionsDir uses) honors $HOME on unix.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestSaveSessionRoundTripPermsAndNoTemp(t *testing.T) {
	isolateHome(t)
	in := validSession("prod")
	in.WorkspaceTokens["prod"] = StoredAccessToken{Token: "tok-prod", ExpiresAt: "2099-01-01T00:00:00Z"}

	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got == nil || got.RefreshToken != in.RefreshToken {
		t.Fatalf("round-trip lost the session (got %+v)", got)
	}
	if got.ActiveWorkspaceKey == nil || *got.ActiveWorkspaceKey != "prod" {
		t.Errorf("active workspace = %v, want prod", got.ActiveWorkspaceKey)
	}
	if got.WorkspaceTokens["prod"].Token != "tok-prod" {
		t.Errorf("workspace token lost in round-trip: %+v", got.WorkspaceTokens)
	}

	// The atomic write must leave 0600 and no leftover temp file.
	info, err := os.Stat(SessionFilePath())
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file perm = %o, want 600", perm)
	}
	entries, _ := os.ReadDir(filepath.Dir(SessionFilePath()))
	for _, e := range entries {
		if len(e.Name()) >= 8 && e.Name()[:8] == ".session" {
			t.Errorf("leftover temp file after atomic write: %s", e.Name())
		}
	}
}

func TestSaveSessionPrunesExpiredWorkspaceTokensOnly(t *testing.T) {
	isolateHome(t)
	in := validSession("prod")
	// Already expired: a dead JWT left over from a project the user is no
	// longer using.
	in.WorkspaceTokens["acme#proj-old"] = StoredAccessToken{Token: "tok-old", ExpiresAt: "2000-01-01T00:00:00Z"}
	// Still valid: must survive the prune.
	in.WorkspaceTokens["acme#proj-new"] = StoredAccessToken{Token: "tok-new", ExpiresAt: "2099-01-01T00:00:00Z"}

	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if _, present := got.WorkspaceTokens["acme#proj-old"]; present {
		t.Errorf("expired workspace token was not pruned: %+v", got.WorkspaceTokens)
	}
	if got.WorkspaceTokens["acme#proj-new"].Token != "tok-new" {
		t.Errorf("valid workspace token was pruned or lost: %+v", got.WorkspaceTokens)
	}
}

func TestMergeWorkspaceTokenPreservesConcurrentActiveChange(t *testing.T) {
	isolateHome(t)
	// Seed the on-disk session with active = prod.
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate a concurrent `workspace use dev`: the on-disk session's active
	// workspace changes to dev after our (hypothetical) caller read it as prod.
	if err := SaveSession(validSession("dev")); err != nil {
		t.Fatalf("concurrent write: %v", err)
	}

	// Now cache a token for a per-invocation --workspace=staging override. This
	// must merge onto the CURRENT on-disk session, not overwrite it with a stale
	// prod snapshot.
	tok := StoredAccessToken{Token: "tok-staging", ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := mergeWorkspaceToken("staging", tok); err != nil {
		t.Fatalf("mergeWorkspaceToken: %v", err)
	}

	got, err := ReadSession()
	if err != nil || got == nil {
		t.Fatalf("read after merge: %v", err)
	}
	// The concurrent active-workspace change must survive.
	if got.ActiveWorkspaceKey == nil || *got.ActiveWorkspaceKey != "dev" {
		t.Errorf("active workspace = %v, want dev (concurrent change clobbered)", got.ActiveWorkspaceKey)
	}
	// And the new token must be present.
	if got.WorkspaceTokens["staging"].Token != "tok-staging" {
		t.Errorf("staging token not merged: %+v", got.WorkspaceTokens)
	}
}

func TestMergeWorkspaceTokenSkipsWhenSessionAbsent(t *testing.T) {
	isolateHome(t)
	// No session on disk: caching must be a no-op, not recreate a session a
	// concurrent logout just deleted.
	tok := StoredAccessToken{Token: "tok", ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := mergeWorkspaceToken("staging", tok); err != nil {
		t.Fatalf("mergeWorkspaceToken on absent session: %v", err)
	}
	if _, err := os.Stat(SessionFilePath()); !os.IsNotExist(err) {
		t.Errorf("session file was created for an absent session (err=%v)", err)
	}
}

func TestEnvKeyShadowsWorkspace(t *testing.T) {
	cases := map[string]struct {
		envKey, savedKey, savedWS, activeWS string
		want                                bool
	}{
		"our key, same workspace":  {"k1", "k1", "wsA", "wsA", false},
		"our key, other workspace": {"k1", "k1", "wsA", "wsB", true},
		"our key, unrecorded":      {"k1", "k1", "", "wsA", false},
		"a key we did not mint":    {"k2", "k1", "wsA", "wsA", true},
		"nothing exported":         {"", "k1", "wsA", "wsB", false},
		"session has no workspace": {"k2", "k1", "wsA", "", false},
	}
	for name, tc := range cases {
		if got := EnvKeyShadowsWorkspace(tc.envKey, tc.savedKey, tc.savedWS, tc.activeWS); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// Pruning is hygiene, not judgement: an entry whose ExpiresAt is absent or
// unparseable (a session written before the field, or a truncated write) is
// not evidence of expiry, and deleting it here destroys a token the read path
// was about to decide on for itself. SaveSession must also leave the caller's
// own map alone — a caller that keeps using its *Session after saving used to
// watch entries disappear from underneath it.
func TestSaveSessionKeepsUnparseableExpiryAndDoesNotMutateTheCaller(t *testing.T) {
	isolateHome(t)
	in := validSession("prod")
	in.WorkspaceTokens["acme#no-expiry"] = StoredAccessToken{Token: "tok-noexp"}
	in.WorkspaceTokens["acme#garbage"] = StoredAccessToken{Token: "tok-garbage", ExpiresAt: "not-a-time"}
	in.WorkspaceTokens["acme#expired"] = StoredAccessToken{Token: "tok-old", ExpiresAt: "2000-01-01T00:00:00Z"}

	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	for _, key := range []string{"acme#no-expiry", "acme#garbage", "acme#expired"} {
		if _, present := in.WorkspaceTokens[key]; !present {
			t.Errorf("SaveSession removed %q from the caller's own map", key)
		}
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got.WorkspaceTokens["acme#no-expiry"].Token != "tok-noexp" {
		t.Errorf("an entry with no recorded expiry was pruned: %+v", got.WorkspaceTokens)
	}
	if got.WorkspaceTokens["acme#garbage"].Token != "tok-garbage" {
		t.Errorf("an entry with an unparseable expiry was pruned: %+v", got.WorkspaceTokens)
	}
	if _, present := got.WorkspaceTokens["acme#expired"]; present {
		t.Errorf("a provably expired entry survived the prune: %+v", got.WorkspaceTokens)
	}
}

func TestSessionHostRule(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.orq.ai":        "my.orq.ai",
		"https://my.orq.ai/":        "my.orq.ai",
		"https://My.Staging.ORQ.ai": "my.staging.orq.ai",
		"http://localhost:8080":     "localhost_8080",
		"http://127.0.0.1:3000/v2":  "127.0.0.1_3000",
		"https://orq.acme.internal": "orq.acme.internal",
		"https://[::1]:4200":        "__1_4200",
	} {
		if got := SessionHost(in); got != want {
			t.Errorf("SessionHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The session in play is the one for the server this invocation resolved:
// --server, ORQ_SERVER, `orq server set`, else the hosted default.
func TestSessionFilePathFollowsTheResolvedServer(t *testing.T) {
	isolateHome(t)
	t.Cleanup(func() { SetServer("", "default") })

	SetServer("", "default")
	if got := filepath.Base(SessionFilePath()); got != "my.orq.ai.json" {
		t.Errorf("default session file = %q, want my.orq.ai.json", got)
	}
	SetServer("https://my.staging.orq.ai", "flag")
	if got := filepath.Base(SessionFilePath()); got != "my.staging.orq.ai.json" {
		t.Errorf("staging session file = %q, want my.staging.orq.ai.json", got)
	}
}
