package launch

import (
	"io"
	"os"
	"reflect"
	"strings"
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

func TestCompletionFlags(t *testing.T) {
	def := FindAgent("opencode") // has AllowModels + a prompt mapping
	if got := CompletionFlags(def, "exec"); got != nil {
		t.Fatalf("non-flag input must complete nothing: %v", got)
	}
	got := CompletionFlags(def, "--mo")
	want := []string{"--model", "--models"}
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

// I4. The flag's help described the pre-branch world: a claude plugin gated on
// --mcp and a kimi-only session directory. It now governs session links for
// every agent uniformly, and it is the one place a launch user reads about
// skills at all.
func TestNoSkillsHelpDescribesWhatTheFlagNowDoes(t *testing.T) {
	out := captureStdout(t, func() { printAgentHelp(FindAgent("kimi")) })
	for _, stale := range []string{"plugin", "--mcp-gated", "independent of --mcp"} {
		if strings.Contains(out, stale) {
			t.Errorf("--no-skills help still describes the pre-branch world (%q):\n%s", stale, out)
		}
	}
	for _, want := range []string{"--no-skills", "removed", "orq connect skills"} {
		if !strings.Contains(out, want) {
			t.Errorf("--no-skills help never says %q:\n%s", want, out)
		}
	}
}

// captureStdout collects what fn prints, so a help string can be asserted on.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	w.Close()
	os.Stdout = prev
	return <-done
}
