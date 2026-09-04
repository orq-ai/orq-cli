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
	opts := &setupOptions{
		project: "banking",
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("an explicit --project reached the picker")
			return nil, nil
		},
	}

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
	soleProject := &setupOptions{
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("a sole project reached the picker")
			return nil, nil
		},
	}
	got, err := resolveProjectStep(newReporter(true), auth.NewClient(only.URL), state, soleProject)
	if err != nil || got == nil || got.ProjectID != "id-1" {
		t.Fatalf("single project: got %+v, err %v; want id-1 without a prompt", got, err)
	}

	many := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A"},{"project_id":"id-2","key":"b","name":"B","is_default":true}`)
	state = projectStepState(t, many.URL)
	nonInteractive := &setupOptions{
		noInput:     true,
		interactive: true,
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("--no-input reached the project picker")
			return nil, nil
		},
	}
	got, err = resolveProjectStep(newReporter(true), auth.NewClient(many.URL), state, nonInteractive)
	if err != nil || got == nil || got.ProjectID != "id-2" {
		t.Fatalf("non-interactive: got %+v, err %v; want the default project id-2", got, err)
	}
}

// A normal TTY setup is already interactive; choosing a project must not
// require the broader -i flag.
func TestProjectStepUsesPickerInDefaultTTYMode(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A","is_default":true},{"project_id":"id-2","key":"b","name":"B"}`)
	state := projectStepState(t, srv.URL)
	pickerCalled := false
	opts := &setupOptions{
		pickProjectFn: func(projects []auth.Project) (*auth.Project, error) {
			pickerCalled = true
			if len(projects) != 2 {
				t.Fatalf("picker received %d projects, want 2", len(projects))
			}
			return &projects[1], nil
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err != nil {
		t.Fatalf("resolveProjectStep: %v", err)
	}
	if !pickerCalled {
		t.Fatal("default TTY setup did not call the project picker")
	}
	if got == nil || got.ProjectID != "id-2" {
		t.Fatalf("chose %+v, want picker result id-2", got)
	}
	if state.session.ActiveProjectID != "id-2" || state.projectID != "id-2" {
		t.Errorf("project was not persisted and forwarded: session=%q state=%q", state.session.ActiveProjectID, state.projectID)
	}
}

func TestProjectStepReturnsPickerError(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A"},{"project_id":"id-2","key":"b","name":"B"}`)
	state := projectStepState(t, srv.URL)
	opts := &setupOptions{
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			return nil, fmt.Errorf("picker cancelled")
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err == nil || err.Error() != "picker cancelled" {
		t.Fatalf("resolveProjectStep = (%+v, %v), want nil project and picker cancellation", got, err)
	}
	if state.projectID != "" {
		t.Errorf("state.projectID = %q after cancellation, want empty", state.projectID)
	}
}

// -y answers confirmations; it never pre-answers a selection. An unattended run
// pre-answers this step with --project or --no-input instead.
func TestProjectStepStillPromptsUnderYes(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A","is_default":true},{"project_id":"id-2","key":"b","name":"B"}`)
	state := projectStepState(t, srv.URL)
	pickerCalled := false
	opts := &setupOptions{
		yes: true,
		pickProjectFn: func(projects []auth.Project) (*auth.Project, error) {
			pickerCalled = true
			return &projects[1], nil
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err != nil {
		t.Fatalf("resolveProjectStep: %v", err)
	}
	if !pickerCalled {
		t.Fatal("-y skipped the project picker; it answers confirmations, not selections")
	}
	if got == nil || got.ProjectID != "id-2" {
		t.Fatalf("chose %+v, want picker result id-2", got)
	}
}

// A step that chooses nothing must still hand the key step the project the
// session already claims. Leaving it empty minted a workspace-wide key for a
// session that named a project, so the agents reached every other project too.
func TestProjectStepKeepsTheSessionProjectWhenListingFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	state := projectStepState(t, srv.URL)
	state.session.ActiveProjectID = "id-9"
	opts := &setupOptions{
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("a failed listing reached the picker")
			return nil, nil
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err != nil || got != nil {
		t.Fatalf("resolveProjectStep = (%+v, %v), want (nil, nil) — a listing failure is not fatal", got, err)
	}
	if state.projectID != "id-9" {
		t.Errorf("state.projectID = %q, want the session's own project id-9 scoping the minted key", state.projectID)
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

// `setup --no-project` documents itself as leaving the session unscoped, so a
// project chosen earlier has to go; returning early made the flag a no-op for
// exactly the users who had one to clear.
func TestSetupNoProjectClearsTheActiveProject(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"acme"}, `{"project_id":"id-1","key":"a","name":"A"}`)
	switchSession(t, srv.URL, "acme", []string{"acme"}, "id-1", "A")
	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}

	state := &authState{apiBase: srv.URL, bearer: "session-token", session: session}
	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noProject: true})
	if err != nil {
		t.Fatalf("resolveProjectStep: %v", err)
	}
	if got != nil {
		t.Errorf("chose %+v, want no project", got)
	}
	saved, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveProjectID != "" || saved.ActiveProjectName != "" {
		t.Errorf("session still scoped to %q/%q after --no-project, want it cleared", saved.ActiveProjectID, saved.ActiveProjectName)
	}
}
