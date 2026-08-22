package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestNamesAreOrqPrefixedAndNonEmpty(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no skills embedded; run scripts/vendor-skills.sh")
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "orq-") && n != "evaluatorq" {
			t.Errorf("skill %q is neither orq-prefixed nor the known exception", n)
		}
	}
}

func TestFingerprintIsStableAndNonEmpty(t *testing.T) {
	a := Fingerprint()
	if a == "" {
		t.Fatal("empty fingerprint")
	}
	if b := Fingerprint(); a != b {
		t.Errorf("fingerprint not stable: %q then %q", a, b)
	}
}

func TestSetFingerprintForTestOverridesAndRestores(t *testing.T) {
	original := Fingerprint()
	t.Run("override", func(t *testing.T) {
		SetFingerprintForTest(t, "deadbeef")
		if got := Fingerprint(); got != "deadbeef" {
			t.Errorf("Fingerprint() = %q, want deadbeef", got)
		}
	})
	if got := Fingerprint(); got != original {
		t.Errorf("fingerprint not restored: got %q, want %q", got, original)
	}
}

func TestEnsureGenerationIsIdempotentAndComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := EnsureGeneration()
	if err != nil {
		t.Fatalf("EnsureGeneration: %v", err)
	}
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, statErr := os.Stat(filepath.Join(first, n, "SKILL.md")); statErr != nil {
			t.Errorf("skill %q missing from generation: %v", n, statErr)
		}
	}

	second, err := EnsureGeneration()
	if err != nil {
		t.Fatalf("second EnsureGeneration: %v", err)
	}
	if first != second {
		t.Errorf("same fingerprint produced two generations: %q then %q", first, second)
	}
}

func TestGenerationCollectionKeepsCurrentAndOnePrevious(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, fp := range []string{"aaa", "bbb", "ccc"} {
		SetFingerprintForTest(t, fp)
		if _, err := EnsureGeneration(); err != nil {
			t.Fatalf("EnsureGeneration(%s): %v", fp, err)
		}
	}
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("kept %d generations, want 2", len(entries))
	}
	if _, err := os.Stat(filepath.Join(home, "snapshot", "gen-ccc")); err != nil {
		t.Errorf("current generation collected: %v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if m, err := LoadManifest(); err != nil {
		t.Fatalf("LoadManifest on a clean machine: %v", err)
	} else if m != nil {
		t.Fatalf("LoadManifest on a clean machine returned %+v, want nil", m)
	}

	m := &Manifest{Version: manifestVersion, Fingerprint: "aaa", Generation: "/gen-aaa"}
	m.AddLink(Link{Path: "/home/u/.claude/skills/orq-build-agent", Agent: "claude", Skill: "orq-build-agent", Mode: ModeSymlink})
	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	got, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.Fingerprint != "aaa" || len(got.Links) != 1 || got.Links[0].Agent != "claude" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestManifestOfAnUnknownVersionIsTreatedAsForeign(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orqHome := filepath.Join(home, ".orq")
	if err := os.MkdirAll(orqHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orqHome, "materialized-skills.json"),
		[]byte(`{"version":99,"links":[{"path":"/somewhere"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m != nil {
		t.Errorf("a version-99 manifest was adopted: %+v", m)
	}
}

func TestAddLinkReplacesExistingByNormalizedPath(t *testing.T) {
	m := &Manifest{Version: manifestVersion}
	link1 := Link{Path: "/home/user/.claude/skills/skill1", Skill: "skill1", Mode: ModeSymlink}
	m.AddLink(link1)
	if len(m.Links) != 1 {
		t.Errorf("after first AddLink: got %d links, want 1", len(m.Links))
	}
	// Add the same path with trailing slash (should normalize to same entry).
	link2 := Link{Path: "/home/user/.claude/skills/skill1/", Skill: "skill1-updated", Mode: ModeCopy}
	m.AddLink(link2)
	if len(m.Links) != 1 {
		t.Errorf("after second AddLink with trailing slash: got %d links, want 1", len(m.Links))
	}
	// Verify it was replaced.
	if m.Links[0].Mode != ModeCopy {
		t.Errorf("link not replaced: Mode is %q, want %q", m.Links[0].Mode, ModeCopy)
	}
}

func TestRemoveLinksFiltersOutSpecifiedPaths(t *testing.T) {
	m := &Manifest{Version: manifestVersion}
	m.Links = []Link{
		{Path: "/path/a", Skill: "skill-a", Mode: ModeSymlink},
		{Path: "/path/b", Skill: "skill-b", Mode: ModeSymlink},
		{Path: "/path/c", Skill: "skill-c", Mode: ModeSymlink},
	}
	m.RemoveLinks([]string{"/path/b"})
	if len(m.Links) != 2 {
		t.Errorf("after RemoveLinks: got %d links, want 2", len(m.Links))
	}
	if m.Links[0].Skill != "skill-a" || m.Links[1].Skill != "skill-c" {
		t.Errorf("wrong links remain: %v", m.Links)
	}
}

func TestRemoveLinksNormalizesPathsBeforeMatching(t *testing.T) {
	m := &Manifest{Version: manifestVersion}
	m.AddLink(Link{Path: "/path/to/skill", Skill: "skill", Mode: ModeSymlink})
	// Remove with trailing slash (should normalize and match).
	m.RemoveLinks([]string{"/path/to/skill/"})
	if len(m.Links) != 0 {
		t.Errorf("RemoveLinks did not match normalized path: got %d links, want 0", len(m.Links))
	}
}

func TestOwnedPathsReturnsAllLinkPaths(t *testing.T) {
	m := &Manifest{Version: manifestVersion}
	m.Links = []Link{
		{Path: "/path/a", Skill: "skill-a", Mode: ModeSymlink},
		{Path: "/path/b", Skill: "skill-b", Mode: ModeSymlink},
	}
	owned := m.OwnedPaths()
	if len(owned) != 2 {
		t.Errorf("OwnedPaths: got %d paths, want 2", len(owned))
	}
	if owned[0] != "/path/a" || owned[1] != "/path/b" {
		t.Errorf("OwnedPaths: got %v, want [\"/path/a\" \"/path/b\"]", owned)
	}
}

func TestConcurrentSaveManifestSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const numWriters = 16
	errs := make(chan error, numWriters)
	for i := 0; i < numWriters; i++ {
		go func(id int) {
			m := &Manifest{
				Version:     manifestVersion,
				Fingerprint: "test-fp",
				Generation:  "/gen-test",
			}
			m.AddLink(Link{
				Path:  filepath.Join("/home/user/.claude/skills", "skill", "dir", fmt.Sprintf("link%d", id)),
				Skill: fmt.Sprintf("skill%d", id),
				Mode:  ModeSymlink,
				Agent: "agent",
			})
			errs <- SaveManifest(m)
		}(i)
	}
	// Collect all errors.
	for i := 0; i < numWriters; i++ {
		if err := <-errs; err != nil {
			t.Errorf("SaveManifest %d failed: %v", i, err)
		}
	}
	// Verify the final manifest is valid.
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest after concurrent saves: %v", err)
	}
	if m == nil {
		t.Fatal("LoadManifest returned nil after concurrent saves")
	}
	if m.Version != manifestVersion {
		t.Errorf("manifest version: got %d, want %d", m.Version, manifestVersion)
	}
}

func TestTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	dirs := func(agents ...string) []string {
		got, err := Targets(agents)
		if err != nil {
			t.Fatalf("Targets(%v): %v", agents, err)
		}
		var out []string
		for _, tg := range got {
			out = append(out, strings.TrimPrefix(tg.Dir, home))
		}
		sort.Strings(out)
		return out
	}

	if got := dirs("claude"); strings.Join(got, ",") != "/.claude/skills" {
		t.Errorf("claude alone = %v, want only its own directory", got)
	}
	if got := dirs("pi"); strings.Join(got, ",") != "/.agents/skills" {
		t.Errorf("pi alone = %v, want only the shared directory", got)
	}

	// kilo also reads an XDG-relative directory, but only on Linux; assert the
	// shape that is actually correct for the platform running this test so it
	// passes on Linux and macOS alike instead of being right on only one.
	wantShared := []string{"/.agents/skills"}
	if runtime.GOOS == "linux" {
		wantShared = append(wantShared, "/.config/agents/skills")
		sort.Strings(wantShared)
	}
	if got := dirs("opencode", "pi", "kilo"); strings.Join(got, ",") != strings.Join(wantShared, ",") {
		t.Errorf("three shared readers = %v, want %v", got, wantShared)
	}

	if got := dirs("claude", "codex", "kimi"); strings.Join(got, ",") != "/.claude/skills,/.codex/skills,/.kimi-code/skills" {
		t.Errorf("three own-directory agents = %v", got)
	}
}

func TestTargetsHonorAgentHomeEnvironmentVariables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	kimiHome := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", kimiHome)
	t.Setenv("CODEX_HOME", codexHome)

	got, err := Targets([]string{"kimi", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"kimi":  filepath.Join(kimiHome, "skills"),
		"codex": filepath.Join(codexHome, "skills"),
	}
	for _, tg := range got {
		if want[tg.Agent] != tg.Dir {
			t.Errorf("%s target = %q, want %q", tg.Agent, tg.Dir, want[tg.Agent])
		}
	}
}

func TestInstallCreatesOneLinkPerSkillPerTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	res, err := Install([]string{"claude"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	if len(res.Added) != len(names) {
		t.Errorf("added %d links, want %d", len(res.Added), len(names))
	}
	for _, n := range names {
		p := filepath.Join(home, ".claude", "skills", n)
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Errorf("skill %q not readable through the link: %v", n, err)
		}
	}
}

func TestInstallLeavesForeignEntriesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(dir, "my-own-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my-own-skill", "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "my-own-skill", "SKILL.md"))
	if err != nil || string(data) != "mine" {
		t.Errorf("install disturbed a skill it does not own: %v %q", err, data)
	}

	if _, err := Remove([]string{"claude"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-own-skill", "SKILL.md")); err != nil {
		t.Errorf("remove deleted a skill it does not own: %v", err)
	}
}

func TestRefreshPrunesSkillsThatLeftTheSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dir := filepath.Join(home, ".claude", "skills")
	ghost := filepath.Join(dir, "orq-retired-skill")
	if err := os.Symlink(filepath.Join(home, "nowhere"), ghost); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: %v %v", m, err)
	}
	m.AddLink(Link{Path: ghost, Agent: "claude", Skill: "orq-retired-skill", Mode: ModeSymlink})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release")
	if _, err := Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Lstat(ghost); !os.IsNotExist(err) {
		t.Errorf("a skill no longer in the set survived the refresh: %v", err)
	}
}

func TestRefreshOnANeverConnectedMachineTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("refresh created an agent directory on a machine that never connected")
	}
	if _, err := os.Stat(filepath.Join(home, ".orq")); !os.IsNotExist(err) {
		t.Error("refresh created orq state on a machine that never connected")
	}
}

func TestInstallSkipsAPathTakenOverByTheUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	if len(names) == 0 {
		t.Fatal("no skills embedded")
	}
	target := filepath.Join(home, ".claude", "skills", names[0])

	// The user deleted our link and put real, precious work in its place.
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(target, "my-precious-work.txt")
	if err := os.WriteFile(precious, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Install([]string{"claude"})
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	data, err := os.ReadFile(precious)
	if err != nil || string(data) != "mine" {
		t.Fatalf("install destroyed user data: %v %q", err, data)
	}
	found := false
	for _, p := range res.Skipped {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Errorf("taken-over path not reported as skipped: %v", res.Skipped)
	}
}

func TestRemoveSkipsAPathTakenOverByTheUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	target := filepath.Join(home, ".claude", "skills", names[0])

	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(target, "mine.txt")
	if err := os.WriteFile(precious, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Remove([]string{"claude"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("Remove destroyed a path taken over by the user: %v", err)
	}
	found := false
	for _, p := range res.Skipped {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Errorf("taken-over path not reported as skipped: %v", res.Skipped)
	}
}

func TestRefreshSkipsAPathTakenOverByTheUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	target := filepath.Join(home, ".claude", "skills", names[0])

	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(target, "mine.txt")
	if err := os.WriteFile(precious, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release-2")
	res, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("Refresh destroyed a path taken over by the user: %v", err)
	}
	found := false
	for _, p := range res.Skipped {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Errorf("taken-over path not reported as skipped: %v", res.Skipped)
	}
}

func TestRefreshSkipsALinkReplacedWithAForeignSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	target := filepath.Join(home, ".claude", "skills", names[0])

	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(home, "my-own-stuff")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user replaced our symlink with their own symlink to their own
	// directory. It is still a symlink, so a type-only check would say it is
	// still ours; it must not be, because its target is outside our snapshot.
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release-3")
	res, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	resolved, err := os.Readlink(target)
	if err != nil || resolved != elsewhere {
		t.Errorf("Refresh clobbered the user's own symlink: %v %q", err, resolved)
	}
	found := false
	for _, p := range res.Skipped {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Errorf("foreign symlink not reported as skipped: %v", res.Skipped)
	}
}

// opencode reads the shared agents-spec directory (~/.agents/skills), which
// Targets attaches to any request naming a shared reader. Remove must honor
// the same membership: naming "opencode" has to remove those shared links,
// not just an opencode-specific directory that does not exist.
func TestRemoveClearsTheSharedDirectoryForASharedReader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"opencode"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	sharedDir := filepath.Join(home, ".agents", "skills")
	names, _ := Names()
	if len(names) == 0 {
		t.Fatal("no skills to install")
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(sharedDir, n)); err != nil {
			t.Fatalf("shared install missing %q: %v", n, err)
		}
	}

	res, err := Remove([]string{"opencode"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(res.Removed) != len(names) {
		t.Errorf("removed %d links, want %d", len(res.Removed), len(names))
	}
	entries, err := os.ReadDir(sharedDir)
	if err == nil && len(entries) != 0 {
		t.Errorf("shared directory still has %d entries after removing its only reader", len(entries))
	}
}

func TestSessionLinksAreReferenceCounted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	releaseA, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	releaseB, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	releaseA()
	if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) == 0 {
		t.Fatalf("first session's exit removed links the second still needs: %v %d", readErr, len(entries))
	}
	releaseB()
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("last session's exit left %d entries", len(entries))
	}
}

// Two agents that read the same shared directory (opencode and pi both resolve
// to ~/.agents/skills) must refcount that directory between them: whichever
// session exits first may not take the links the other is still using.
func TestSessionsForSharedReadersRefcountTheSharedDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".agents", "skills")

	releaseOpencode, err := InstallSession("opencode")
	if err != nil {
		t.Fatalf("opencode session: %v", err)
	}
	releasePi, err := InstallSession("pi")
	if err != nil {
		t.Fatalf("pi session: %v", err)
	}
	names, err := Names()
	if err != nil || len(names) == 0 {
		t.Fatalf("Names: %v %d", err, len(names))
	}

	releaseOpencode()
	for _, n := range names {
		if _, statErr := os.Lstat(filepath.Join(dir, n)); statErr != nil {
			t.Fatalf("opencode's exit removed %q while the pi session still needs it: %v", n, statErr)
		}
	}
	releasePi()
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("last shared-reader session's exit left %d entries", len(entries))
	}
}

func TestSessionLinksSurviveAPermanentInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	release()

	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Errorf("a session exit removed the permanent install: %v %d", err, len(entries))
	}
}

func TestSweepRemovesLinksOwnedByDeadProcesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	if _, err := InstallSession("claude"); err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	// A PID that cannot be running: claim the links for a dead process.
	for i := range m.Sessions {
		m.Sessions[i].PID = 999999
	}
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	if err := SweepDeadSessions(); err != nil {
		t.Fatalf("SweepDeadSessions: %v", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("sweep left %d entries from a dead session", len(entries))
	}
}

// A sweep must not touch a session whose process is still running, and must
// leave the permanent install alone whatever it finds.
func TestSweepLeavesLiveSessionsAndPermanentLinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"codex"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	defer release()

	if err := SweepDeadSessions(); err != nil {
		t.Fatalf("SweepDeadSessions: %v", err)
	}
	names, _ := Names()
	for _, n := range names {
		if _, statErr := os.Lstat(filepath.Join(home, ".claude", "skills", n)); statErr != nil {
			t.Errorf("sweep removed live session link %q: %v", n, statErr)
		}
		if _, statErr := os.Lstat(filepath.Join(home, ".codex", "skills", n)); statErr != nil {
			t.Errorf("sweep removed permanent link %q: %v", n, statErr)
		}
	}
}

// A session that finds somebody else's file at one of its paths uses what is
// there and never removes it: only paths the CLI created are ever deleted.
func TestSessionLeavesForeignSkillsAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	names, err := Names()
	if err != nil || len(names) == 0 {
		t.Fatalf("Names: %v %d", err, len(names))
	}
	if err := os.MkdirAll(filepath.Join(dir, names[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, names[0], "THEIRS")
	if err := os.WriteFile(marker, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	release()

	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("session removed a directory it did not create: %v", statErr)
	}
}

// Sessions that start at the same time must not lose one another's records:
// load → mutate → save is not atomic, and this is the case that made the
// manifest's lost-update race real rather than theoretical.
func TestConcurrentSessionsKeepEveryClaim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Unpack up front: the point of the test is the manifest write, not the
	// generation race, which EnsureGeneration already handles.
	if _, err := EnsureGeneration(); err != nil {
		t.Fatal(err)
	}

	const sessions = 8
	releases := make([]func(), sessions)
	errs := make([]error, sessions)
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			releases[i], errs[i] = InstallSession("claude")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}

	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	if len(m.Sessions) != sessions {
		t.Fatalf("manifest records %d sessions, want %d: a concurrent write was lost", len(m.Sessions), sessions)
	}

	dir := filepath.Join(home, ".claude", "skills")
	for i, release := range releases {
		release()
		if i == sessions-1 {
			break
		}
		if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) == 0 {
			t.Fatalf("session %d's exit emptied the directory while %d sessions were still live", i, sessions-i-1)
		}
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("the last session's exit left %d entries", len(entries))
	}
}
