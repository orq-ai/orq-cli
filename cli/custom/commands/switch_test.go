package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// switchServer answers the profile fetch every WhoAmI/UseWorkspace call makes
// and /v2/projects with the given rows. One server covers both routes because
// `switch` walks workspace then project in a single command.
func switchServer(t *testing.T, workspaceKeys []string, projectRows string) *httptest.Server {
	t.Helper()
	wsJSON := make([]string, 0, len(workspaceKeys))
	for _, k := range workspaceKeys {
		wsJSON = append(wsJSON, fmt.Sprintf(`{"key":%q,"name":%q}`, k, k))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "ProfileService"):
			fmt.Fprintf(w, `{"profile":{"id":"u1","email":"a@b.c","display_name":"A","workspaces":[%s]}}`, strings.Join(wsJSON, ","))
		case strings.HasSuffix(r.URL.Path, "/v2/projects"):
			fmt.Fprintf(w, `{"data":[%s],"has_more":false}`, projectRows)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// switchSession seeds a logged-in session with a pre-cached, far-future token
// for every workspace a test names, so the run never needs the real
// access-token exchange route: only the two routes switch.go's own decision
// logic touches (profile fetch, /v2/projects) are exercised.
func switchSession(t *testing.T, apiBase, activeWorkspace string, workspaceKeys []string, activeProjectID, activeProjectName string) {
	t.Helper()
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokens := map[string]auth.StoredAccessToken{}
	for _, k := range workspaceKeys {
		tokens[k] = auth.StoredAccessToken{Token: "tok-" + k, ExpiresAt: exp}
	}
	var active *string
	if activeWorkspace != "" {
		active = &activeWorkspace
	}
	if err := auth.SaveSession(&auth.Session{
		Version: 1, APIBaseURL: apiBase, AuthBaseURL: apiBase, V1BaseURL: apiBase, ProfileBaseURL: apiBase,
		RefreshToken:       "refresh",
		BootstrapToken:     auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: exp},
		ActiveWorkspaceKey: active,
		ActiveProjectID:    activeProjectID,
		ActiveProjectName:  activeProjectName,
		WorkspaceTokens:    tokens,
	}); err != nil {
		t.Fatal(err)
	}
}

// switchTestEnv isolates the session file under a throwaway HOME and forces
// the non-interactive path, since every scenario here is about a scripted
// `orq switch` run rather than the picker.
func switchTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORQ_API_KEY", "")
	// Restore the previous values rather than zeroing them: these are process
	// globals shared with every other test in the package.
	prevDir, prevProfile, prevNoInput := viper.GetString("config-directory"), viper.GetString("profile"), viper.GetBool("no-input")
	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	viper.Set("no-input", true)
	t.Cleanup(func() {
		viper.Set("config-directory", prevDir)
		viper.Set("profile", prevProfile)
		viper.Set("no-input", prevNoInput)
	})
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
}

// A scripted `orq switch <workspace> <project>` is the one case with enough
// information to answer both halves outright; if only the workspace half made
// it to disk, the next command in the script would silently run against
// whatever project (or none) was active before.
func TestSwitchRecordsWorkspaceAndProject(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"acme"},
		`{"project_id":"id-1","key":"banking","name":"Banking"},{"project_id":"id-2","key":"other","name":"Other"}`)
	switchSession(t, srv.URL, "", []string{"acme"}, "", "")

	cmd := NewSwitchCommand()
	cmd.SetArgs([]string{"acme", "banking"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("switch: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey != "acme" {
		t.Errorf("active workspace = %v, want acme", session.ActiveWorkspaceKey)
	}
	if session.ActiveProjectID != "id-1" || session.ActiveProjectName != "Banking" {
		t.Errorf("active project = %q/%q, want id-1/Banking", session.ActiveProjectID, session.ActiveProjectName)
	}
}

// A non-interactive run given only a workspace has no picker to fall back on,
// so with several projects in play it must take the workspace's declared
// default rather than erroring or blocking on a prompt that will never be
// answered.
func TestSwitchWorkspaceOnlyFallsBackToDefaultProject(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"acme"},
		`{"project_id":"id-1","key":"a","name":"A"},{"project_id":"id-2","key":"b","name":"B","is_default":true}`)
	switchSession(t, srv.URL, "", []string{"acme"}, "", "")

	cmd := NewSwitchCommand()
	cmd.SetArgs([]string{"acme"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("switch: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveProjectID != "id-2" || session.ActiveProjectName != "B" {
		t.Errorf("active project = %q/%q, want the workspace default id-2/B", session.ActiveProjectID, session.ActiveProjectName)
	}
}

// A workspace with no projects is a complete answer, not a failure: erroring
// here would make `orq switch <workspace>` unusable for the workspaces most
// likely to be freshly created and still empty.
func TestSwitchWorkspaceWithNoProjectsSucceeds(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"acme"}, ``)
	switchSession(t, srv.URL, "", []string{"acme"}, "", "")

	cmd := NewSwitchCommand()
	cmd.SetArgs([]string{"acme"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("a project-less workspace must not fail the switch: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveProjectID != "" || session.ActiveProjectName != "" {
		t.Errorf("active project = %q/%q, want none", session.ActiveProjectID, session.ActiveProjectName)
	}
}

// Switching workspace must clear whatever project was active before: a project
// id minted for the old workspace is meaningless in the new one, and carrying
// it across would point every following command at a project the user never
// chose in this workspace.
func TestSwitchWorkspaceClearsStaleProject(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"ws1", "ws2"}, ``) // ws2 has no projects
	switchSession(t, srv.URL, "ws1", []string{"ws1", "ws2"}, "stale-id", "Stale")

	cmd := NewSwitchCommand()
	cmd.SetArgs([]string{"ws2"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("switch: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey != "ws2" {
		t.Errorf("active workspace = %v, want ws2", session.ActiveWorkspaceKey)
	}
	if session.ActiveProjectID != "" || session.ActiveProjectName != "" {
		t.Errorf("ws1's project survived the switch to ws2: %q/%q, want none", session.ActiveProjectID, session.ActiveProjectName)
	}
}
