package commands

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"orq/cli/custom/auth"

	"github.com/spf13/cobra"
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

// diskLogin is the login a gateway key test records on, read as setup reads it
// before minting.
func diskLogin(t *testing.T) *auth.Session {
	t.Helper()
	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// Clearing pairs nothing, so a session read before a workspace move must not
// move the workspace back.
func TestClearingTheProjectKeepsAWorkspaceSavedSinceTheRead(t *testing.T) {
	switchTestEnv(t)
	switchSession(t, "https://api.example", "acme", []string{"acme", "other"}, "id-1", "Banking")
	stale := diskLogin(t)
	changeSessionOnDisk(t, func(s *auth.Session) {
		other := "other"
		s.ActiveWorkspaceKey = &other
	})

	cmd := &cobra.Command{}
	if err := clearActiveProject(cmd, stale); err != nil {
		t.Fatal(err)
	}

	session := diskLogin(t)
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey != "other" || session.ActiveProjectID != "" {
		t.Errorf("active pair = %v/%q, want other with no project", session.ActiveWorkspaceKey, session.ActiveProjectID)
	}
}

// `orq switch` fetches the profile inside UseWorkspace, after the project list. A
// login replaced during that fetch must fail the switch, not be overwritten by it.
func TestSwitchFailsWhenTheLoginChangesDuringUseWorkspace(t *testing.T) {
	switchTestEnv(t)
	var listed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "ProfileService"):
			if listed.Swap(false) {
				changeSessionOnDisk(t, func(s *auth.Session) { s.RefreshToken = "refresh-new-login" })
			}
			fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","display_name":"A","workspaces":[{"key":"acme","name":"acme"},{"key":"other","name":"other"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/v2/projects"):
			listed.Store(true)
			fmt.Fprint(w, `{"data":[{"project_id":"id-1","key":"banking","name":"Banking"}],"has_more":false}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	switchSession(t, srv.URL, "acme", []string{"acme", "other"}, "", "")

	cmd := NewSwitchCommand()
	cmd.SetArgs([]string{"other", "banking"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); !errors.Is(err, auth.ErrSessionChanged) {
		t.Fatalf("switch error = %v, want ErrSessionChanged", err)
	}

	session := diskLogin(t)
	if session.RefreshToken != "refresh-new-login" || session.ActiveProjectID != "" {
		t.Errorf("new login was changed: refresh %q, project %q", session.RefreshToken, session.ActiveProjectID)
	}
}

// A key minted for one login must not land on the login that replaced it.
func TestGatewayKeyIsNotRecordedOnALoginThatReplacedTheMintingOne(t *testing.T) {
	switchTestEnv(t)
	switchSession(t, "https://api.example", "acme", []string{"acme"}, "", "")
	minting := diskLogin(t)
	changeSessionOnDisk(t, func(s *auth.Session) { s.RefreshToken = "refresh-new-login" })

	err := saveGatewayKeyProfile(minting, "orq-key", "key-1", time.Now().Add(time.Hour), "acme", "")
	if !errors.Is(err, auth.ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	if session := diskLogin(t); session.GatewayKey != "" {
		t.Errorf("new login received key %q", session.GatewayKey)
	}
}

// onDiskLogin makes a partial fixture session the login on disk, as the session
// a real setup run holds always is.
func onDiskLogin(t *testing.T, session *auth.Session) *auth.Session {
	t.Helper()
	session.RefreshToken = diskLogin(t).RefreshToken
	return session
}

// The mint is a network call, so setup has to read the login before it.
func TestMintFailsWhenTheLoginChangesDuringTheRequest(t *testing.T) {
	credsHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":"DOMAIN_GROUP_GATEWAY","writable":true}]}`)
			return
		}
		changeSessionOnDisk(t, func(s *auth.Session) { s.RefreshToken = "refresh-new-login" })
		fmt.Fprint(w, `{"token":"sk-orq-KEYID1-secret","api_key":{"id":"KEYID1"}}`)
	}))
	defer srv.Close()
	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: onDiskLogin(t, &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}})}

	_, _, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if !errors.Is(err, auth.ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	if session := diskLogin(t); session.GatewayKey != "" {
		t.Errorf("new login received key %q", session.GatewayKey)
	}
}

// A key with no login to land on would stay live on the platform with nothing
// pointing at it, so setup must not mint one.
func TestNoLoginMeansNoMint(t *testing.T) {
	credsHarness(t)
	if err := auth.ClearSession(); err != nil {
		t.Fatal(err)
	}
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":"DOMAIN_GROUP_GATEWAY","writable":true}]}`)
			return
		}
		mints.Add(1)
		fmt.Fprint(w, `{"token":"sk-orq-KEYID1-secret","api_key":{"id":"KEYID1"}}`)
	}))
	defer srv.Close()

	state := &authState{apiBase: srv.URL, bearer: "t"}
	_, _, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if !errors.Is(err, errNoLoginForGatewayKey) {
		t.Fatalf("error = %v, want errNoLoginForGatewayKey", err)
	}
	if got := mints.Load(); got != 0 {
		t.Errorf("minted %d keys with no login to save them on", got)
	}
}
