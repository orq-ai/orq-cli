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
//
// Both flag states, because the MCP writers only run under --mcp: sweeping the
// zero value alone stopped exercising every MCP config the moment MCP became
// opt-in, and a key inlined there would have gone unnoticed.
func TestCanaryKeyNeverLeaks(t *testing.T) {
	// kimi now reaches skills.EnsureGeneration, which writes into the real
	// $HOME/.orq unless isolated: without this, a plain `go test` run leaves
	// a generation directory on the developer's (or CI runner's) machine and
	// makes this test's outcome depend on whatever is already there.
	t.Setenv("HOME", t.TempDir())

	for _, def := range Agents() {
		def := def
		for _, flags := range flagStates() {
			flags := flags
			t.Run(def.Name+flags.label, func(t *testing.T) {
				plan, err := def.Resolve(&AgentContext{
					Creds:  &Credentials{APIKey: canaryKey, APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
					Getenv: env(nil),
					Flags:  flags.GatewayFlags,
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
					var walkFn filepath.WalkFunc
					walkFn = func(path string, info os.FileInfo, err error) error {
						if err != nil {
							return err
						}
						// Session skills are symlinked into the temp dir (kimi,
						// pi): filepath.Walk does not follow symlinks itself, so
						// resolve one level and walk the real target instead —
						// otherwise a symlinked skill directory is neither
						// recursed into (Walk sees a non-directory) nor
						// readable as a file (it is one).
						if info.Mode()&os.ModeSymlink != 0 {
							target, evalErr := filepath.EvalSymlinks(path)
							if evalErr != nil {
								return evalErr
							}
							targetInfo, statErr := os.Lstat(target)
							if statErr != nil {
								return statErr
							}
							if targetInfo.IsDir() {
								return filepath.Walk(target, walkFn)
							}
							path, info = target, targetInfo
						}
						if info.IsDir() {
							return nil
						}
						// Kimi is the sole sanctioned exception: it resolves
						// provider credentials from config.toml only (no env
						// fallback or interpolation), so the key lives in a 0600
						// file inside a temp dir removed at session end. Scoped to
						// the home root specifically — a shipped skill also ships
						// its own unrelated config.toml under skills/.
						if def.Name == "kimi" && path == filepath.Join(dir.HostPath, "config.toml") {
							if info.Mode().Perm() != 0o600 {
								t.Fatalf("kimi config.toml must be 0600, got %v", info.Mode().Perm())
							}
							return nil
						}
						data, err := os.ReadFile(path)
						if err != nil {
							return err
						}
						if strings.Contains(string(data), canaryKey) {
							t.Fatalf("key written to %s", path)
						}
						return nil
					}
					if err := filepath.Walk(dir.HostPath, walkFn); err != nil {
						t.Fatal(err)
					}
				}

				// Every host path handed to the agent must be declared in
				// TempDirs — an undeclared dir is never cleaned up, and never
				// reported by --dry-run.
				for _, arg := range plan.PreArgs {
					assertDeclaredIfPath(t, arg, plan.TempDirs)
				}
				for _, v := range plan.Env {
					assertDeclaredIfPath(t, v, plan.TempDirs)
				}
			})
		}
	}
}

// flagStates are the states that change which writers run. MCP is opt-in, so
// its config writers execute only under --mcp.
func flagStates() []struct {
	GatewayFlags
	label string
} {
	return []struct {
		GatewayFlags
		label string
	}{
		{GatewayFlags{}, ""},
		{GatewayFlags{MCP: true}, "/--mcp"},
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

func TestParseArgvLeadingOnly(t *testing.T) {
	// codex's own -p <profile> after a subcommand stays codex's.
	flags, rest, err := ParseArgv([]string{"exec", "-p", "work", "do it"}, ParseArgvOptions{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.Join(rest, " ") != "exec -p work do it" {
		t.Fatalf("passthrough mangled: %v", rest)
	}

	// Leading launcher flags still work.
	flags, rest, err = ParseArgv([]string{"--mcp", "--dry-run", "exec"}, ParseArgvOptions{})
	if err != nil || !flags.MCP || !flags.DryRun || strings.Join(rest, " ") != "exec" {
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
	flags, rest, err = ParseArgv([]string{"--", "--dry-run"}, ParseArgvOptions{})
	if err != nil || flags.DryRun || strings.Join(rest, " ") != "--dry-run" {
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

// TestEveryAgentInheritsTheResolvedServer pins the other launch-wide env
// invariant: the server the parent resolved reaches every child as ORQ_SERVER.
// That env var, not a flag, is how a nested `orq` (and orqi) stays on the host
// its parent chose — a launcher that drops the line silently points its agent
// at the hosted service while the user asked for a self-hosted one.
func TestEveryAgentInheritsTheResolvedServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const server = "https://my.staging.orq.ai"

	for _, def := range Agents() {
		def := def
		t.Run(def.Name, func(t *testing.T) {
			plan, err := def.Resolve(&AgentContext{
				Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: server, Kind: CredentialAPIKey},
				Getenv: env(nil),
				Fetch: func(_, _ string) ([]ModelInfo, error) {
					return []ModelInfo{{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000}}, nil
				},
				ExecProbe: func(string, ...string) (string, error) { return "", io.EOF },
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Cleanup != nil {
				defer plan.Cleanup()
			}
			if got := plan.Env["ORQ_SERVER"]; got != server {
				t.Errorf("ORQ_SERVER = %q, want %q", got, server)
			}
		})
	}
}
