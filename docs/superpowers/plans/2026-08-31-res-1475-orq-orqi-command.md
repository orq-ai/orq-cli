# `orq orqi` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One command, `orq orqi`, that runs the orqi assistant and installs it on first use via the orqi repo's own `install.sh`.

**Architecture:** A single hand-written cobra command in `cli/custom/commands/orqi.go`, registered with `DisableFlagParsing: true`. Because cobra then parses nothing, the command scans the front of argv for its own two flags (`-h/--help`, `--install`), exactly as `orq launch <agent>` does in `cli/custom/launch/args.go`, and hands everything after the first argument it does not own to orqi verbatim. orq's own globals — `--profile`, `--no-input` and the rest — are lifted off the line earlier, by `splitPassthroughGlobals` in `cli/custom/launchargs.go`, so the profile the child is told about and the token it inherits cannot disagree, and `--no-input` behaves as it does everywhere else in the CLI. It resolves the binary from PATH or the install directory, shells out to `curl` and `sh` to run the upstream installer when it is missing, and launches through `launch.RunChild` so the CLI's exit-code contract is unchanged.

**Tech Stack:** Go 1.x, cobra, `AlecAivazis/survey/v2`, the repo's own `cli/custom/launch` and `cli/custom/commands` packages. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-31-orq-orqi-command-design.md`

**Ticket:** RES-1475

## Global Constraints

- **New code goes in `cli/custom/`, never `cli/generated/`.** `bartolo generate` wipes the generated tree.
- **Everything under `cli/custom/` ships on both module lines** (root and `packages/orq-rc`, which reaches it through `replace orq => ../..`), so it must compile against both schemas. This command touches no schema types, so that is automatic — but do not import anything from `cli/generated/`.
- **Exit-code contract:** 0 ok, 1 command error, 130 SIGINT, 143 SIGTERM, and a launched child's own code propagated verbatim. `launch.RunChild` implements it; do not reimplement.
- **Prompts go to stderr** via `promptStdio()`, and every prompt is gated on `hasInteractiveTTY()` (both in `cli/custom/commands/prompts.go`). stdout belongs to the command's result, or in this case to the child process.
- **Installer URL:** `https://raw.githubusercontent.com/orq-ai/orqi/main/install.sh`
- **Installer one-liner shown to users:** `curl -fsSL https://raw.githubusercontent.com/orq-ai/orqi/main/install.sh | sh`
- **Supported platforms:** `darwin/arm64`, `darwin/amd64`, `linux/amd64`. Everything else is refused before any lookup, prompt or download.
- **Default install directory:** `$ORQI_INSTALL_DIR` if the user exported one, else `~/.local/bin` — `install.sh`'s own default.
- **What CI runs:** `go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go')`, plus `go run ./cmd/surface-dump -check`.

## File Structure

- **Create `cli/custom/commands/orqi.go`** — the whole command: argv scanner, completion list, platform gate, binary resolution, installer, prompt, launch, help text. It is one command with one job; splitting it across files would scatter four small private helpers that only this command calls.
- **Create `cli/custom/commands/orqi_test.go`** — tests for all of the above, using package-level seams.
- **Modify `cli/custom/groups.go`** — one line in `commandGroup`.
- **Modify `cli/custom/register.go`** — one line in `profileExemptCommands`, one line in `registerCommands`.
- **Modify `cli/custom/launchargs.go`** — generalise `splitLaunchGlobals` so `orqi` is a second command whose invocation has orq's own global flags lifted out of it before cobra dispatches. Without this, `--profile` on an `orqi` line is silently the wrong profile (Task 0).
- **Modify `cli/custom/run.go`** — the one call site follows the rename.
- **Modify `surface.json`** — regenerated, not hand-edited.
- **Modify `README.md`** — command table row and a short section.

---

### Task 0: Route orq's global flags on an orqi invocation

`DisableFlagParsing` means cobra parses nothing for this command — including the root's
persistent `--profile`. `installSessionPreRun` (`cli/custom/register.go`) still runs, still calls
`auth.ReadSession()` against whatever profile viper holds, and still does
`os.Setenv("ORQ_API_KEY", token)` in the parent process. With `--profile` unparsed, that is the
**default** profile's token, and `launch.RunChild` hands it to the child alongside any
`ORQ_PROFILE` we set. orqi's credential ladder puts `ORQ_API_KEY` first, so
`orq orqi --profile staging` silently runs against the default workspace whenever the default
profile is also logged in.

`cli/custom/launchargs.go` already solves exactly this, for `launch`, by lifting orq's globals
out of argv and parsing them onto the root before `Execute`. It is hard-coded to the literal
string `"launch"`. Generalising it to a small set of passthrough commands is the whole fix: the
profile is then resolved once, by the same PreRun chain every other command uses, and `orqi.go`
never handles `--profile` at all. It also hands the command every other root global for free —
`--no-input` most usefully, which is why `orqi.go` does not scan that either.

**The two commands terminate globals differently, and that is deliberate.** For `launch` the
terminator is the agent name: `orq launch kimi --profile staging` gives kimi both arguments,
because a launch line always has a positional between orq's flags and the agent's. An `orqi` line
has no such positional, so globals run until the first token orq does not own — which is the
prompt. `orq orqi --profile staging "why"` is orq's; `orq orqi "why" --profile staging` is orqi's.
That is the front-of-argv rule the spec already promised, and `leadingGlobals` needs no change to
produce it. Do not "fix" the asymmetry: making `orqi` behave like `launch` would mean requiring
`orq --profile staging orqi`, and the spec promises the other order works.

The cost is bounded and was checked against the real binary: on an `orqi` line, a front-position
flag whose name matches an orq root global (`--profile`, `--no-input`, `--json`, `--server`,
`--workspace`, `--verbose`, `--no-color`, `--raw`, `-o`, `-j`) is orq's, not orqi's. orqi's entire
flag surface is `--version/-v` and `--help/-h` (`/tmp/orqi-probe/src/main.ts:39-45`), and none of
those are root *persistent* flags — cobra puts `-h` and `-v` on `root.Flags()` — so nothing
collides today. Anything after the first prompt argument is orqi's regardless.

**Files:**
- Modify: `cli/custom/launchargs.go`
- Modify: `cli/custom/run.go`
- Test: `cli/custom/launchargs_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `var passthroughCommands = map[string]bool{"launch": true, "orqi": true}`; `splitLaunchGlobals` renamed to `splitPassthroughGlobals` with the same signature.

- [ ] **Step 1: Write the failing tests**

Append to `cli/custom/launchargs_test.go`, alongside the existing `TestSplitLaunchGlobals`:

```go
func TestSplitPassthroughGlobalsOnOrqi(t *testing.T) {
	root := testRoot()
	for _, tc := range []struct {
		name    string
		args    []string
		globals []string
		rest    []string
	}{
		{
			// The whole point: without this the token injected into
			// ORQ_API_KEY is the default profile's, and it beats ORQ_PROFILE.
			name:    "after the command word",
			args:    []string{"orqi", "--profile", "staging", "why did it fail?"},
			globals: []string{"--profile", "staging"},
			rest:    []string{"orqi", "why did it fail?"},
		},
		{
			name:    "before the command word",
			args:    []string{"--profile", "staging", "orqi"},
			globals: []string{"--profile", "staging"},
			rest:    []string{"orqi"},
		},
		{
			// Same front-of-argv rule launch uses: past the first argument
			// orq does not own, every flag is the child's.
			name:    "after a passthrough argument",
			args:    []string{"orqi", "why did it fail?", "--profile", "staging"},
			globals: nil,
			rest:    []string{"orqi", "why did it fail?", "--profile", "staging"},
		},
		{
			// Unlike launch, there is no agent name to terminate the globals,
			// so several run until the prompt.
			name:    "several globals, then the prompt",
			args:    []string{"orqi", "--no-input", "--profile", "staging", "why did it fail?"},
			globals: []string{"--no-input", "--profile", "staging"},
			rest:    []string{"orqi", "why did it fail?"},
		},
		{
			// -h and -v live on root.Flags(), not PersistentFlags(), so they
			// are never lifted: `orq orqi -h` reaches the command intact.
			name:    "the help shorthand is not a global",
			args:    []string{"orqi", "-h"},
			globals: nil,
			rest:    []string{"orqi", "-h"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globals, rest := splitPassthroughGlobals(root, tc.args)
			if !slices.Equal(globals, tc.globals) {
				t.Errorf("globals = %v, want %v", globals, tc.globals)
			}
			if !slices.Equal(rest, tc.rest) {
				t.Errorf("rest = %v, want %v", rest, tc.rest)
			}
		})
	}
}
```

`testRoot` is whatever the existing `TestSplitLaunchGlobals` uses to build a root with orq's
persistent flags registered; reuse it rather than building a second one. Add `"slices"` to the
test file's imports if it is not already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/ -run TestSplitPassthroughGlobals -v`
Expected: FAIL to build, with `undefined: splitPassthroughGlobals`.

- [ ] **Step 3: Generalise the splitter**

In `cli/custom/launchargs.go`:

```go
// passthroughCommands run with cobra's DisableFlagParsing, so cobra parses no
// flags at all for them — not even the root's own. Each needs its globals
// lifted out of argv here instead.
var passthroughCommands = map[string]bool{
	"launch": true,
	"orqi":   true,
}
```

Rename `splitLaunchGlobals` to `splitPassthroughGlobals` and `launch` (the index variable) to
`cmdIndex`, replace the `args[i] == "launch"` test in `leadingGlobals` with
`passthroughCommands[args[i]]`, and return the matched word so the caller can rebuild argv:

```go
func splitPassthroughGlobals(root *cobra.Command, args []string) (globals, rest []string) {
	globals, cmdIndex := leadingGlobals(root, args)
	if cmdIndex < 0 {
		return nil, args
	}
	name := args[cmdIndex]
	i := cmdIndex + 1
	...unchanged...
	rest = append(rest, name)
	return globals, append(rest, args[i:]...)
}
```

Update the doc comments on both functions: they are no longer about `launch` specifically, and
the reason the rule exists — a child flag that collides with one of ours must still reach the
child — is the same for orqi.

In `cli/custom/run.go`, follow the rename at the one call site and update its comment.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/ -run 'TestSplitLaunchGlobals|TestSplitPassthroughGlobals' -v`
Expected: PASS. The existing launch cases must still pass unchanged — this is a generalisation,
not a behaviour change for `launch`.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/launchargs.go cli/custom/launchargs_test.go cli/custom/run.go
git commit -m "refactor(launch): route orq globals for any passthrough command"
```

---

### Task 1: Argv scanner and completion list

The command owns two flags of its own. `--profile` and `--no-input` are orq's own root flags,
lifted off the line by Task 0 and parsed onto the root like they are for every other command, so
this scanner never sees them. Cobra parses none of what is left, so this task builds the scanner that does, plus the completion
list that must stay in step with it.

**Files:**
- Create: `cli/custom/commands/orqi.go`
- Test: `cli/custom/commands/orqi_test.go`

**Interfaces:**
- Consumes: Task 0's routing (not as code — `--profile` simply never reaches this scanner).
- Produces: `type orqiFlags struct { Help, Install bool }`; `func parseOrqiArgv(argv []string) (orqiFlags, []string, error)` returning flags, the passthrough argv, and an error; `func orqiCompletionFlags(toComplete string) []string`; `var orqiFlagNames = []string{"-h", "--help", "--install"}`; `var orqiGlobalFlagNames = []string{"--no-input", "--profile"}`.

- [ ] **Step 1: Write the failing tests**

Create `cli/custom/commands/orqi_test.go`:

```go
package commands

import (
	"strings"
	"testing"
)

func TestParseOrqiArgvStopsAtFirstUnownedArg(t *testing.T) {
	// --install after a passthrough argument is orqi's, not ours: the same
	// rule that keeps orqi's own future flags reachable.
	flags, rest, err := parseOrqiArgv([]string{"why did it fail?", "--install"})
	if err != nil {
		t.Fatalf("parse error = %v, want nil", err)
	}
	if flags.Install {
		t.Error("install = true, want false: it came after an orqi argument")
	}
	if len(rest) != 2 {
		t.Errorf("rest = %v, want both args", rest)
	}
}

func TestParseOrqiArgvDoubleDashEndsScanning(t *testing.T) {
	flags, rest, err := parseOrqiArgv([]string{"--", "--install"})
	if err != nil {
		t.Fatalf("parse error = %v, want nil", err)
	}
	if flags.Install {
		t.Error("install = true, want false: --install after -- is orqi's")
	}
	if len(rest) != 1 || rest[0] != "--install" {
		t.Errorf("rest = %v, want [--install]", rest)
	}
}

func TestParseOrqiArgvInstallIsTerminal(t *testing.T) {
	_, _, err := parseOrqiArgv([]string{"--install", "extra"})
	if err == nil || !strings.Contains(err.Error(), "--install") {
		t.Fatalf("error = %v, want one naming --install", err)
	}
}

func TestOrqiCompletionFlagsMatchParser(t *testing.T) {
	// orqiGlobalFlagNames are deliberately absent here: Task 0 lifts them out
	// before this scanner ever sees them. TestSplitPassthroughGlobalsOnOrqi in
	// cli/custom is what proves those reach the right place.
	for _, name := range orqiFlagNames {
		argv := []string{name}
		flags, _, err := parseOrqiArgv(argv)
		if err != nil {
			t.Fatalf("parseOrqiArgv(%v) error = %v, want the flag consumed", argv, err)
		}
		if flags == (orqiFlags{}) {
			t.Errorf("%s is advertised for completion but sets nothing in the parser", name)
		}
	}
	if got := orqiCompletionFlags("--in"); len(got) != 1 || got[0] != "--install" {
		t.Errorf("orqiCompletionFlags(--in) = %v, want [--install]", got)
	}
	if got := orqiCompletionFlags("why"); got != nil {
		t.Errorf("orqiCompletionFlags(why) = %v, want nil: non-flag input belongs to orqi", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/commands/ -run TestParseOrqiArgv -v`
Expected: FAIL to build, with `undefined: parseOrqiArgv`.

- [ ] **Step 3: Write the scanner**

Create `cli/custom/commands/orqi.go`:

```go
package commands

import (
	"fmt"
	"strings"
)

// orqiFlags are the flags this command owns on `orq orqi`. Everything else is
// orqi's, except orq's own global --profile, which splitPassthroughGlobals
// (cli/custom/launchargs.go) parses onto the root before cobra dispatches.
type orqiFlags struct {
	Help    bool
	Install bool
}

// orqiFlagNames mirrors what parseOrqiArgv consumes;
// TestOrqiCompletionFlagsMatchParser asserts the two agree.
var orqiFlagNames = []string{"-h", "--help", "--install"}

// orqiGlobalFlagNames are orq's own root flags, which work on an orqi line
// because splitPassthroughGlobals lifts them out of argv before cobra
// dispatches. Offered for completion; never seen by parseOrqiArgv.
var orqiGlobalFlagNames = []string{"--no-input", "--profile"}

// parseOrqiArgv recognizes orq's own flags only at the FRONT of argv: the
// first argument orq does not own ends parsing and everything from there
// belongs to orqi verbatim, so a flag orqi grows later can never collide with
// one of ours. A leading `--` ends parsing explicitly. Same convention as
// launch.ParseArgv (cli/custom/launch/args.go), whose flag set and
// GatewayFlags return are gateway-specific and so not reusable here.
//
// cobra parses none of this: `orq orqi` runs with DisableFlagParsing, which
// leaves even the root's persistent --profile unparsed. It arrives at the
// front of argv, which is where this scanner reads it.
func parseOrqiArgv(argv []string) (orqiFlags, []string, error) {
	var flags orqiFlags
	i := 0
scan:
	for ; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			i++
			break scan
		case arg == "-h" || arg == "--help":
			flags.Help = true
		case arg == "--install":
			flags.Install = true
		default:
			break scan
		}
	}
	rest := argv[i:]
	// --install starts no session, so there is no child for trailing
	// arguments to belong to. Refusing beats dropping them silently.
	if flags.Install && len(rest) > 0 {
		return flags, nil, fmt.Errorf("--install takes no arguments, got %q", strings.Join(rest, " "))
	}
	return flags, rest, nil
}

// orqiCompletionFlags returns orq's own flags matching toComplete. Cobra
// cannot enumerate them itself with flag parsing disabled. Anything that does
// not look like a flag belongs to orqi's own CLI. orq's globals are offered
// too, even though this file does not parse them: on an orqi line they work.
func orqiCompletionFlags(toComplete string) []string {
	if !strings.HasPrefix(toComplete, "-") {
		return nil
	}
	var out []string
	for _, f := range append(append([]string{}, orqiFlagNames...), orqiGlobalFlagNames...) {
		if strings.HasPrefix(f, toComplete) {
			out = append(out, f)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run 'TestParseOrqiArgv|TestOrqiCompletion' -v`
Expected: PASS, seven tests.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/orqi.go cli/custom/commands/orqi_test.go
git commit -m "feat(orqi): scan orq-owned flags at the front of argv"
```

---

### Task 2: Platform gate and binary resolution

`install.sh` writes to `~/.local/bin` and only prints a PATH hint. A `LookPath`-only design would therefore report "not installed" for a machine where orqi plainly is installed, and re-offer the install on every run. Resolution checks PATH, then the install directory on disk.

**Files:**
- Modify: `cli/custom/commands/orqi.go`
- Test: `cli/custom/commands/orqi_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `func orqiInstallDir() string`; `func resolveOrqi() string` returning an absolute path or `""`; `func orqiPlatformSupported() bool`; seams `var orqiLookPath = exec.LookPath` and `var orqiPlatform = func() string { return runtime.GOOS + "/" + runtime.GOARCH }`.

- [ ] **Step 1: Write the failing tests**

Append to `cli/custom/commands/orqi_test.go`:

```go
// orqiFakeLookPath answers for "orqi" only, so a test that hides the binary
// does not also hide curl and sh from the installer's preflight.
func orqiFakeLookPath(t *testing.T, path string, err error) {
	orqiFakeLookPathFunc(t, func(name string) (string, error) {
		if name == "orqi" {
			return path, err
		}
		return exec.LookPath(name)
	})
}

// orqiFakeLookPathFunc swaps the PATH lookup wholesale, for tests that need a
// different answer per binary.
func orqiFakeLookPathFunc(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := orqiLookPath
	t.Cleanup(func() { orqiLookPath = orig })
	orqiLookPath = fn
}

func TestResolveOrqiPrefersPath(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	if got := resolveOrqi(); got != "/usr/local/bin/orqi" {
		t.Errorf("resolveOrqi() = %q, want /usr/local/bin/orqi", got)
	}
}

func TestResolveOrqiFindsInstallDirWhenNotOnPath(t *testing.T) {
	// install.sh only prints a PATH hint, so an installed orqi is routinely
	// invisible to LookPath. Missing this is a reinstall loop.
	dir := t.TempDir()
	binary := filepath.Join(dir, "orqi")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQI_INSTALL_DIR", dir)
	orqiFakeLookPath(t, "", errors.New("not found"))
	if got := resolveOrqi(); got != binary {
		t.Errorf("resolveOrqi() = %q, want %q", got, binary)
	}
}

func TestResolveOrqiEmptyWhenAbsent(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	if got := resolveOrqi(); got != "" {
		t.Errorf("resolveOrqi() = %q, want empty", got)
	}
}

func TestOrqiPlatformSupported(t *testing.T) {
	orig := orqiPlatform
	t.Cleanup(func() { orqiPlatform = orig })
	for platform, want := range map[string]bool{
		"darwin/arm64":  true,
		"darwin/amd64":  true,
		"linux/amd64":   true,
		"linux/arm64":   false,
		"windows/amd64": false,
	} {
		orqiPlatform = func() string { return platform }
		if got := orqiPlatformSupported(); got != want {
			t.Errorf("orqiPlatformSupported() on %s = %v, want %v", platform, got, want)
		}
	}
}
```

Add `"errors"`, `"os"`, `"os/exec"`, `"path/filepath"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/commands/ -run 'TestResolveOrqi|TestOrqiPlatform' -v`
Expected: FAIL to build, with `undefined: orqiLookPath`.

- [ ] **Step 3: Write the resolution code**

Append to `cli/custom/commands/orqi.go`, and add `"os"`, `"os/exec"`, `"path/filepath"`, `"runtime"` to its imports:

```go
// Seams. Tests answer these instead of touching the real PATH or GOOS.
var (
	orqiLookPath = exec.LookPath
	orqiPlatform = func() string { return runtime.GOOS + "/" + runtime.GOARCH }
)

// orqiPlatforms is what the orqi release publishes. Linux arm64 is refused as
// early as Windows: install.sh would reject it too, but only after a prompt
// and a download.
var orqiPlatforms = map[string]bool{
	"darwin/arm64": true,
	"darwin/amd64": true,
	"linux/amd64":  true,
}

func orqiPlatformSupported() bool { return orqiPlatforms[orqiPlatform()] }

// orqiInstallDir is where install.sh will put the binary: the user's own
// ORQI_INSTALL_DIR, or install.sh's default.
func orqiInstallDir() string {
	if dir := strings.TrimSpace(os.Getenv("ORQI_INSTALL_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

// resolveOrqi returns the orqi binary's path, or "" when there is none.
// PATH is not enough on its own: install.sh writes to ~/.local/bin and only
// prints a hint about it, so a freshly installed orqi is invisible to
// LookPath until the user acts on that hint or opens a new shell.
func resolveOrqi() string {
	if path, err := orqiLookPath("orqi"); err == nil {
		return path
	}
	dir := orqiInstallDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "orqi")
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run 'TestResolveOrqi|TestOrqiPlatform' -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/orqi.go cli/custom/commands/orqi_test.go
git commit -m "feat(orqi): resolve the binary from PATH or the install dir"
```

---

### Task 3: The installer

Mirrors `updateViaInstaller` in `cli/custom/commands/update.go`: preflight `curl` and `sh`, download to a temp dir, run the script. Read that function before writing this one — its comment already records why the download is not piped into a shell.

**Files:**
- Modify: `cli/custom/commands/orqi.go`
- Test: `cli/custom/commands/orqi_test.go`

**Interfaces:**
- Consumes: `orqiInstallDir()` and the `orqiLookPath` seam from Task 2.
- Produces: `func installOrqi(ctx context.Context, dir string) error`; seam `var runOrqiCommand func(ctx context.Context, env map[string]string, name string, args ...string) error`; constants `orqiInstallerURL`, `orqiInstallerCmd`.

- [ ] **Step 1: Write the failing tests**

Append to `cli/custom/commands/orqi_test.go`:

```go
// orqiFakeRunner captures what would have been executed, with the env each
// command was given, and returns errs[n] for the nth call.
func orqiFakeRunner(t *testing.T, errs ...error) *[]string {
	t.Helper()
	orig := runOrqiCommand
	t.Cleanup(func() { runOrqiCommand = orig })
	var ran []string
	runOrqiCommand = func(_ context.Context, env map[string]string, name string, args ...string) error {
		line := strings.Join(append([]string{name}, args...), " ")
		if dir, ok := env["ORQI_INSTALL_DIR"]; ok {
			line += " [ORQI_INSTALL_DIR=" + dir + "]"
		}
		ran = append(ran, line)
		if len(ran) <= len(errs) {
			return errs[len(ran)-1]
		}
		return nil
	}
	return &ran
}

func TestInstallOrqiDownloadsThenRuns(t *testing.T) {
	ran := orqiFakeRunner(t)
	if err := installOrqi(context.Background(), "/opt/bin"); err != nil {
		t.Fatalf("installOrqi error = %v, want nil", err)
	}
	if len(*ran) != 2 {
		t.Fatalf("ran %v, want a curl and an sh", *ran)
	}
	if !strings.HasPrefix((*ran)[0], "curl ") || !strings.Contains((*ran)[0], orqiInstallerURL) {
		t.Errorf("first command = %q, want a curl of the installer", (*ran)[0])
	}
	if !strings.HasPrefix((*ran)[1], "sh ") || !strings.Contains((*ran)[1], "[ORQI_INSTALL_DIR=/opt/bin]") {
		t.Errorf("second command = %q, want sh with the install dir set", (*ran)[1])
	}
}

func TestInstallOrqiReportsDownloadFailure(t *testing.T) {
	ran := orqiFakeRunner(t, errors.New("curl: (22) 404"))
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), orqiInstallerURL) {
		t.Fatalf("error = %v, want one naming the installer URL", err)
	}
	if len(*ran) != 1 {
		t.Errorf("ran %v, want the installer never to run after a failed download", *ran)
	}
}

func TestInstallOrqiReportsInstallerFailure(t *testing.T) {
	orqiFakeRunner(t, nil, errors.New("exit status 1"))
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), orqiInstallerCmd) {
		t.Fatalf("error = %v, want one showing the manual one-liner", err)
	}
}

func TestInstallOrqiRequiresCurlAndSh(t *testing.T) {
	orqiFakeLookPathFunc(t, func(name string) (string, error) {
		if name == "curl" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	})
	ran := orqiFakeRunner(t)
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), "curl") {
		t.Fatalf("error = %v, want one naming curl", err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want nothing fetched when the preflight fails", *ran)
	}
}

func TestInstallOrqiRemovesItsTempDir(t *testing.T) {
	var scriptPath string
	orig := runOrqiCommand
	t.Cleanup(func() { runOrqiCommand = orig })
	runOrqiCommand = func(_ context.Context, _ map[string]string, name string, args ...string) error {
		if name == "curl" {
			scriptPath = args[len(args)-2] // -o <path> <url>
			return os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o600)
		}
		return errors.New("exit status 1")
	}
	if err := installOrqi(context.Background(), "/opt/bin"); err == nil {
		t.Fatal("installOrqi error = nil, want the installer failure")
	}
	if scriptPath == "" {
		t.Fatal("curl was never called")
	}
	if _, err := os.Stat(filepath.Dir(scriptPath)); !os.IsNotExist(err) {
		t.Errorf("temp dir still present after a failed install: %v", err)
	}
}
```

Add `"context"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/commands/ -run TestInstallOrqi -v`
Expected: FAIL to build, with `undefined: installOrqi`.

- [ ] **Step 3: Write the installer**

Append to `cli/custom/commands/orqi.go`, adding `"context"` and `bartolocli "github.com/orq-ai/bartolo/cli"` and `"orq/cli/custom/launch"` to its imports:

```go
const (
	orqiInstallerURL = "https://raw.githubusercontent.com/orq-ai/orqi/main/install.sh"
	orqiInstallerCmd = "curl -fsSL " + orqiInstallerURL + " | sh"
)

// runOrqiCommand is the seam tests replace so they never run curl or the real
// installer. Child output goes to stderr: it is the installer's diagnostics,
// while stdout belongs to orqi itself once it starts.
var runOrqiCommand = func(ctx context.Context, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = bartolocli.Stderr, bartolocli.Stderr
	cmd.Env = launch.MergeEnv(os.Environ(), env)
	return cmd.Run()
}

// installOrqi runs the orqi repo's own install.sh, which resolves the release,
// downloads the right tarball, sheds macOS quarantine by extracting it, and
// verifies the result by running `orqi --version`. Reimplementing that here
// would be a second copy of a path that has to stay in step with the orqi
// release layout. Downloaded to a file and run in two steps rather than
// `curl | sh`, for the reason updateViaInstaller records in update.go.
//
// No timeout: the installer downloads ~25 MB and the user is watching it. The
// command's own context still carries Ctrl-C.
func installOrqi(ctx context.Context, dir string) error {
	for _, bin := range []string{"curl", "sh"} {
		if _, err := orqiLookPath(bin); err != nil {
			return fmt.Errorf("installing orqi needs %s, which is not on PATH. Install it, or run:\n  %s", bin, orqiInstallerCmd)
		}
	}
	tmp, err := os.MkdirTemp("", "orq-orqi-")
	if err != nil {
		return fmt.Errorf("cannot create a temporary directory for the installer: %w", err)
	}
	defer os.RemoveAll(tmp)

	script := filepath.Join(tmp, "install.sh")
	if err := runOrqiCommand(ctx, nil, "curl", "-fsSL", "-o", script, orqiInstallerURL); err != nil {
		return fmt.Errorf("cannot download the orqi installer from %s: %w", orqiInstallerURL, err)
	}
	if err := runOrqiCommand(ctx, map[string]string{"ORQI_INSTALL_DIR": dir}, "sh", script); err != nil {
		return fmt.Errorf("the orqi installer failed: %w\nRun it yourself to see the full output:\n  %s", err, orqiInstallerCmd)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run TestInstallOrqi -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/orqi.go cli/custom/commands/orqi_test.go
git commit -m "feat(orqi): run the upstream install.sh from a temp dir"
```

---

### Task 4: The command, and registration

Ties the previous three together: gate the platform, resolve, prompt, install, launch. Then register it so it appears in the tree.

**Files:**
- Modify: `cli/custom/commands/orqi.go`
- Modify: `cli/custom/groups.go`
- Modify: `cli/custom/register.go`
- Modify: `surface.json` (regenerated)
- Test: `cli/custom/commands/orqi_test.go`

**Interfaces:**
- Consumes: `parseOrqiArgv`, `orqiCompletionFlags` (Task 1); `resolveOrqi`, `orqiInstallDir`, `orqiPlatformSupported` (Task 2); `installOrqi` (Task 3).
- Produces: `func NewOrqiCommand() *cobra.Command`; `func runOrqi(cmd *cobra.Command, argv []string) (int, error)` returning the child's exit code; `func printOrqiHelp()`; seams `var orqiConfirm func(message string) bool`, `var orqiInteractive = hasInteractiveTTY` and `var runOrqiChild = launch.RunChild`.

- [ ] **Step 1: Write the failing tests**

Append to `cli/custom/commands/orqi_test.go`:

```go
// orqiFakeChild captures the launch instead of executing a real orqi.
func orqiFakeChild(t *testing.T, code int) (*string, *[]string, *map[string]string) {
	t.Helper()
	orig := runOrqiChild
	t.Cleanup(func() { runOrqiChild = orig })
	var binary string
	var args []string
	var env map[string]string
	runOrqiChild = func(b string, a []string, e map[string]string) (int, error) {
		binary, args, env = b, a, e
		return code, nil
	}
	return &binary, &args, &env
}

// orqiFakeConfirm answers the install prompt without a terminal.
func orqiFakeConfirm(t *testing.T, answer bool) *int {
	t.Helper()
	orig := orqiConfirm
	t.Cleanup(func() { orqiConfirm = orig })
	asked := 0
	orqiConfirm = func(string) bool {
		asked++
		return answer
	}
	return &asked
}

// orqiFakeInteractive decides whether runOrqi believes it has a terminal.
// `go test` never has one on stdin and stdout, so without this seam every
// prompt path is unreachable from a test.
func orqiFakeInteractive(t *testing.T, interactive bool) {
	t.Helper()
	orig := orqiInteractive
	t.Cleanup(func() { orqiInteractive = orig })
	orqiInteractive = func() bool { return interactive }
}

func orqiTestRoot(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "orq"}
	// The real root's --profile, which Task 0 parses before dispatch and
	// runOrqi reads back off the root. Without it Lookup returns nil.
	root.PersistentFlags().String("profile", "", "credentials profile")
	// cobra backfills the context in Execute, which these tests bypass; a nil
	// context panics exec.CommandContext inside the installer.
	root.SetContext(context.Background())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root
}

func runOrqiArgs(t *testing.T, argv ...string) (int, error) {
	t.Helper()
	return runOrqi(orqiTestRoot(t), argv)
}

func TestRunOrqiPassesArgumentsThrough(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	binary, args, _ := orqiFakeChild(t, 0)
	ran := orqiFakeRunner(t)
	if _, err := runOrqiArgs(t, "why did it fail?", "--version"); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *binary != "/usr/local/bin/orqi" {
		t.Errorf("binary = %q, want the resolved path", *binary)
	}
	if len(*args) != 2 || (*args)[0] != "why did it fail?" || (*args)[1] != "--version" {
		t.Errorf("args = %v, want both passed through verbatim", *args)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want no install for a binary already present", *ran)
	}
}

func TestRunOrqiPropagatesProfile(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	_, _, env := orqiFakeChild(t, 0)
	// As Task 0 leaves it: parsed onto the root, not sitting in argv.
	root := orqiTestRoot(t)
	if err := root.PersistentFlags().Set("profile", "staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := runOrqi(root, nil); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if (*env)["ORQ_PROFILE"] != "staging" {
		t.Errorf("env = %v, want ORQ_PROFILE=staging", *env)
	}
}

func TestRunOrqiLeavesProfileUnsetByDefault(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	_, _, env := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if _, ok := (*env)["ORQ_PROFILE"]; ok {
		t.Errorf("env = %v, want ORQ_PROFILE untouched so orqi resolves it itself", *env)
	}
}

func TestRunOrqiPropagatesExitCode(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	orqiFakeChild(t, 42)
	code, err := runOrqiArgs(t)
	if err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if code != 42 {
		t.Errorf("code = %d, want the child's 42", code)
	}
}

func TestRunOrqiInstallsAfterConfirmation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ORQI_INSTALL_DIR", dir)
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *asked != 1 {
		t.Errorf("prompted %d times, want 1", *asked)
	}
	if len(*ran) != 2 {
		t.Errorf("ran %v, want a download and an install", *ran)
	}
	if *binary != filepath.Join(dir, "orqi") {
		t.Errorf("binary = %q, want the just-installed path, not a bare lookup", *binary)
	}
}

func TestRunOrqiDeclinedInstallDoesNothing(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	orqiFakeConfirm(t, false)
	ran := orqiFakeRunner(t)
	code, err := runOrqiArgs(t)
	if err != nil || code != 0 {
		t.Fatalf("runOrqi = (%d, %v), want (0, nil)", code, err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want nothing", *ran)
	}
}

// --no-input reaches this through hasInteractiveTTY's viper read, exactly as
// it does for every other prompt in the CLI; TestHasInteractiveTTYHonorsNoInput
// covers that half. Here the seam stands in for "no terminal".
func TestRunOrqiRefusesWhenNotInteractive(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, false)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	_, err := runOrqiArgs(t)
	if err == nil || !strings.Contains(err.Error(), orqiInstallerCmd) {
		t.Fatalf("error = %v, want one showing the install one-liner", err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want neither", *asked, *ran)
	}
}

func TestRunOrqiInstallFlagUsesTheExistingBinarysDir(t *testing.T) {
	// Installing into ~/.local/bin regardless would fork a second copy for
	// anyone whose orqi came from source or a package manager.
	orqiFakeLookPath(t, "/opt/homebrew/bin/orqi", nil)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t, "--install"); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if len(*ran) != 2 || !strings.Contains((*ran)[1], "[ORQI_INSTALL_DIR=/opt/homebrew/bin]") {
		t.Errorf("ran %v, want the install to target the existing binary's dir", *ran)
	}
	if *binary != "" {
		t.Errorf("started %q, want --install to start no session", *binary)
	}
}

func TestRunOrqiRunsTheInstallDirBinaryWithoutPrompting(t *testing.T) {
	// install.sh only prints a PATH hint. A LookPath-only design would prompt
	// to reinstall on every run for anyone who has not acted on it.
	dir := t.TempDir()
	t.Setenv("ORQI_INSTALL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "orqi"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *binary != filepath.Join(dir, "orqi") {
		t.Errorf("binary = %q, want the one already in the install dir", *binary)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want neither", *asked, *ran)
	}
}

func TestRunOrqiFailedInstallStartsNoSession(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	orqiFakeConfirm(t, true)
	orqiFakeRunner(t, nil, errors.New("exit status 1"))
	binary, _, _ := orqiFakeChild(t, 0)
	code, err := runOrqiArgs(t)
	if err == nil || code != 1 {
		t.Fatalf("runOrqi = (%d, %v), want (1, an installer error)", code, err)
	}
	if *binary != "" {
		t.Errorf("started %q, want no session after a failed install", *binary)
	}
}

func TestRunOrqiHelpNeverInstalls(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	code, err := runOrqiArgs(t, "--help")
	if err != nil || code != 0 {
		t.Fatalf("runOrqi = (%d, %v), want (0, nil)", code, err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want help to be free", *asked, *ran)
	}
}

// hasInteractiveTTY is the CLI's one prompt gate and had no test at all; the
// orqi command's --no-input promise now rests on this branch.
func TestHasInteractiveTTYHonorsNoInput(t *testing.T) {
	viper.Set("no-input", true)
	t.Cleanup(func() { viper.Set("no-input", false) })
	if hasInteractiveTTY() {
		t.Error("hasInteractiveTTY() = true under --no-input, want false")
	}
}

func TestOrqiHelpListsEveryWrapperFlag(t *testing.T) {
	// cobra cannot enumerate them: DisableFlagParsing means it never sees any
	// of them, so the help text is the only place they are discoverable.
	var out bytes.Buffer
	orig := bartolocli.Stderr
	t.Cleanup(func() { bartolocli.Stderr = orig })
	bartolocli.Stderr = &out
	printOrqiHelp()
	for _, flag := range append(append([]string{}, orqiFlagNames...), orqiGlobalFlagNames...) {
		if !strings.Contains(out.String(), flag) {
			t.Errorf("help does not mention %s:\n%s", flag, out.String())
		}
	}
}

func TestRunOrqiRefusesUnsupportedPlatform(t *testing.T) {
	orig := orqiPlatform
	t.Cleanup(func() { orqiPlatform = orig })
	orqiPlatform = func() string { return "windows/amd64" }
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	_, err := runOrqiArgs(t)
	if err == nil || !strings.Contains(err.Error(), "windows/amd64") {
		t.Fatalf("error = %v, want one naming the platform", err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want the refusal to come first", *asked, *ran)
	}
}
```

Add `"bytes"`, `bartolocli "github.com/orq-ai/bartolo/cli"`, `"github.com/spf13/cobra"` and `"github.com/spf13/viper"` to the test file's imports. `"os/exec"` is already there from Task 2.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/commands/ -run 'TestRunOrqi|TestOrqiHelp' -v`
Expected: FAIL to build, with `undefined: runOrqi`.

- [ ] **Step 3: Write the command**

Append to `cli/custom/commands/orqi.go`, adding `survey "github.com/AlecAivazis/survey/v2"` and `"github.com/spf13/cobra"` to its imports:

```go
// orqiConfirm and runOrqiChild are seams; tests answer the prompt and capture
// the launch instead of drawing a terminal or executing a real orqi.
var (
	orqiInteractive = hasInteractiveTTY
	orqiConfirm     = func(message string) bool {
		answer := true
		if err := survey.AskOne(&survey.Confirm{Message: message, Default: true}, &answer, promptStdio()); err != nil {
			return false
		}
		return answer
	}
	runOrqiChild = launch.RunChild
)

// printOrqiHelp writes the help cobra cannot: DisableFlagParsing means the
// wrapper's own flags are never registered, so cmd.Help() would advertise none
// of them. launch/run.go's printAgentHelp exists for the same reason.
func printOrqiHelp() {
	fmt.Fprintf(bartolocli.Stderr, `Run orqi, the orq.ai assistant in your terminal, installing it first if it is missing.

orqi reads the login session this CLI maintains, so 'orq auth login' (or a
valid ORQ_API_KEY) is all the setup it needs.

Usage:
  orq orqi [flags] [--] [prompt or orqi arguments...]

Flags:
  -h, --help            Print this help and exit; never installs anything
      --install         Install or reinstall orqi, then exit without starting a session
      --no-input        Never prompt; fail instead of offering to install (orq global)
      --profile <name>  The login profile orqi should use (orq global)

These flags are recognised only before the first argument orq does not own.
Everything from that argument on is passed to orqi untouched, so
'orq orqi "why did it fail" --install' sends --install to orqi. The two orq
globals also work before the command word: 'orq --profile staging orqi'.
`)
}

func NewOrqiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "orqi [flags] [--] [prompt or orqi arguments...]",
		Short: "Run orqi, the orq.ai assistant, installing it on first use",
		Long: `Run orqi, the orq.ai assistant in your terminal, installing it first if it is missing.

orqi reads the login session this CLI maintains, so 'orq auth login' (or a
valid ORQ_API_KEY) is all the setup it needs. Everything after the first
argument orq does not own is passed to orqi untouched.`,
		// Disabled so orqi's own flags reach it; parseOrqiArgv reads ours.
		DisableFlagParsing: true,
		ValidArgsFunction: func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if comps := orqiCompletionFlags(toComplete); comps != nil {
				return comps, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		// cobra's own help would list none of the wrapper's flags.
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runOrqi(cmd, args)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

func runOrqi(cmd *cobra.Command, argv []string) (int, error) {
	flags, passthrough, err := parseOrqiArgv(argv)
	if err != nil {
		return 1, err
	}
	if flags.Help {
		printOrqiHelp()
		return 0, nil
	}
	if !orqiPlatformSupported() {
		return 1, fmt.Errorf("orqi runs on macOS (arm64, x86_64) and Linux x86_64; this machine is %s. See https://github.com/orq-ai/orqi", orqiPlatform())
	}
	path := resolveOrqi()
	dir := orqiInstallDir()
	if path != "" {
		dir = filepath.Dir(path)
	}

	switch {
	case flags.Install:
		if err := installOrqi(cmd.Context(), dir); err != nil {
			return 1, err
		}
		success("orqi installed in %s", dir)
		return 0, nil
	case path == "":
		if !orqiInteractive() {
			return 1, fmt.Errorf("orqi is not installed. Install it with:\n  %s\nor run: orq orqi --install", orqiInstallerCmd)
		}
		if !orqiConfirm("orqi is not installed. Install it now?") {
			return 0, nil
		}
		if err := installOrqi(cmd.Context(), dir); err != nil {
			return 1, err
		}
		// The installer's PATH hint may not have been acted on yet, so run
		// the path we just wrote rather than looking it up again.
		path = filepath.Join(dir, "orqi")
	}

	// --profile was parsed onto the root before dispatch, by
	// splitPassthroughGlobals — the same path that made installSessionPreRun
	// resolve this profile's token rather than the default one's. Reading it
	// back here keeps the profile orqi is told about and the ORQ_API_KEY it
	// inherits as one answer.
	env := map[string]string{}
	if f := cmd.Root().PersistentFlags().Lookup("profile"); f != nil && f.Changed {
		env["ORQ_PROFILE"] = f.Value.String()
	}
	return runOrqiChild(path, passthrough, env)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run 'TestRunOrqi|TestOrqiHelp' -v`
Expected: PASS, fourteen tests.

Also re-run Task 0's, since this task is what consumes the flag it routes:
Run: `go test ./cli/custom/ -run TestSplitPassthroughGlobals`

Verify by hand that the two halves meet, since no single unit test spans both packages:

```bash
go run ./cmd/orq orqi --profile staging --help
```
Expected: the help text, and no "unknown flag" error — proof that Task 0 consumed `--profile`
off an `orqi` line rather than passing it through.

- [ ] **Step 5: Register the command**

In `cli/custom/groups.go`, add to `commandGroup` next to the other getting-started entries:

```go
	"orqi":       groupGetStarted,
```

In `cli/custom/register.go`, add to `profileExemptCommands`:

```go
	"orqi":          true, // installs and launches orqi; touches no orq API
```

and to `registerCommands`, next to `NewLaunchCommand`:

```go
	root.AddCommand(commands.NewOrqiCommand())
```

- [ ] **Step 6: Run the full check, then regenerate the surface**

Run: `go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go')`
Expected: PASS, and `gofmt` prints nothing.

Run: `go run ./cmd/surface-dump -write`
Then: `go run ./cmd/surface-dump -check`
Expected: the second run exits 0, and `git diff --stat surface.json` shows the new command.

- [ ] **Step 7: Verify by hand**

```bash
go run ./cmd/orq orqi --help
go run ./cmd/orq --help | grep orqi
```
Expected: the command's own help listing all four flags, and an `orqi` row under "Get started:".

Every test above fakes `curl`, `sh` and the child. Nothing in CI ever runs the real installer, so
before merging, on a machine with no orqi installed, confirm the real path end to end once:

```bash
go run ./cmd/orq orqi --version   # answer yes at the prompt
```
Expected: install.sh's own output on stderr, then orqi's version. Repeat on Linux x86_64 if you
have one to hand; if you do not, say so on the PR rather than claiming the path is verified.

- [ ] **Step 8: Commit**

```bash
git add cli/custom/commands/orqi.go cli/custom/commands/orqi_test.go cli/custom/groups.go cli/custom/register.go surface.json
git commit -m "feat(orqi): add 'orq orqi' to install and run the assistant (RES-1475)"
```

---

### Task 5: Documentation

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the finished command from Task 4.
- Produces: nothing code depends on.

- [ ] **Step 1: Add the command-table row**

In the command table around `README.md:261`, next to the `orq launch <agent>` row:

```markdown
| `orq orqi` | Run orqi, the orq.ai assistant, installing it on first use (see [orqi](#orqi)) |
```

- [ ] **Step 2: Add the section**

After the Launch section (which ends around `README.md:335`):

````markdown
## orqi

[orqi](https://github.com/orq-ai/orqi) is the orq.ai assistant in your terminal: ask it to
investigate a failing agent, check workspace health or explain the platform, and it answers
using your workspace's own tools, models and skills.

```bash
orq orqi                                   # interactive session
orq orqi "why did my agent fail today?"    # one-shot
orq orqi --install                         # install or reinstall, then exit
```

The first run installs it, after asking, by running the orqi repo's own installer. It lands in
`~/.local/bin` (or `$ORQI_INSTALL_DIR`) and the session starts straight away, whether or not
that directory is on your PATH yet. Under `--no-input` nothing is installed and the command
prints the one-liner instead.

orqi reads the login session this CLI maintains, so `orq auth login` is all the setup it needs.
`--profile <name>` — orq's own global flag — selects which session it uses, and must come before
any argument meant for orqi — everything from the first orqi argument onward is passed through untouched, so orqi's own
flags always reach it. A leading `--` ends orq's parsing explicitly.

`--no-input` refuses rather than installing, so a script that needs both does it in two calls:

```bash
orq orqi --install                      # unconditional, prompts nobody
orq orqi --no-input "summarise today"   # fails loudly if the first call did not run
```

orqi publishes macOS (arm64, x86_64) and Linux x86_64 builds; on anything else the command says
so and stops.
````

- [ ] **Step 3: Verify the examples run**

Run: `go run ./cmd/orq orqi --install "hello"` — expected: an error naming `--install`, proving the terminal rule the README describes. (`--install` takes no arguments; there is no dry-run flag.)
Run: `go run ./cmd/orq orqi --help` — expected: help text matching the section.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document orq orqi"
```

---

## Self-Review

**Test-table coverage.** Sixteen of the spec's eighteen rows have a test. Two rows changed owner:
`--profile` and `--no-input` are orq globals routed by Task 0, so their argv-position behaviour is
asserted in `TestSplitPassthroughGlobalsOnOrqi` rather than against `parseOrqiArgv`. The two that do not:
"installer output written to stderr" is a property of `runOrqiCommand`, the seam every test
replaces, so a test would only assert the fake; and "`--profile` after a passthrough argument
reaches the child" is covered at the scanner level (Task 1) but not at `runOrqi` level. Both are
deliberate, and "`--profile` after a passthrough argument" moved to Task 0's
`TestSplitPassthroughGlobalsOnOrqi`, which is where that flag is now handled. No other row is
unmapped — an earlier draft of this plan claimed coverage for the
`curl`/`sh` preflight, for the install-dir binary and for a failed install without having tests
for them; Tasks 3 and 4 now do.

**Departure from the spec.** The spec has `orq orqi` scanning `--profile` out of the front of
argv itself. It cannot: `installSessionPreRun` injects the *default* profile's token into
`ORQ_API_KEY` in the parent, the child inherits it, and orqi's credential ladder prefers it over
`ORQ_PROFILE`. So the flag would have been accepted and silently ignored. Task 0 routes it
through the existing `splitPassthroughGlobals` machinery instead, which fixes the token as well
as the flag. User-visible behaviour is unchanged from what the spec promised — `--profile` at the
front works, `--profile` after an orqi argument is orqi's — so the spec's prose stands; only the
mechanism moved.

**Spec coverage.** Command surface, front-of-argv scanning of the three command-owned flags → Task 1. `--profile` routing → Task 0. Completion list mirroring the scanner → Task 1. Two-step binary resolution and the platform gate → Task 2. `--install` targeting an existing binary's directory → Task 4 (uses Task 2's resolution). Prompting through `hasInteractiveTTY`/`promptStdio` → Task 4, behind the `orqiInteractive` and `orqiConfirm` seams. `curl`/`sh` preflight, temp dir with `defer os.RemoveAll`, `ORQI_INSTALL_DIR` in the child env, installer output to stderr → Task 3. `launch.RunChild`, `ORQ_PROFILE` propagation from the parsed root flag, exec of the resolved path → Task 4. Registration and `surface.json` → Task 4. Docs → Task 5.

Two spec statements have no task and need none: the inherited `ORQ_API_KEY` is inherited by doing nothing (`launch.RunChild` merges over `os.Environ()`), and "no `--json` mode of its own" is satisfied by never writing to stdout.

Every row of the spec's test table maps to a test above, except "installer output written to stderr", which is asserted by construction — `runOrqiCommand` sets both streams to `bartolocli.Stderr` and the seam replaces the whole function in tests, so there is nothing left to observe. That is a deliberate gap, not an oversight.

**Placeholders.** None: every step carries the code or the command it needs.

**Type consistency.** `runOrqiCommand` takes `(ctx, env, name, args...)` in Task 3 and is called with that shape in Task 3 only. `runOrqiChild` matches `launch.RunChild`'s `(string, []string, map[string]string) (int, error)` in both its definition and the Task 4 fake. `resolveOrqi()` returns a path or `""` everywhere. `runOrqi` returns `(int, error)` in its definition, its caller in `RunE`, and every test.
