package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"orq/cli/custom/auth"
)

// projectsServer answers /v2/projects with the given payload rows.
func projectsServer(t *testing.T, rows string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[%s],"has_more":false}`, rows)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func projectStepState(t *testing.T, apiBase string) *authState {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	key := "acme"
	return &authState{
		apiBase: apiBase,
		bearer:  "session-token",
		session: &auth.Session{ActiveWorkspaceKey: &key},
	}
}

// The whole point of the step is that a later command mints a token scoped to
// what was chosen here, so the choice must reach the session file.
func TestProjectStepRecordsTheChoice(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"banking","name":"Banking"},{"project_id":"id-2","key":"other","name":"Other","is_default":true}`)
	state := projectStepState(t, srv.URL)
	opts := &setupOptions{project: "banking"}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err != nil {
		t.Fatalf("resolveProjectStep: %v", err)
	}
	if got == nil || got.ProjectID != "id-1" {
		t.Fatalf("chose %+v, want the project named by --project", got)
	}
	if state.session.ActiveProjectID != "id-1" || state.session.ActiveProjectName != "Banking" {
		t.Errorf("session records %q/%q, want id-1/Banking", state.session.ActiveProjectID, state.session.ActiveProjectName)
	}
	// The key minted in the next step is scoped from here.
	if state.projectID != "id-1" {
		t.Errorf("state.projectID = %q, want id-1", state.projectID)
	}
}

// Skipping is never fatal: a project-less session falls back to the workspace
// default, so every skip path must return cleanly and leave the session alone.
func TestProjectStepSkipsWithoutChoosing(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows string
		opts *setupOptions
		// noSession replaces the state with an API-key-only run.
		noSession bool
	}{
		{name: "--no-project", rows: `{"project_id":"id-1","key":"a","name":"A"}`, opts: &setupOptions{noProject: true}},
		{name: "empty workspace", rows: ``, opts: &setupOptions{}},
		{name: "no session", rows: ``, opts: &setupOptions{}, noSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := projectsServer(t, tc.rows)
			state := projectStepState(t, srv.URL)
			if tc.noSession {
				state.session = nil
			}
			got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, tc.opts)
			if err != nil {
				t.Fatalf("a skipped project step must not fail: %v", err)
			}
			if got != nil {
				t.Errorf("chose %+v, want no project", got)
			}
			if state.projectID != "" {
				t.Errorf("state.projectID = %q, want empty", state.projectID)
			}
		})
	}
}

// A single project is the common case and asking about it is noise; a
// non-interactive run with several takes the workspace default rather than
// prompting or failing.
func TestProjectStepAutoSelects(t *testing.T) {
	only := projectsServer(t, `{"project_id":"id-1","key":"only","name":"Only"}`)
	state := projectStepState(t, only.URL)
	got, err := resolveProjectStep(newReporter(true), auth.NewClient(only.URL), state, &setupOptions{})
	if err != nil || got == nil || got.ProjectID != "id-1" {
		t.Fatalf("single project: got %+v, err %v; want id-1 without a prompt", got, err)
	}

	many := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A"},{"project_id":"id-2","key":"b","name":"B","is_default":true}`)
	state = projectStepState(t, many.URL)
	got, err = resolveProjectStep(newReporter(true), auth.NewClient(many.URL), state, &setupOptions{})
	if err != nil || got == nil || got.ProjectID != "id-2" {
		t.Fatalf("non-interactive: got %+v, err %v; want the default project id-2", got, err)
	}
}

// A key minted for another project must not be reused: the agents would keep
// authenticating against the project the user just left.
func TestKeyProjectMismatch(t *testing.T) {
	for _, tc := range []struct {
		saved, active string
		want          bool
	}{
		{"id-1", "id-2", true},
		{"id-1", "id-1", false},
		{"", "id-1", false}, // minted before scoping existed
		{"id-1", "", false}, // no project active now
	} {
		if got := keyProjectMismatch(tc.saved, tc.active); got != tc.want {
			t.Errorf("keyProjectMismatch(%q, %q) = %v, want %v", tc.saved, tc.active, got, tc.want)
		}
	}
}
