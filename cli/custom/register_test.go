package custom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"orq/cli/custom/skills"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// A machine that never ran `orq connect` has no manifest. The sweep half of
// the hook runs on every command, including this one, and must still leave
// nothing behind: SweepDeadSessions reads the manifest and returns before
// locking when there is no dead session to collect. That pre-lock check is
// load-bearing, because acquireLock creates ~/.orq/lock on the way past — so
// without it, `orq man-pages` on a fresh machine would create skills state
// for a user who has never asked for skills. This proves the wiring keeps the
// property, not just that the function does (skills_test.go covers that in
// isolation).
func TestSkillsRefreshHookTouchesNothingOnANeverConnectedMachine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"man-pages", "--dir", filepath.Join(t.TempDir(), "man")})
	if err := root.Execute(); err != nil {
		t.Fatalf("man-pages: %v", err)
	}

	// bartolo itself creates ~/.orq for its own config/cache (initConfig in
	// its cli.go), unconditionally, so its mere existence proves nothing
	// about the skills hook. What must not exist is anything skills-specific:
	// the manifest and the unpacked generation snapshot, which only Install,
	// Refresh with something to update, or a session create.
	if _, err := os.Stat(filepath.Join(home, ".orq", "materialized-skills.json")); !os.IsNotExist(err) {
		t.Errorf("skills manifest exists after a command on a never-connected machine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".orq", "snapshot")); !os.IsNotExist(err) {
		t.Errorf("skills generation snapshot exists after a command on a never-connected machine: %v", err)
	}
	// The lock file is what acquireLock creates, so its absence is what proves
	// the sweep returned before reaching for the lock at all.
	if _, err := os.Stat(filepath.Join(home, ".orq", "materialized-skills.json.lock")); !os.IsNotExist(err) {
		t.Errorf("skills manifest lock exists after a command on a never-connected machine: %v", err)
	}
}

// The hook makes an update take effect on the commands whose job involves
// skills, and stays out of the way everywhere else. Simulating "an older
// binary installed this" by staling the manifest's recorded fingerprint proves
// the pre-run hook itself calls Refresh before the command body runs
// (skills.SetFingerprintForTest, the seam skills_test.go uses to move the
// *real* fingerprint, lives in that package's own test scope and cannot drive
// this from here).
func TestSkillsRefreshHookFixesAStaleManifestBeforeTheCommandRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	names, err := skills.Names()
	if err != nil || len(names) == 0 {
		t.Fatalf("skills.Names: %v %v", names, err)
	}
	m := &skills.Manifest{Fingerprint: "a-previous-release", Generation: "previous-gen"}
	for _, n := range names {
		m.AddLink(skills.Link{
			Path:  filepath.Join(home, ".claude", "skills", n),
			Agent: "claude",
			Skill: n,
			Mode:  skills.ModeSymlink,
		})
	}
	if err := skills.SaveManifest(m); err != nil {
		t.Fatalf("seed stale manifest: %v", err)
	}
	// SaveManifest alone does not create the on-disk links refresh reprojects;
	// give it something real to reproject onto so the pre-run hook's refresh
	// has recorded links to bring current, matching what `orq connect` would
	// have left behind.
	if _, err := skills.Install([]string{"claude"}, skills.ScopeGlobal); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	seeded, err := skills.LoadManifest()
	if err != nil || seeded == nil {
		t.Fatalf("manifest after seed install: %v %v", seeded, err)
	}
	seeded.Fingerprint = "a-previous-release"
	seeded.Generation = "previous-gen"
	if err := skills.SaveManifest(seeded); err != nil {
		t.Fatalf("re-stale manifest: %v", err)
	}

	// man-pages has nothing to do with skills, so it must leave the manifest
	// exactly as it found it — no lock, no walk, no reprojection.
	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"man-pages", "--dir", filepath.Join(t.TempDir(), "man")})
	if err := root.Execute(); err != nil {
		t.Fatalf("man-pages: %v", err)
	}
	untouched, err := skills.LoadManifest()
	if err != nil || untouched == nil {
		t.Fatalf("manifest after unrelated command: %v %v", untouched, err)
	}
	if untouched.Fingerprint != "a-previous-release" {
		t.Errorf("fingerprint = %q, want the unrelated command to have left it stale", untouched.Fingerprint)
	}

	// connect is one of the commands the hook is scoped to. --status changes
	// nothing itself, so anything that moves is the hook.
	root = buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"connect", "--status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("connect --status: %v", err)
	}

	after, err := skills.LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("manifest after command: %v %v", after, err)
	}
	if after.Fingerprint != skills.Fingerprint() {
		t.Errorf("fingerprint = %q, want it refreshed to the current build's %q before the command ran", after.Fingerprint, skills.Fingerprint())
	}
}

// A generated command — `models list`, `whoami`, the kind everyone actually
// runs — reaches the same root.PersistentPreRunE the hook chains onto. That
// used to mean it refreshed skills; now it must mean the opposite, because
// the hook is scoped to the commands whose job involves them. A regression
// that widened the gate again would be invisible in-process, where every
// command shares one already-built tree.
//
// Driven in a subprocess: bartolo's generated command bodies call zerolog's
// Fatal (os.Exit) on any API error, including a plain connection refusal,
// which takes the whole `go test` binary down rather than the one subtest.
// That also makes this the closest thing to what a user sees — a real built
// binary, a real subcommand that is neither `man-pages` nor `whoami`.
func TestAGeneratedCommandDoesNotRefreshSkills(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "orq-hook-test")
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not determine this file's path")
	}
	moduleRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/orq")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build orq for subprocess test: %v\n%s", err, out)
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if _, err := skills.Install([]string{"claude"}, skills.ScopeGlobal); err != nil {
		t.Fatalf("seed skills install: %v", err)
	}

	manifestPath := filepath.Join(home, ".orq", "materialized-skills.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest after install: %v", err)
	}
	var m skills.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	m.Fingerprint = "a-previous-release"
	stale, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, stale, 0o644); err != nil {
		t.Fatalf("stale manifest: %v", err)
	}

	// Pointed at a closed local port so it fails fast instead of reaching the
	// network — whether the body succeeds is not the point.
	cmd := exec.Command(binPath, "models", "list", "--server", "http://127.0.0.1:1")
	cmd.Env = append(os.Environ(), "HOME="+home, "ORQ_API_KEY=sk-orq-TEST")
	_, _ = cmd.CombinedOutput() // exit status is not the point here

	data, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest after command: %v", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest after command: %v", err)
	}
	if m.Fingerprint != "a-previous-release" {
		t.Errorf("fingerprint = %q, want the stale value: a generated command refreshed skills", m.Fingerprint)
	}
}

func TestOnlySkillsCommandsRefreshSkills(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	for _, name := range []string{"launch", "connect", "disconnect", "setup", "doctor", "help", "workspace"} {
		cmd := &cobra.Command{Use: name}
		root.AddCommand(cmd)
	}
	want := map[string]bool{"launch": true, "connect": true, "disconnect": true, "setup": true}
	for _, cmd := range root.Commands() {
		if got := skillsCommand(cmd); got != want[cmd.Name()] {
			t.Errorf("skillsCommand(%q) = %v, want %v", cmd.Name(), got, want[cmd.Name()])
		}
	}

	// A subcommand answers the same as its parent: `orq connect skills` and
	// `orq launch claude` must not fall through the switch on their own name.
	for _, parent := range []string{"connect", "launch", "workspace"} {
		p, _, err := root.Find([]string{parent})
		if err != nil {
			t.Fatal(err)
		}
		child := &cobra.Command{Use: "anything"}
		p.AddCommand(child)
		if got := skillsCommand(child); got != want[parent] {
			t.Errorf("skillsCommand(%q %q) = %v, want %v", parent, child.Name(), got, want[parent])
		}
	}

	if skillsCommand(nil) {
		t.Error("a nil command refreshed skills")
	}
	// The root itself is `orq` with no subcommand — help output, nothing else.
	if skillsCommand(root) {
		t.Error("bare `orq` refreshed skills")
	}
}

func TestApplyNoColorPreservesTerminalTableRendering(t *testing.T) {
	previousTerminal := stdoutIsTerminal
	previousFormatter := bartolocli.Formatter
	previousStdout := bartolocli.Stdout
	t.Cleanup(func() {
		stdoutIsTerminal = previousTerminal
		bartolocli.Formatter = previousFormatter
		bartolocli.Stdout = previousStdout
	})
	stdoutIsTerminal = func() bool { return true }
	t.Setenv("NO_COLOR", "1")

	applyNoColor()
	var out bytes.Buffer
	bartolocli.Stdout = &out
	restore, err := bartolocli.SetOutputFormat("table")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if err := bartolocli.FormatList(map[string]any{"items": []map[string]any{{"name": "acme"}}}, "name"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "┌") {
		t.Fatalf("NO_COLOR disabled table rendering: %q", out.String())
	}
}

func TestMigrationRunsBeforeInMemoryProfileTypeRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "")
	t.Setenv("ORQ_NO_COLOR", "")
	previousStdout := bartolocli.Stdout
	cleanCreds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bartolocli.Creds = cleanCreds
		bartolocli.Stdout = previousStdout
	})
	root := buildRoot(t)
	viper.Set("no-color", false)
	bartolocli.Stdout = &bytes.Buffer{}
	dir := viper.GetString("config-directory")
	credentials := `{"profiles":{"default":{"api_key":"sk-orq-REAL","type":"stale","workspace":"acme"}}}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := bartolocli.NewCredentialsFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	bartolocli.Creds = creds
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("profile", "") })
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	stored := bartolocli.Creds.GetString("profiles.default.type")
	if _, ok := bartolocli.AuthHandlers[stored]; !ok {
		t.Fatalf("profile type repair was discarded by migration reload: %q", stored)
	}
}

// The global `--json` is gone: `-o json` is the only spelling. Keeping both
// was two ways to ask for one thing, and `--json -o yaml` asked for two
// formats at once.
// buildOrqBinary compiles the real `orq` and returns its path. A test that
// needs bartolo's own root — its PersistentPreRunE, its viper wiring, its
// config and env tiers — cannot get it from a synthetic cobra tree.
func buildOrqBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "orq")
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not determine this file's path")
	}
	moduleRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/orq")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build orq: %v\n%s", err, out)
	}
	return binPath
}

func TestJSONFlagIsGone(t *testing.T) {
	binPath := buildOrqBinary(t)

	t.Run("rejects --json", func(t *testing.T) {
		cmd := exec.Command(binPath, "--json", "version")
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "NO_COLOR=", "ORQ_NO_COLOR=")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("orq --json version succeeded: %s", out)
		}
		if !strings.Contains(string(out), "unknown flag: --json") {
			t.Fatalf("orq --json version = %s, want an unknown-flag error", out)
		}
	})
	t.Run("serializes with -o json", func(t *testing.T) {
		cmd := exec.Command(binPath, "-o", "json", "version")
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "NO_COLOR=", "ORQ_NO_COLOR=")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("orq -o json version: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("-o json output is not JSON: %v\n%s", err, out)
		}
	})
}

func TestImproveArgErrorsAppendsUsageLine(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	sub := &cobra.Command{
		Use:  "add-profile <name> <api-key>",
		Args: cobra.ExactArgs(2),
		Run:  func(*cobra.Command, []string) {},
	}
	root.AddCommand(sub)
	improveArgErrors(root)

	err := sub.Args(sub, nil)
	if err == nil {
		t.Fatal("expected an arity error")
	}
	for _, want := range []string{"accepts 2 arg(s)", "orq add-profile <name> <api-key>", "orq add-profile --help"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if err := sub.Args(sub, []string{"a", "b"}); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
}

// TestCustomCommandsDoNotCollideWithGenerated catches the duplicate; this pins
// which of the two kept `list`, since a swap leaves the count right.
func TestRouterModelsListIsTheRenamedOne(t *testing.T) {
	models := childCommand(buildRoot(t), "models")
	if models == nil {
		t.Fatal("no models command")
	}
	list, preview := childCommand(models, "list"), childCommand(models, "list-preview")
	if list == nil || preview == nil {
		t.Fatalf("want both list and list-preview, got list=%v list-preview=%v", list != nil, preview != nil)
	}
	if !strings.Contains(preview.Long, "Router") {
		t.Errorf("models list-preview is not the router listing: %q", preview.Long)
	}
	if strings.Contains(list.Long, "Router") {
		t.Errorf("models list is the router listing, so the wrong command was renamed: %q", list.Long)
	}
}

// --no-input refuses `auth profile add` only when it would prompt; a supplied
// key is the headless form and must keep working under ORQ_NO_INPUT.
func TestNoInputGuardLetsProfileAddWithAKeyThrough(t *testing.T) {
	add, _, err := buildRoot(t).Find([]string{"auth", "profile", "add"})
	if err != nil || commandPath(add) != "auth profile add" {
		t.Fatalf("auth profile add not found: %v", err)
	}
	if !wizardWouldPrompt(add, []string{"ci"}) {
		t.Error("name only, no key: must be refused under --no-input")
	}
	if wizardWouldPrompt(add, []string{"ci", "sk-positional"}) {
		t.Error("key as argument: must not be refused")
	}
	if err := add.Flags().Set("api-key-file", "ci.key"); err != nil {
		t.Fatal(err)
	}
	if wizardWouldPrompt(add, []string{"ci"}) {
		t.Error("--api-key-file: must not be refused")
	}
}

// The canonical profile-add command must be covered by the --no-input guard.
func TestInteractiveWizardGuardCoversCanonicalProfileAdd(t *testing.T) {
	if !interactiveWizardCommands["auth profile add"] {
		t.Error("`auth profile add` must be refused under --no-input")
	}
	// Deprecated aliases remain guarded while they are present in surface.json.
	surface, err := os.ReadFile(filepath.Join("..", "..", "surface.json"))
	if err != nil {
		t.Fatalf("read surface.json: %v", err)
	}
	for _, path := range []string{"auth add-profile", "auth list-profiles"} {
		stillShips := bytes.Contains(surface, []byte(`"orq `+path+`"`))
		if stillShips && !profileExemptCommands[path] {
			t.Errorf("%q still ships (surface.json) but lost its profile exemption", path)
		}
		if !stillShips && profileExemptCommands[path] {
			t.Errorf("%q is gone from surface.json; drop it from profileExemptCommands", path)
		}
	}
	if bytes.Contains(surface, []byte(`"orq auth add-profile"`)) != interactiveWizardCommands["auth add-profile"] {
		t.Error("`auth add-profile` must be in interactiveWizardCommands exactly while it still ships")
	}
	// Listing logins must work before one exists.
	if !profileExemptCommands["auth sessions"] {
		t.Error("`auth sessions` must be exempt from the unknown-profile guard")
	}
}

// threadSpanServer answers the two calls `orq traces thread tr_x span-1`
// makes, so a binary-level run reaches a render instead of the network.
func threadSpanServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v3/traces/tr_x/spans/span-1" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
			return
		}
		fmt.Fprint(w, `{"span": {"span_id": "span-1", "trace_id": "tr_x", "attributes": {`+
			`"gen_ai.request.model": "gpt-4o",`+
			`"gen_ai.input": [{"role": "user", "content": "hello"}],`+
			`"gen_ai.output": [{"role": "assistant", "content": "hi"}]}}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// threadHome is a HOME whose config file names format, or a bare one when it
// is empty.
func threadHome(t *testing.T, format string) string {
	t.Helper()
	home := t.TempDir()
	if format == "" {
		return home
	}
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".orq", "config.yaml"), []byte("output-format: "+format+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// `orq traces thread` takes its format from -o alone. Both standing sources —
// the exported variable and the config file — answer for every command in a
// shell or a tree, and neither may swap the render a person came to read.
// Only the real binary has those tiers populated at all.
func TestThreadIgnoresStandingFormatsInTheRealBinary(t *testing.T) {
	binPath := buildOrqBinary(t)
	server := threadSpanServer(t)
	for _, source := range []string{"environment", "config"} {
		t.Run(source, func(t *testing.T) {
			home := threadHome(t, "")
			env := []string{"HOME=" + home, "NO_COLOR=", "ORQ_NO_COLOR=", "ORQ_API_KEY=stub-key", "ORQ_SERVER=" + server.URL, "ORQ_OUTPUT_FORMAT="}
			if source == "environment" {
				env[len(env)-1] = "ORQ_OUTPUT_FORMAT=json"
			} else {
				env[0] = "HOME=" + threadHome(t, "json")
			}
			cmd := exec.Command(binPath, "traces", "thread", "tr_x", "span-1")
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("traces thread: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "<thread ") || strings.Contains(string(out), `"messages"`) {
				t.Fatalf("a standing %s format decided the render: %s", source, out)
			}
		})
	}
}

// -o is the only route to the two renders bartolo's own list does not have,
// and it reaches the command without any relaxation of bartolo's check: the
// local flag shadows the bound global one, so nothing this command is asked
// ever lands in viper. This is the test that fails if that stops being true.
func TestThreadFlagRendersMarkdownInTheRealBinary(t *testing.T) {
	binPath := buildOrqBinary(t)
	server := threadSpanServer(t)
	cmd := exec.Command(binPath, "traces", "thread", "tr_x", "span-1", "-o", "markdown")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+threadHome(t, ""),
		"NO_COLOR=",
		"ORQ_NO_COLOR=",
		"ORQ_API_KEY=stub-key",
		"ORQ_SERVER="+server.URL,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("traces thread -o markdown: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "## USER") {
		t.Fatalf("output = %s, want the markdown render", out)
	}
}

// A standing value bartolo does not know is refused before any command runs.
// `orq traces thread` used to be exempted from that check, so a config file
// naming `markdown` — a value every other command rejects — worked there and
// nowhere else. One answer for the whole CLI is the point: the same value
// fails the same way whichever command it is handed to.
func TestUnknownStandingFormatFailsEveryCommandAlike(t *testing.T) {
	binPath := buildOrqBinary(t)
	// Reachable and credentialed, so a run that gets past the check succeeds
	// and the exit code alone says the check was skipped.
	server := threadSpanServer(t)
	const want = `--output-format: "markdown" is not one of`
	for _, source := range []string{"environment", "config"} {
		t.Run(source, func(t *testing.T) {
			for _, args := range [][]string{{"version"}, {"traces", "thread", "tr_x", "span-1"}} {
				cmd := exec.Command(binPath, args...)
				cmd.Dir = t.TempDir()
				home, format := threadHome(t, ""), ""
				if source == "config" {
					home = threadHome(t, "markdown")
				} else {
					format = "markdown"
				}
				cmd.Env = append(os.Environ(),
					"HOME="+home,
					"NO_COLOR=",
					"ORQ_NO_COLOR=",
					"ORQ_API_KEY=stub-key",
					"ORQ_SERVER="+server.URL,
					"ORQ_OUTPUT_FORMAT="+format,
				)
				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("orq %v exited 0 under a %s markdown: %s", args, source, out)
				}
				if !strings.Contains(string(out), want) {
					t.Fatalf("orq %v under a %s markdown = %s, want %q", args, source, out, want)
				}
			}
		})
	}
}

// The notice's own tests cover when it prints; this covers that it is reached
// at all, and reached before the command body. The wiring is the fragile part:
// the notice hangs off bartolo's single PreRun hook, and a chain assembled in
// the wrong order — or an installer that replaces the hook instead of wrapping
// it — would silence it with every unit test still green.
func TestUpdateNoticeHookRunsBeforeTheCommandBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	var order []string
	updateNoticeOnce = sync.Once{}
	prev := maybePrintUpdateNotice
	maybePrintUpdateNotice = func(*cobra.Command) { order = append(order, "notice") }
	t.Cleanup(func() { maybePrintUpdateNotice = prev })

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.AddCommand(&cobra.Command{
		Use: "hook-probe",
		RunE: func(*cobra.Command, []string) error {
			order = append(order, "command")
			return nil
		},
	})
	root.SetArgs([]string{"hook-probe"})
	if err := root.Execute(); err != nil {
		t.Fatalf("hook-probe: %v", err)
	}

	if want := []string{"notice", "command"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// Cobra never reaches a PersistentPreRunE for help output, so the pre-run hook
// alone left `orq` and `orq --help` silent — and those are the runs someone
// makes while working out what the CLI does. `orq help <cmd>` reaches the hook
// AND the help renderer, and still owes the user exactly one line.
func TestUpdateNoticeReachesTheHelpPathExactlyOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	for _, args := range [][]string{{"--help"}, {}, {"help", "man-pages"}} {
		t.Run(strings.Join(append([]string{"orq"}, args...), " "), func(t *testing.T) {
			calls := 0
			updateNoticeOnce = sync.Once{} // a fresh process, as a user would have
			prev := maybePrintUpdateNotice
			maybePrintUpdateNotice = func(*cobra.Command) { calls++ }
			t.Cleanup(func() { maybePrintUpdateNotice = prev })

			root := buildRoot(t)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if calls != 1 {
				t.Errorf("notice reached %d times, want exactly 1", calls)
			}
		})
	}
}
