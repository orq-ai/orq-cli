package launch

import (
	"reflect"
	"testing"
)

// Run's dry-run path exercises the full wiring: argv parse → credential
// resolution (env key) → agent resolve → redacted print, without launching
// anything. Non-TTY test env skips the interactive prompt.
func TestRunDryRun(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "test-key")
	t.Setenv("ORQ_LAUNCH_NON_INTERACTIVE", "1")

	for _, name := range []string{"claude", "opencode", "kilo", "pi"} {
		def := FindAgent(name)
		if def == nil {
			t.Fatalf("agent %s missing from registry", name)
		}
		code, err := Run(def, []string{"--dry-run", "--no-fetch-models"})
		if err != nil || code != 0 {
			t.Fatalf("%s dry-run: code=%d err=%v", name, code, err)
		}
	}
}

// Sandbox dry-run must be side-effect free: no docker required at all
// (this test env may not have a daemon), no image build, no container.
func TestRunSandboxDryRun(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "test-key")
	t.Setenv("ORQ_LAUNCH_NON_INTERACTIVE", "1")
	t.Setenv("PATH", "") // any docker invocation would fail loudly

	for _, name := range []string{"claude", "opencode", "kimi", "pi"} {
		def := FindAgent(name)
		code, err := Run(def, []string{"--sandbox", "--dry-run", "--no-fetch-models"})
		if err != nil || code != 0 {
			t.Fatalf("%s sandbox dry-run: code=%d err=%v", name, code, err)
		}
	}
}

func TestCompletionFlags(t *testing.T) {
	def := FindAgent("opencode") // has AllowModels + a prompt mapping
	if got := CompletionFlags(def, "exec"); got != nil {
		t.Fatalf("non-flag input must complete nothing: %v", got)
	}
	got := CompletionFlags(def, "--mo")
	want := []string{"--model", "--mount-cwd", "--models"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := CompletionFlags(FindAgent("claude"), "--models"); got != nil {
		t.Fatalf("--models must not complete without AllowModels: %v", got)
	}
}

func TestRunHelp(t *testing.T) {
	def := FindAgent("claude")
	code, err := Run(def, []string{"-h"})
	if err != nil || code != 0 {
		t.Fatalf("help: code=%d err=%v", code, err)
	}
}

// The stability contract documents exactly 0 / 1 / 130 / 143, so a bad flag
// exits 1 like any other failure rather than inventing a usage code.
func TestRunBadFlag(t *testing.T) {
	def := FindAgent("opencode")
	code, err := Run(def, []string{"--model"})
	if err == nil || code != 1 {
		t.Fatalf("missing value should exit 1: code=%d err=%v", code, err)
	}
}

func TestFindAgentRegistry(t *testing.T) {
	for _, name := range []string{"claude", "codex", "opencode", "kilo", "kimi"} {
		if FindAgent(name) == nil {
			t.Fatalf("agent %s missing", name)
		}
	}
	if FindAgent("nope") != nil {
		t.Fatal("unknown agent should be nil")
	}
}
