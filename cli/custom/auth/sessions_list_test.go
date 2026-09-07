package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeSessionFile drops raw bytes at sessions/<name>, bypassing SaveSession so
// a test can write a file SaveSession would never produce.
func writeSessionFile(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func marshalSession(t *testing.T, s *Session) []byte {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestListSessionsNoDirectory(t *testing.T) {
	isolateHome(t)

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %v", sessions)
	}
}

// The listing is one row per host, sorted, carrying what identifies the login.
func TestListSessionsReportsEachHost(t *testing.T) {
	isolateHome(t)

	staging := validSession("staging-ws")
	staging.APIBaseURL = "https://my.staging.orq.ai"
	staging.User = &SessionUser{Email: "someone@example.com"}
	staging.ActiveProjectName = "checkout"
	writeSessionFile(t, "my.staging.orq.ai.json", marshalSession(t, staging))

	prod := validSession("prod-ws")
	prod.APIBaseURL = "https://my.orq.ai"
	writeSessionFile(t, "my.orq.ai.json", marshalSession(t, prod))

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(sessions), sessions)
	}
	if sessions[0].Host != "my.orq.ai" || sessions[1].Host != "my.staging.orq.ai" {
		t.Fatalf("expected hosts sorted, got %q and %q", sessions[0].Host, sessions[1].Host)
	}

	got := sessions[1]
	if got.Server != "https://my.staging.orq.ai" {
		t.Errorf("Server = %q", got.Server)
	}
	if got.User != "someone@example.com" {
		t.Errorf("User = %q", got.User)
	}
	if got.Workspace != "staging-ws" {
		t.Errorf("Workspace = %q", got.Workspace)
	}
	if got.Project != "checkout" {
		t.Errorf("Project = %q", got.Project)
	}
	if got.Expired {
		t.Error("Expired = true, want false for a 2099 bootstrap token")
	}
}

// migrateSessionFiles parks the loser of a host collision under
// <name>.json.deprecated. Those are not logins and must not be listed.
func TestListSessionsSkipsDeprecatedAndNonJSON(t *testing.T) {
	isolateHome(t)

	writeSessionFile(t, "my.orq.ai.json", marshalSession(t, validSession("prod")))
	writeSessionFile(t, "work.json.deprecated", marshalSession(t, validSession("old")))
	writeSessionFile(t, ".hidden.json", marshalSession(t, validSession("hidden")))
	writeSessionFile(t, "notes.txt", []byte("not a session"))

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Host != "my.orq.ai" {
		t.Fatalf("expected only my.orq.ai, got %v", sessions)
	}
}

// A session too broken to decode is what someone runs this command to find, so
// it is reported by host rather than dropped.
func TestListSessionsReportsUnreadableSession(t *testing.T) {
	isolateHome(t)

	writeSessionFile(t, "broken.example.json", []byte("{not json"))

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected the broken session listed, got %v", sessions)
	}
	if sessions[0].Host != "broken.example" || sessions[0].Server != "" {
		t.Fatalf("expected a host-only row, got %+v", sessions[0])
	}
}

func TestListSessionsMarksExpiredBootstrapToken(t *testing.T) {
	isolateHome(t)

	stale := validSession("prod")
	stale.BootstrapToken.ExpiresAt = "2000-01-01T00:00:00Z"
	writeSessionFile(t, "my.orq.ai.json", marshalSession(t, stale))

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if !sessions[0].Expired {
		t.Error("Expired = false, want true for a year-2000 bootstrap token")
	}
}

// "Active" is the session this invocation would actually use, which is the
// host --server / ORQ_SERVER / `orq server set` resolved — not a stored flag.
func TestListSessionsMarksResolvedHostActive(t *testing.T) {
	isolateHome(t)
	SetServer("", "test")

	writeSessionFile(t, "my.orq.ai.json", marshalSession(t, validSession("prod")))
	writeSessionFile(t, "my.staging.orq.ai.json", marshalSession(t, validSession("staging")))

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	byHost := map[string]bool{}
	for _, s := range sessions {
		byHost[s.Host] = s.Active
	}
	if !byHost["my.orq.ai"] {
		t.Errorf("expected the default host active, got %v", byHost)
	}
	if byHost["my.staging.orq.ai"] {
		t.Errorf("expected staging inactive, got %v", byHost)
	}
}
