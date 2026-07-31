package launch

import "testing"

// Run's dry-run path exercises the full wiring: argv parse → credential
// resolution (env key) → agent resolve → redacted print, without launching
// anything. Non-TTY test env skips the interactive prompt.
func TestRunDryRun(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "test-key")
	t.Setenv("ORQ_LAUNCH_NON_INTERACTIVE", "1")

	for _, name := range []string{"claude", "opencode", "kilo"} {
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

func TestRunHelp(t *testing.T) {
	def := FindAgent("claude")
	code, err := Run(def, []string{"-h"})
	if err != nil || code != 0 {
		t.Fatalf("help: code=%d err=%v", code, err)
	}
}

func TestRunBadFlag(t *testing.T) {
	def := FindAgent("opencode")
	code, err := Run(def, []string{"--model"})
	if err == nil || code != 2 {
		t.Fatalf("missing value should exit 2: code=%d err=%v", code, err)
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
