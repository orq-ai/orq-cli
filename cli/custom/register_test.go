package custom

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"orq/cli/custom/skills"

	"github.com/spf13/cobra"
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
