package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshHealthPreservesConcurrentSessionChanges(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Client, *Session)
	}{
		{"check", func(c *Client, s *Session) { CheckSession(SessionHost(s.APIBaseURL)) }},
		{"bootstrap", func(c *Client, s *Session) { c.EnsureBootstrapToken(s) }},
		{"workspace", func(c *Client, s *Session) { c.EnsureWorkspaceToken(s, "acme") }},
		{"workspace override", func(c *Client, s *Session) { c.WorkspaceToken(s, "acme") }},
	}
	for _, operation := range operations {
		for _, rejected := range []bool{false, true} {
			for _, change := range []string{"login", "logout", "settings"} {
				t.Run(fmt.Sprintf("%s/rejected=%t/%s", operation.name, rejected, change), func(t *testing.T) {
					isolateHome(t)
					token := jwtWithExpiry(t, time.Now().Add(time.Hour))
					var concurrent *Session
					var requests atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if change == "logout" {
							if err := ClearSession(); err != nil {
								t.Error(err)
							}
						} else {
							if err := SaveSession(concurrent); err != nil {
								t.Error(err)
							}
						}
						if rejected {
							w.WriteHeader(400)
							fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
						} else {
							fmt.Fprintf(w, `{"access_token":%q}`, token)
						}
					}))
					defer srv.Close()
					previous, source := Server(), ServerSource()
					SetServer(srv.URL, "flag")
					t.Cleanup(func() { SetServer(previous, source) })
					original := validSession("acme")
					original.APIBaseURL = srv.URL
					original.RefreshToken = "old-refresh"
					original.BootstrapToken.ExpiresAt = "2000-01-01T00:00:00Z"
					if err := SaveSession(original); err != nil {
						t.Fatal(err)
					}
					latest := *original
					concurrent = &latest
					concurrent.GatewayKey = "new-gateway"
					concurrent.ActiveProjectID = "new-project"
					active := "new-selection"
					concurrent.ActiveWorkspaceKey = &active
					concurrent.WorkspaceTokens = map[string]StoredAccessToken{"other": {Token: "other-token", ExpiresAt: "2099-01-01T00:00:00Z"}}
					if change == "login" {
						concurrent.RefreshToken = "fresh-login"
						concurrent.BootstrapToken = StoredAccessToken{Token: "fresh-bootstrap", ExpiresAt: "2099-01-01T00:00:00Z"}
					}
					operation.run(clientFor(srv), original)
					if requests.Load() != 1 {
						t.Fatalf("requests = %d, want 1", requests.Load())
					}
					current, err := ReadSession()
					if err != nil {
						t.Fatal(err)
					}
					if change == "logout" {
						if current != nil {
							t.Fatalf("refresh recreated a logged-out session")
						}
						if _, err := os.Stat(SessionFilePath()); !os.IsNotExist(err) {
							t.Fatalf("session exists: %v", err)
						}
						return
					}
					if current == nil {
						t.Fatal("session disappeared")
					}
					wantProject := "new-project"
					if operation.name == "workspace" && !rejected && change != "login" {
						wantProject = ""
					}
					if current.RefreshToken != concurrent.RefreshToken || current.GatewayKey != "new-gateway" || current.ActiveProjectID != wantProject || current.WorkspaceTokens["other"].Token != "other-token" {
						t.Fatalf("concurrent state overwritten: refresh=%q gateway=%q project=%q tokens=%v", current.RefreshToken, current.GatewayKey, current.ActiveProjectID, current.WorkspaceTokens)
					}
					wantSignedOut := rejected && change != "login"
					if current.SignedOut != wantSignedOut {
						t.Fatalf("signedOut=%v, want %v", current.SignedOut, wantSignedOut)
					}
					if change == "login" && current.BootstrapToken.Token != "fresh-bootstrap" {
						t.Fatal("fresh login's bootstrap overwritten")
					}
					if operation.name != "workspace" && *current.ActiveWorkspaceKey != active {
						t.Fatal("active selection overwritten")
					}
				})
			}
		}
	}
}

func TestProfileRefreshPreservesConcurrentSessionChanges(t *testing.T) {
	for _, operation := range []string{"profile", "active token"} {
		for _, when := range []string{"bootstrap", "profile", "workspace"} {
			for _, change := range []string{"login", "logout", "settings"} {
				if operation == "profile" && when == "workspace" {
					continue
				}
				t.Run(operation+"/"+when+"/"+change, func(t *testing.T) {
					isolateHome(t)
					token := jwtWithExpiry(t, time.Now().Add(time.Hour))
					var concurrent *Session
					var changed atomic.Bool
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						profileRequest := r.URL.Path == ProfileRPCPath
						if ((when == "profile" && profileRequest) || (when != "profile" && !profileRequest)) && changed.CompareAndSwap(false, true) {
							if change == "logout" {
								if err := ClearSession(); err != nil {
									t.Error(err)
								}
							} else if err := SaveSession(concurrent); err != nil {
								t.Error(err)
							}
						}
						if profileRequest {
							fmt.Fprint(w, `{"profile":{"id":"user","workspaces":[{"key":"acme"},{"key":"new-selection"}],"preferences":{"active_workspace":"acme"}}}`)
						} else {
							fmt.Fprintf(w, `{"access_token":%q}`, token)
						}
					}))
					defer srv.Close()
					previous, source := Server(), ServerSource()
					SetServer(srv.URL, "flag")
					t.Cleanup(func() { SetServer(previous, source) })
					original := validSession("acme")
					original.APIBaseURL = srv.URL
					if when == "bootstrap" {
						original.BootstrapToken.ExpiresAt = "2000-01-01T00:00:00Z"
					}
					if err := SaveSession(original); err != nil {
						t.Fatal(err)
					}
					next := *original
					concurrent = &next
					next.GatewayKey = "new-key"
					next.ActiveProjectID = "new-project"
					selection := "new-selection"
					next.ActiveWorkspaceKey = &selection
					if change == "login" {
						next.RefreshToken = "fresh-login"
						next.BootstrapToken = StoredAccessToken{Token: "fresh-bootstrap", ExpiresAt: "2099-01-01T00:00:00Z"}
					}
					client := clientFor(srv)
					if operation == "profile" {
						if _, err := client.RefreshProfile(original); err != nil {
							t.Fatal(err)
						}
					} else if _, err := client.GetActiveWorkspaceAccessToken(); err != nil {
						t.Fatal(err)
					}
					if !changed.Load() {
						t.Fatal("concurrent update was not exercised")
					}
					current, err := ReadSession()
					if err != nil {
						t.Fatal(err)
					}
					if change == "logout" {
						if current != nil {
							t.Fatal("profile continuation recreated logged-out session")
						}
						return
					}
					if current == nil {
						t.Fatal("session disappeared")
					}
					if current.RefreshToken != next.RefreshToken || current.GatewayKey != "new-key" || current.ActiveProjectID != "new-project" || *current.ActiveWorkspaceKey != selection {
						t.Fatalf("profile continuation overwrote concurrent state: %+v", current)
					}
					if change == "login" && current.BootstrapToken.Token != "fresh-bootstrap" {
						t.Fatal("fresh bootstrap was overwritten")
					}
				})
			}
		}
	}
}
