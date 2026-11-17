package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateSessionKeepsWhatAnotherWriterSaved(t *testing.T) {
	isolateHome(t)
	seed := validSession("prod")
	if err := SaveSession(seed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}

	other, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	other.GatewayKey = "orq-key"
	if err := SaveSession(other); err != nil {
		t.Fatal(err)
	}

	if err := UpdateSession(snapshot, func(s *Session) { s.ActiveProjectID = "id-1" }); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayKey != "orq-key" || got.ActiveProjectID != "id-1" {
		t.Errorf("on disk: key %q, project %q; want both writes kept", got.GatewayKey, got.ActiveProjectID)
	}
	if snapshot.GatewayKey != "orq-key" || snapshot.ActiveProjectID != "id-1" {
		t.Errorf("caller's copy: key %q, project %q; want it to match disk", snapshot.GatewayKey, snapshot.ActiveProjectID)
	}
}

func TestUpdateSessionConcurrentWritersAllSurvive(t *testing.T) {
	isolateHome(t)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	const writers = 20
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := ReadSession()
			if err != nil {
				t.Error(err)
				return
			}
			key := fmt.Sprintf("ws-%d", i)
			if err := UpdateSession(snapshot, func(s *Session) {
				s.WorkspaceTokens[key] = StoredAccessToken{Token: key, ExpiresAt: "2099-01-01T00:00:00Z"}
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	got, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WorkspaceTokens) != writers {
		t.Errorf("%d of %d writes survived", len(got.WorkspaceTokens), writers)
	}
}

func TestUpdateSessionRefusesAReplacedLogin(t *testing.T) {
	isolateHome(t)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	replaced := validSession("prod")
	replaced.RefreshToken = "refresh-new-login"
	if err := SaveSession(replaced); err != nil {
		t.Fatal(err)
	}

	err = UpdateSession(snapshot, func(s *Session) { s.ActiveProjectID = "id-1" })
	if !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	got, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveProjectID != "" {
		t.Errorf("new login's project = %q, want it untouched", got.ActiveProjectID)
	}
}

func TestUpdateSessionDoesNotRecreateARemovedLogin(t *testing.T) {
	isolateHome(t)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := ClearSession(); err != nil {
		t.Fatal(err)
	}

	err = UpdateSession(snapshot, func(s *Session) { s.ActiveProjectID = "id-1" })
	if !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	if got, err := ReadSession(); err != nil || got != nil {
		t.Errorf("session after logout = %v, %v; want none", got, err)
	}
}

// A login blocks on the network after reading the previous session. A gateway
// key `orq setup` saves in that window has to survive the login's write.
func TestLoginKeepsAGatewayKeyWrittenDuringTheRequest(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/access-token"):
			current, err := ReadSession()
			if err != nil {
				t.Error(err)
				return
			}
			current.GatewayKey = "orq-key"
			if err := SaveSession(current); err != nil {
				t.Error(err)
			}
			fmt.Fprintf(w, `{"access_token":%q}`, jwtWithExpiry(t, time.Now().Add(time.Hour)))
		case r.URL.Path == ProfileRPCPath:
			fmt.Fprint(w, `{"profile":{"id":"user-1","workspaces":[{"key":"acme","name":"acme"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := loginAgainst(t, srv)
	if err := SaveSession(sessionWith("user-1", "acme", carriedFields{})); err != nil {
		t.Fatal(err)
	}

	approved := &ApprovedDeviceLogin{
		AccessToken:  jwtWithExpiry(t, time.Now().Add(time.Hour)),
		RefreshToken: "refresh-new",
	}
	if _, err := client.CreateSessionFromDeviceApproval(approved, nil, "acme"); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayKey != "orq-key" || got.RefreshToken != "refresh-new" {
		t.Errorf("key %q, refresh %q; want the key kept on the new login", got.GatewayKey, got.RefreshToken)
	}
}
