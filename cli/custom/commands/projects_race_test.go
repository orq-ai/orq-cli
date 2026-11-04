package commands

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/auth"
)

// projectsServerDuring runs during on the project list request, which is the network
// round trip `orq projects use` holds its session across.
func projectsServerDuring(t *testing.T, during func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "ProfileService"):
			fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","display_name":"A","workspaces":[{"key":"acme","name":"acme"},{"key":"other","name":"other"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/v2/projects"):
			during()
			fmt.Fprint(w, `{"data":[{"project_id":"id-1","key":"banking","name":"Banking"}],"has_more":false}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func changeSessionOnDisk(t *testing.T, change func(*auth.Session)) {
	t.Helper()
	session, err := auth.ReadSession()
	if err != nil {
		t.Error(err)
		return
	}
	change(session)
	if err := auth.SaveSession(session); err != nil {
		t.Error(err)
	}
}

// An `orq setup` finishing while the project list is in flight writes a gateway
// key this command's session was read before. Saving that older copy lost a key
// that still works server-side.
func TestProjectsUseKeepsAGatewayKeyWrittenDuringTheRequest(t *testing.T) {
	switchTestEnv(t)
	srv := projectsServerDuring(t, func() {
		changeSessionOnDisk(t, func(s *auth.Session) {
			s.GatewayKey, s.GatewayKeyID = "orq-key", "key-1"
			s.GatewayKeyExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		})
	})
	switchSession(t, srv.URL, "acme", []string{"acme"}, "", "")

	cmd := NewProjectsUseCommand()
	cmd.SetArgs([]string{"banking"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("projects use: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.GatewayKey != "orq-key" {
		t.Errorf("gateway key = %q, want the one written during the request", session.GatewayKey)
	}
	if session.ActiveProjectID != "id-1" {
		t.Errorf("active project = %q, want id-1", session.ActiveProjectID)
	}
}

// A project chosen from one login's list means nothing on the login that
// replaced it, so the command has to fail rather than report a switch.
func TestProjectsUseFailsWhenTheLoginChangedDuringTheRequest(t *testing.T) {
	switchTestEnv(t)
	srv := projectsServerDuring(t, func() {
		changeSessionOnDisk(t, func(s *auth.Session) { s.RefreshToken = "refresh-new-login" })
	})
	switchSession(t, srv.URL, "acme", []string{"acme"}, "", "")

	cmd := NewProjectsUseCommand()
	cmd.SetArgs([]string{"banking"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); !errors.Is(err, auth.ErrSessionChanged) {
		t.Fatalf("projects use error = %v, want ErrSessionChanged", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.RefreshToken != "refresh-new-login" || session.ActiveProjectID != "" {
		t.Errorf("new login was changed: refresh %q, project %q", session.RefreshToken, session.ActiveProjectID)
	}
}

// A project belongs to the workspace it was listed from, so the pair lands
// together even if `orq workspace use` moved the session during the request.
func TestProjectsUseWritesTheWorkspaceWithItsProject(t *testing.T) {
	switchTestEnv(t)
	srv := projectsServerDuring(t, func() {
		changeSessionOnDisk(t, func(s *auth.Session) {
			other := "other"
			s.ActiveWorkspaceKey = &other
		})
	})
	switchSession(t, srv.URL, "acme", []string{"acme", "other"}, "", "")

	cmd := NewProjectsUseCommand()
	cmd.SetArgs([]string{"banking"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("projects use: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey != "acme" || session.ActiveProjectID != "id-1" {
		t.Errorf("active pair = %v/%q, want acme/id-1", session.ActiveWorkspaceKey, session.ActiveProjectID)
	}
}
