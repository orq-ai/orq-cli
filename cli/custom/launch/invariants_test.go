package launch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canaryKey = "sk-canary-8f3a1b9c-secret"

// TestCanaryKeyNeverLeaks pins the launch-wide secrets invariant for every
// registered agent: the API key may appear only as a whole env value (that is
// the delivery mechanism) — never inside argv, never inside a substring of a
// composed env value, and never in any file the resolver writes. A new agent
// fails this automatically if it embeds the key anywhere.
func TestCanaryKeyNeverLeaks(t *testing.T) {
	for _, def := range Agents() {
		def := def
		t.Run(def.Name, func(t *testing.T) {
			plan, err := def.Resolve(&AgentContext{
				Creds:  &Credentials{APIKey: canaryKey, APIBaseURL: DefaultGatewayAPIBaseURL},
				Getenv: env(nil),
				Flags:  GatewayFlags{},
				Fetch: func(_, _ string) ([]ModelInfo, error) {
					return []ModelInfo{{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000},
						{ID: "anthropic/claude-sonnet-4-6", ContextWindow: 200000, MaxOutputTokens: 64000}}, nil
				},
				ExecProbe: func(string, ...string) (string, error) {
					return "", io.EOF // catalog probes degrade to a warning
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Cleanup != nil {
				defer plan.Cleanup()
			}

			for _, arg := range plan.PreArgs {
				if strings.Contains(arg, canaryKey) {
					t.Fatalf("key leaked into argv: %q", arg)
				}
			}
			for k, v := range plan.Env {
				if v != canaryKey && strings.Contains(v, canaryKey) {
					t.Fatalf("key embedded in composed env %s=%q", k, v)
				}
			}
			for _, dir := range plan.TempDirs {
				err := filepath.Walk(dir.HostPath, func(path string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return err
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if strings.Contains(string(data), canaryKey) {
						t.Fatalf("key written to %s", path)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			// Every host path handed to the agent must be declared in
			// TempDirs — sandbox mode only delivers declared dirs, so an
			// undeclared path breaks exclusively inside the container.
			for _, arg := range plan.PreArgs {
				assertDeclaredIfPath(t, arg, plan.TempDirs)
			}
			for _, v := range plan.Env {
				assertDeclaredIfPath(t, v, plan.TempDirs)
			}
		})
	}
}

func assertDeclaredIfPath(t *testing.T, value string, dirs []TempDir) {
	t.Helper()
	if !filepath.IsAbs(value) {
		return
	}
	if _, err := os.Stat(value); err != nil {
		return // not an existing filesystem path, nothing to declare
	}
	for _, d := range dirs {
		if value == d.HostPath || strings.HasPrefix(value, d.HostPath+string(filepath.Separator)) {
			return
		}
	}
	t.Fatalf("host path %q not declared in TempDirs %v", value, dirs)
}

func TestParseLocalChoice(t *testing.T) {
	cases := map[string]localChoice{
		"o":         localOk,
		"OK":        localOk,
		"yes":       localOk,
		"\n":        localOk, // bare Enter accepts, by design (see comment)
		"s":         localSandbox,
		"Sandbox\n": localSandbox,
		"c":         localCancel,
		"no":        localCancel,
		"garbage":   localCancel,
	}
	for in, want := range cases {
		if got := parseLocalChoice(in); got != want {
			t.Fatalf("parseLocalChoice(%q) = %v, want %v", in, got, want)
		}
	}
}

// Regressions for the launcher-flag collision review findings: launcher flags
// are leading-only, values must not look like flags, prompt expansions land
// at the front, and `--` after agent args reaches the agent.
func TestParseArgvLeadingOnly(t *testing.T) {
	// codex's own --sandbox <mode> after a subcommand stays codex's.
	flags, rest, err := ParseArgv([]string{"exec", "--sandbox", "workspace-write", "do it"}, ParseArgvOptions{})
	if err != nil || flags.Sandbox {
		t.Fatalf("agent --sandbox consumed: %+v err=%v", flags, err)
	}
	if strings.Join(rest, " ") != "exec --sandbox workspace-write do it" {
		t.Fatalf("passthrough mangled: %v", rest)
	}

	// Leading launcher flags still work.
	flags, rest, err = ParseArgv([]string{"--sandbox", "--dry-run", "exec"}, ParseArgvOptions{})
	if err != nil || !flags.Sandbox || !flags.DryRun || strings.Join(rest, " ") != "exec" {
		t.Fatalf("leading flags: %+v rest=%v err=%v", flags, rest, err)
	}

	// A flag value may not itself be a flag.
	if _, _, err := ParseArgv([]string{"--model", "--dry-run"}, ParseArgvOptions{}); err == nil {
		t.Fatal("--model --dry-run must error, not swallow the flag")
	}
	if _, _, err := ParseArgv([]string{"--model="}, ParseArgvOptions{}); err == nil {
		t.Fatal("--model= must error")
	}

	// Prompt mapping expands at the front of the agent argv.
	opts := ParseArgvOptions{Prompt: &PromptMapping{
		Flags:  []string{"-p"},
		ToArgs: func(v string) []string { return []string{"run", v} },
	}}
	_, rest, err = ParseArgv([]string{"-p", "hi"}, opts)
	if err != nil || strings.Join(rest, " ") != "run hi" {
		t.Fatalf("prompt expansion: %v err=%v", rest, err)
	}

	// `--` after agent args is preserved for the agent.
	_, rest, err = ParseArgv([]string{"exec", "--", "--flag"}, ParseArgvOptions{})
	if err != nil || strings.Join(rest, " ") != "exec -- --flag" {
		t.Fatalf("agent's -- dropped: %v", rest)
	}
	// Leading -- delimits launcher flags and is consumed.
	flags, rest, err = ParseArgv([]string{"--", "--sandbox"}, ParseArgvOptions{})
	if err != nil || flags.Sandbox || strings.Join(rest, " ") != "--sandbox" {
		t.Fatalf("leading --: %+v %v", flags, rest)
	}
}

// TestCompletionFlagsMatchParser enforces the claim that the completion list
// mirrors ParseArgv: every advertised flag must be consumed when leading.
func TestCompletionFlagsMatchParser(t *testing.T) {
	def := FindAgent("opencode") // AllowModels + prompt mapping = fullest list
	for _, flag := range CompletionFlags(def, "-") {
		argv := []string{flag}
		switch flag {
		case "--model", "--models", "--base-url", "-p", "--prompt":
			argv = append(argv, "value")
		}
		flags, rest, err := ParseArgv(argv, ParseArgvOptions{
			AllowModels: def.AllowModels,
			Prompt:      def.Prompt,
		})
		if err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if flag == "-p" || flag == "--prompt" {
			continue // expands into passthrough by design
		}
		if len(rest) != 0 {
			t.Fatalf("advertised flag %s not consumed by ParseArgv (flags=%+v rest=%v)", flag, flags, rest)
		}
	}
}

// TestResolveExplicitModelsOffline covers the --no-fetch-models + --models
// path: the first explicit model becomes the gateway model.
func TestResolveExplicitModelsOffline(t *testing.T) {
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Flags.NoFetchModels = true
		in.Flags.Models = "openai/explicit-a, openai/explicit-b"
		in.DefaultModel = ""
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayModel != "openai/explicit-a" {
		t.Fatalf("explicit model not chosen: %s", got.GatewayModel)
	}
}

// TestLocalDryRunRedactsKey pins the redaction blocks: the canary key must
// not appear in dry-run stdout.
func TestLocalDryRunRedactsKey(t *testing.T) {
	t.Setenv("ORQ_API_KEY", canaryKey)
	t.Setenv("ORQ_LAUNCH_NON_INTERACTIVE", "1")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code, err := Run(FindAgent("claude"), []string{"--dry-run", "--no-fetch-models"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if err != nil || code != 0 {
		t.Fatalf("dry-run failed: %d %v", code, err)
	}
	if strings.Contains(string(out), canaryKey) {
		t.Fatalf("dry-run printed the API key:\n%s", out)
	}
	if !strings.Contains(string(out), "<redacted>") {
		t.Fatalf("expected redaction marker in output:\n%s", out)
	}
}
