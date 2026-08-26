package commands

import (
	"testing"

	"orq/cli/custom/auth"
)

// One name for the host. The root PreRun decides it; everything here reads that
// decision, and the session only wins when nothing explicit did.
func TestServerResolution(t *testing.T) {
	t.Cleanup(func() { auth.SetServer("", "default") })
	session := &auth.Session{APIBaseURL: "https://session.example"}

	auth.SetServer("", "default")
	if got := sessionAPIBase(session); got != "https://session.example" {
		t.Fatalf("no override: got %q", got)
	}

	auth.SetServer("https://flag.example", "flag")
	if got := sessionAPIBase(session); got != "https://flag.example" {
		t.Fatalf("resolved server should win: got %q", got)
	}
	if got := auth.ServerSource(); got != "flag" {
		t.Fatalf("source: got %q", got)
	}

	// Provenance is recorded, not inferred: an env var holding exactly the
	// session's host must still report as env, which is what `orq doctor` shows.
	auth.SetServer(session.APIBaseURL, "env")
	if got := auth.ServerSource(); got != "env" {
		t.Fatalf("source for a value equal to the session host: got %q", got)
	}

	auth.SetServer("", "default")
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "https://legacy.example")
	if got := apiBaseFromEnv(); got != "https://legacy.example" {
		t.Fatalf("deprecated env: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "https://env.example")
	if got := apiBaseFromEnv(); got != "https://env.example" {
		t.Fatalf("ORQ_SERVER should beat the deprecated spelling: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "")
	if got := apiBaseFromEnv(); got != auth.DefaultAPIBaseURL {
		t.Fatalf("default: got %q", got)
	}
}
