package custom

import (
	"net/http"
	"net/http/httptest"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// 12.10.1 shipped the named handler, so `auth profile add` there saved
// "type": "ApiKey". Those profiles must keep working after the handler goes
// back to anonymous, like the "apikey" ones from before it.
func TestProfileSavedUnderTheNamedTypeStillAuthenticates(t *testing.T) {
	profileHarness(t, `{"profiles":{"default":{"api_key":"sk-orq-CCC","type":"ApiKey"}}}`)
	root := buildRoot(t)
	root.SetArgs([]string{"--profile", "default", "version"})
	var err error
	captureOutput(t, func() { err = root.Execute() })
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	if _, err := bartolocli.Client.URL(srv.URL).Get().Do(); err != nil {
		t.Fatalf("a profile saved by 12.10.1 no longer authenticates: %v", err)
	}
	if gotAuth != "Bearer sk-orq-CCC" {
		t.Errorf("Authorization = %q, want the profile's key", gotAuth)
	}
}

func TestGeneratedAuthIsAnonymous(t *testing.T) {
	root := buildRoot(t)
	if _, named := bartolocli.AuthHandlers[generatedAuthType]; named {
		t.Errorf("auth handler is still registered as %q", generatedAuthType)
	}
	if _, ok := bartolocli.AuthHandlers[""]; !ok || len(bartolocli.AuthHandlers) != 1 {
		t.Errorf("want exactly one anonymous auth handler, got %d", len(bartolocli.AuthHandlers))
	}
	add := commandAt(root, "auth", "profile", "add")
	if add == nil {
		t.Fatal("no auth profile add")
	}
	if add.RunE == nil || childCommand(add, generatedAuthType) != nil {
		t.Errorf("auth profile add should be one command, not a parent of %q", generatedAuthType)
	}
}
