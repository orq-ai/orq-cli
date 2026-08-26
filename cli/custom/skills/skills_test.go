package skills

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
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
	// Into the snapshot, which is where every link we write points. A link
	// aimed anywhere else is not ours and refresh must leave it alone.
	gen, err := EnsureGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(gen, "orq-retired-skill"), ghost); err != nil {
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

// A writer that cannot take the manifest lock must fail instead of writing
// anyway. Writing unlocked loses the other writer's records, and a link the
// manifest does not record can never be removed by anything again.
func TestManifestLockTimeoutIsAnErrorNotAnUnlockedWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := EnsureGeneration(); err != nil {
		t.Fatal(err)
	}
	holdLockForeignly(t, home)

	release, err := InstallSession("claude")
	if err == nil {
		release()
		t.Fatal("InstallSession wrote the manifest while another process held the lock")
	}
	if !errors.Is(err, ErrManifestLocked) {
		t.Fatalf("error does not identify the lock holder: %v", err)
	}
	if !strings.Contains(err.Error(), "orq") {
		t.Errorf("error message does not say what happened: %v", err)
	}

	// Nothing may be left behind: no manifest, and no links the manifest
	// would not have recorded.
	if m, loadErr := LoadManifest(); loadErr != nil || m != nil {
		t.Errorf("a locked-out writer still wrote the manifest: %v %v", m, loadErr)
	}
	if entries, readErr := os.ReadDir(filepath.Join(home, ".claude", "skills")); readErr == nil && len(entries) != 0 {
		t.Errorf("a locked-out writer left %d unrecorded links on disk", len(entries))
	}
	// The other manifest writers must refuse for the same reason, and must
	// still hand back a Result: their callers report the error and then read
	// the result anyway, so a nil here is a crash rather than a refusal.
	if res, err := Install([]string{"claude"}); !errors.Is(err, ErrManifestLocked) || res == nil {
		t.Errorf("Install under a held lock: res=%v err=%v", res, err)
	}
	if res, err := Remove([]string{"claude"}); !errors.Is(err, ErrManifestLocked) || res == nil {
		t.Errorf("Remove under a held lock: res=%v err=%v", res, err)
	}
}

// The sweep is a writer too, and it refuses for the same reason. It only
// reaches the lock when there is something to collect, so this has to record a
// dead session first: with nothing to sweep it returns before locking, which
// is what keeps an uncontended command from waiting on a busy one.
func TestSweepUnderAHeldLockKeepsTheDeadSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := &Manifest{Version: manifestVersion, Fingerprint: Fingerprint()}
	m.Sessions = append(m.Sessions, Session{ID: "dead", PID: 999999, Paths: []string{filepath.Join(home, ".claude", "skills", "orq-gone")}})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	holdLockForeignly(t, home)

	if err := SweepDeadSessions(); !errors.Is(err, ErrManifestLocked) {
		t.Fatalf("SweepDeadSessions under a held lock: %v", err)
	}
	after, err := LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(after.Sessions) != 1 {
		t.Errorf("a locked-out sweep dropped the claim anyway: %d sessions left", len(after.Sessions))
	}
}

// Nothing recorded means nothing to lock. A writer on a machine that has never
// connected must still take the lock rather than read the missing state
// directory as permission to skip it: two first-run launches would otherwise
// both write the manifest and lose one another's links.
func TestAFirstRunWriterCreatesTheStateDirectoryAndLocksIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lock := filepath.Join(home, ".orq", "materialized-skills.json.lock")
	err := withManifestLock(func() error {
		if _, statErr := os.Stat(lock); statErr != nil {
			t.Errorf("wrote the manifest without a lock file at %s: %v", lock, statErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withManifestLock on a fresh home: %v", err)
	}
}

// Disconnect leaves a path it no longer owns alone, and stops recording it.
// Keeping the record would strand an entry no command can clear: refresh only
// disowns a foreign path once its skill also leaves the shipped set, so every
// later connect and disconnect would skip this one again in silence.
func TestDisconnectDropsTheRecordForAPathWeNoLongerOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(dir, names[0])
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(theirs, "their-work.md")
	if err := os.WriteFile(keep, []byte("the user's own edits"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{Version: manifestVersion, Fingerprint: Fingerprint()}
	m.AddLink(Link{Path: theirs, Agent: "claude", Skill: names[0], Mode: ModeCopy})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	res, err := Remove([]string{"claude"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != theirs {
		t.Errorf("Skipped = %v, want [%s] so disconnect can say it left it alone", res.Skipped, theirs)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("disconnect deleted a directory it does not own: %v", err)
	}
	after, err := LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("LoadManifest after remove: %v", err)
	}
	for _, l := range after.Links {
		if l.Path == theirs {
			t.Error("kept a record for a path disconnect will skip forever")
		}
	}
}

// A release that cannot take the lock keeps its claim, so the links stay
// recorded and the sweep can still collect them later.
func TestReleaseUnderAHeldLockKeepsTheClaim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	stop := holdLockForeignly(t, home)
	release() // cannot acquire; must not drop the claim

	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	if len(m.Sessions) != 1 || len(m.Sessions[0].Paths) == 0 {
		t.Fatalf("a locked-out release dropped the claim: %+v", m.Sessions)
	}
	for _, p := range m.Sessions[0].Paths {
		if !exists(p) {
			t.Errorf("claim kept but link %s is gone", p)
		}
	}

	// With the lock free again, the same release function retries and works.
	stop()
	release()
	m, err = LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	if len(m.Sessions) != 0 || len(m.Links) != 0 {
		t.Errorf("retry did not release: %+v %+v", m.Sessions, m.Links)
	}
}

// An interrupted run leaves links on disk with no manifest to record them.
// They are demonstrably ours — symlinks into our own snapshot — so the next
// install adopts them instead of mistaking them for somebody else's skills.
// Misreading them as foreign leaves links nothing can ever remove, which the
// user only discovers when generation pruning turns them into dangling
// symlinks in their real home.
func TestInstallAdoptsOrphansLeftByAnInterruptedRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Crash equivalent: the links are in place, the manifest never landed.
	path, err := manifestPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	names, _ := Names()
	orphans, _ := os.ReadDir(dir)
	if len(orphans) != len(names) {
		t.Fatalf("setup: %d orphans, want %d", len(orphans), len(names))
	}
	if err := SweepDeadSessions(); err != nil {
		t.Fatalf("SweepDeadSessions: %v", err)
	}

	res, err := Install([]string{"claude"})
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("install treated %d of its own orphans as foreign: %v", len(res.Skipped), res.Skipped)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(m.Links) {
		t.Fatalf("%d links on disk but %d recorded: the difference can never be removed", len(entries), len(m.Links))
	}
	// And the whole lot is now reclaimable.
	if _, err := Remove([]string{"claude"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("%d unreclaimable orphans left after remove", len(entries))
	}
}

// An adopted orphan must point at the current generation, not at whatever
// snapshot the crashed run happened to be using.
func TestInstallRepointsAnOrphanFromAnOldGeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	SetFingerprintForTest(t, "aaaaaaaaaaaaaaaa")
	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	path, _ := manifestPath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "bbbbbbbbbbbbbbbb")
	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	names, _ := Names()
	for _, n := range names {
		target, err := os.Readlink(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("readlink %s: %v", n, err)
		}
		if !strings.Contains(target, "gen-bbbbbbbbbbbbbbbb") {
			t.Errorf("adopted orphan still points at the old generation: %s", target)
		}
	}
}

// A session that finds an orphan of ours takes ownership of it, so it is
// recorded while the session runs and released with everything else. Free
// riding on it instead would leave it behind forever.
func TestSessionAdoptsAnOrphanOfOurs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	_ = release
	path, _ := manifestPath()
	if err := os.Remove(path); err != nil { // crash: links stay, manifest goes
		t.Fatal(err)
	}

	release2, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("second InstallSession: %v", err)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(m.Links) {
		t.Fatalf("%d links on disk but %d recorded", len(entries), len(m.Links))
	}
	release2()
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("the session left %d adopted orphans behind", len(entries))
	}
}

// kill(pid, 0) answers EPERM for a live process owned by another user, and
// reading that as "dead" is the direction that pulls skills out from under a
// running agent. PID 1 is always alive and (unless the test runs as root)
// always somebody else's.
func TestProcessAliveTreatsAnotherUsersProcessAsAlive(t *testing.T) {
	if !processAlive(1) {
		t.Error("pid 1 reported dead; a live process owned by another user would lose its links")
	}
}

// I1. The spec promises the installed skills are safe to delete. Deleting the
// whole directory used to wedge refresh permanently: project() only created
// the parent on the copy branch, so every later `orq` command repeated a raw
// symlink error and never advanced the fingerprint.
func TestRefreshRecreatesADeletedSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release-deleted-dir")
	res, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh after the directory was deleted: %v", err)
	}
	names, _ := Names()
	if len(res.Added) != len(names) {
		t.Errorf("re-linked %d of %d skills", len(res.Added), len(names))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(names) {
		t.Fatalf("directory not restored: %v (%d entries)", err, len(entries))
	}
	// Converged: a second refresh at the same fingerprint has nothing to do.
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Fingerprint != Fingerprint() {
		t.Errorf("fingerprint not advanced: %q", m.Fingerprint)
	}
}

// One agent's broken directory must not abandon every other agent's links. The
// abort was global: a single failure returned from the loop, so a stale skill
// set persisted silently everywhere else.
func TestRefreshKeepsGoingWhenOneLinkCannotBeProjected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude", "codex"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	if len(names) == 0 {
		t.Fatal("no skills embedded")
	}
	// codex's skills directory replaced by a regular file: nothing can be
	// created under it, and no amount of MkdirAll will fix it.
	codexDir := filepath.Join(home, ".codex", "skills")
	if err := os.RemoveAll(codexDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release-partial")
	res, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(res.Failed) != len(names) {
		t.Errorf("Failed = %d, want codex's %d links recorded as failures", len(res.Failed), len(names))
	}
	claudeDir := filepath.Join(home, ".claude", "skills")
	entries, err := os.ReadDir(claudeDir)
	if err != nil || len(entries) != len(names) {
		t.Fatalf("claude's links were abandoned by codex's failure: %v (%d entries)", err, len(entries))
	}
	for _, name := range names {
		target, err := os.Readlink(filepath.Join(claudeDir, name))
		if err != nil {
			t.Fatalf("Readlink %s: %v", name, err)
		}
		if !strings.Contains(target, "next-release-partial") {
			t.Errorf("%s still points at the old generation: %s", name, target)
		}
	}

	// The failure must not be absorbed. Advancing the fingerprint past it
	// would make the next Refresh a no-op, so codex would stay broken forever
	// and the warning naming it would have been printed exactly once.
	again, err := Refresh()
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if len(again.Failed) != len(names) {
		t.Errorf("second Refresh reported %d failures, want %d: a failed link was never retried", len(again.Failed), len(names))
	}

	// Once the user repairs the directory, the retry lands and the state
	// converges — the nagging is bounded by the repair, not by a counter.
	if err := os.Remove(codexDir); err != nil {
		t.Fatal(err)
	}
	repaired, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh after repair: %v", err)
	}
	if len(repaired.Failed) != 0 {
		t.Errorf("Failed = %d after the directory was repaired, want 0", len(repaired.Failed))
	}
	if m, err := LoadManifest(); err != nil || m == nil || m.Fingerprint != Fingerprint() {
		t.Errorf("a converged refresh did not record the fingerprint: %v %v", m, err)
	}
}

// M5. The generation directory inherited os.MkdirTemp's 0700 while everything
// inside it, and the snapshot root above it, are 0755. Whichever is right, the
// chain should not disagree with itself by accident.
func TestGenerationPermissionsMatchItsContents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	gen, err := EnsureGeneration()
	if err != nil {
		t.Fatalf("EnsureGeneration: %v", err)
	}
	info, err := os.Stat(gen)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("generation directory is %o, want 0755 to match its contents and ~/.orq/snapshot", got)
	}
}

// The manifest records a path, not a promise that we still own what sits there.
// Pruning a skill that left the shipped set used to os.RemoveAll whatever was at
// the recorded path, and refresh runs from PreRun on every command.
func TestPruneLeavesAPathWeNoLongerOwn(t *testing.T) {
	// Both modes, not linkMode(): ModeCopy is the Windows fallback, and its
	// ownership check is weaker than the symlink one — isOurs can only see
	// that a directory is a directory. Running only the host's mode meant the
	// Unix suite passed while the same refresh deleted the user's work on
	// Windows.
	for _, mode := range []string{ModeSymlink, ModeCopy} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".claude", "skills")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}

			// A real directory of the user's, at a path the manifest still
			// claims for a skill this binary no longer ships.
			victim := filepath.Join(dir, "orq-departed-skill")
			if err := os.MkdirAll(victim, 0o755); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(victim, "their-work.md")
			if err := os.WriteFile(keep, []byte("the user's own edits"), 0o644); err != nil {
				t.Fatal(err)
			}

			m := &Manifest{Version: manifestVersion, Fingerprint: "a-previous-release"}
			m.AddLink(Link{Path: victim, Agent: "claude", Skill: "orq-departed-skill", Mode: mode})
			if err := SaveManifest(m); err != nil {
				t.Fatal(err)
			}

			res, err := Refresh()
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}

			if _, err := os.Stat(keep); err != nil {
				t.Errorf("refresh deleted a directory it does not own: %v", err)
			}
			after, err := LoadManifest()
			if err != nil || after == nil {
				t.Fatalf("LoadManifest after refresh: %v", err)
			}
			for _, l := range after.Links {
				if l.Path == victim {
					t.Error("kept tracking a path we do not own; the record should be dropped")
				}
			}
			// Dropping the record silently would leave the user a directory
			// nothing mentions again, not even `orq disconnect skills`.
			if len(res.Disowned) != 1 || res.Disowned[0] != victim {
				t.Errorf("Disowned = %v, want [%s] so the caller can say so once", res.Disowned, victim)
			}
		})
	}
}

// A permanent link is only ever demoted to session-scoped by mistake, and the
// mistake is expensive: the session takes the user's install with it on exit.
// AddLink is the last place that can refuse, so pin all four combinations
// rather than only the end-to-end path through InstallSession.
func TestAddLinkNeverDemotesAPermanentLink(t *testing.T) {
	cases := []struct {
		name              string
		existing, added   bool
		wantSessionScoped bool
	}{
		{"permanent stays permanent", false, true, false},
		{"permanent is not re-marked", false, false, false},
		{"session is promoted", true, false, false},
		{"session stays session", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Manifest{Version: manifestVersion}
			m.AddLink(Link{Path: "/tmp/orq-x", Skill: "x", Session: c.existing})
			m.AddLink(Link{Path: "/tmp/orq-x", Skill: "x", Session: c.added})
			if len(m.Links) != 1 {
				t.Fatalf("got %d links, want 1: the second AddLink should replace the first", len(m.Links))
			}
			if m.Links[0].Session != c.wantSessionScoped {
				t.Errorf("Session = %v, want %v", m.Links[0].Session, c.wantSessionScoped)
			}
		})
	}
}

// A launch must never inherit a permanent install. Deleting the skills
// directory is documented as safe, so the repair path has to leave the record
// permanent, or the next session exit takes the user's `orq connect` install
// with it.
func TestALaunchDoesNotInheritAPermanentInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before, err := LoadManifest()
	if err != nil || before == nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	permanent := len(before.Links)
	if permanent == 0 {
		t.Fatal("install recorded no links")
	}

	// The user removes the directory; project.go documents this as recoverable.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	release()

	after, err := LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("LoadManifest after session: %v", err)
	}
	if len(after.Links) != permanent {
		t.Errorf("permanent links after a session: %d, want %d", len(after.Links), permanent)
	}
	for _, l := range after.Links {
		if l.Session {
			t.Errorf("%s was demoted to session-scoped", l.Path)
		}
	}
	if n := countEntries(t, dir); n != permanent {
		t.Errorf("%d skills on disk after the session exited, want %d", n, permanent)
	}
}

func countEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}

// The lock is the kernel's, held on an open descriptor. A second acquirer
// waits and then gives up rather than writing anyway: the manifest is the
// deletion allow-list, and a lost update there leaves links nothing can ever
// remove.
func TestASecondAcquireWaitsThenReportsTheHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	// acquireLock directly, not withManifestLock: manifestMu would serialize
	// these two before the file lock is ever consulted, which is the whole
	// point of having both, and would make this test prove the mutex instead.
	release, err := acquireLock()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if release == nil {
		t.Fatal("first acquire returned no release, so there was no state directory")
	}

	start := time.Now()
	second, err := acquireLock()
	elapsed := time.Since(start)
	if second != nil {
		second()
		t.Fatal("a second acquire took the lock while the first still held it")
	}
	if !errors.Is(err, ErrManifestLocked) {
		t.Errorf("err = %v, want it to wrap ErrManifestLocked so callers can recognise the case", err)
	}
	if elapsed < lockTimeout {
		t.Errorf("gave up after %s, want it to wait out lockTimeout (%s) first", elapsed, lockTimeout)
	}

	release()
	third, err := acquireLock()
	if err != nil || third == nil {
		t.Fatalf("acquire after release: %v", err)
	}
	third()
}

// The reason for a descriptor lock rather than a lock written into a file: a
// holder that dies takes its lock with it. Every wedge this package has had —
// an unstamped lock file, a lock stamped with a PID the OS later reused, a
// lock whose holder was killed mid-write — was a way for a contents-based
// lock to outlive the process that took it, and each needed its own guess
// (liveness, an age bound, a heartbeat) to undo.
func TestALockDiesWithItsHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(home, "held")

	// Re-exec this test binary as a helper that takes the lock and blocks.
	// A goroutine cannot stand in: it would share this process's descriptors
	// and, more to the point, could not be killed without cleanup the way a
	// crashed holder is.
	helper := exec.Command(os.Args[0], "-test.run=TestLockHolderHelper")
	helper.Env = append(os.Environ(), "ORQ_LOCK_HELPER=1", "HOME="+home, "ORQ_LOCK_READY="+ready)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = helper.Process.Kill()
		_, _ = helper.Process.Wait()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never signalled that it holds the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Killed, not asked to exit: nothing gets to run a cleanup, which is the
	// case a contents-based lock could not recover from.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Process.Wait(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	release, err := acquireLock()
	if err != nil {
		t.Fatalf("acquire after the holder was killed: %v", err)
	}
	if release == nil {
		t.Fatal("acquire returned no release, so there was no state directory")
	}
	release()
	if elapsed := time.Since(start); elapsed >= lockTimeout {
		t.Errorf("waited %s for a dead holder's lock; the kernel should have released it on exit", elapsed)
	}
}

// holdLockForeignly takes the manifest lock in a subprocess and returns the
// function that kills it. It has to be another process: the lock is the
// kernel's, held per open file description, so nothing this process does to a
// file on disk can simulate a foreign holder.
func holdLockForeignly(t *testing.T, home string) (stop func()) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "held")
	helper := exec.Command(os.Args[0], "-test.run=TestLockHolderHelper")
	helper.Env = append(os.Environ(), "ORQ_LOCK_HELPER=1", "HOME="+home, "ORQ_LOCK_READY="+ready)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	stop = func() {
		if killed {
			return
		}
		killed = true
		_ = helper.Process.Kill()
		_, _ = helper.Process.Wait()
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return stop
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never signalled that it holds the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLockHolderHelper is not a test. It is the subprocess half of
// TestALockDiesWithItsHolder, and it does nothing at all unless that test
// re-execs this binary with ORQ_LOCK_HELPER set.
func TestLockHolderHelper(t *testing.T) {
	if os.Getenv("ORQ_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper for TestALockDiesWithItsHolder")
	}
	release, err := acquireLock()
	if err != nil || release == nil {
		t.Fatalf("helper could not take the lock: %v", err)
	}
	if err := os.WriteFile(os.Getenv("ORQ_LOCK_READY"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Held until killed. Never released: that is what is being tested. A
	// sleep loop rather than select{}, which the runtime would report as a
	// deadlock and exit on — releasing the lock, which is the opposite of
	// the point.
	for {
		time.Sleep(time.Second)
	}
}

// A copy-mode projection has no symlink target to prove it is ours, so it
// carries a marker instead. Without one, "is a directory here" was the whole
// ownership check and any directory the user put at one of our paths passed
// it — on Windows that meant refresh, install and disconnect all treated the
// user's own work as ours and deleted it.
func TestACopyProvesOwnershipWithItsMarker(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "snapshot", "orq-x")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "skills", "orq-x")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	// projectCopy directly: the copy branch only runs on Windows, and it is
	// the branch whose ownership is hardest to prove.
	if err := projectCopy(src, dest, filepath.Dir(dest)); err != nil {
		t.Fatalf("projectCopy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("the copy did not land: %v", err)
	}
	link := Link{Path: dest, Skill: "orq-x", Mode: ModeCopy}
	if !isOurs(link) {
		t.Error("a copy we just projected is not recognised as ours")
	}
	if !ourOrphan(dest) {
		t.Error("an unrecorded copy of ours is not recognised as our orphan, so install would refuse to adopt it")
	}

	// The user takes the directory over. Removing the marker is the documented
	// way to say so, and a directory they created themselves never had one.
	if err := os.Remove(filepath.Join(dest, ownerMarker)); err != nil {
		t.Fatal(err)
	}
	if isOurs(link) {
		t.Error("a directory with no marker was claimed as ours; refresh, install and disconnect would all delete it")
	}
	if ourOrphan(dest) {
		t.Error("a directory with no marker was adoptable as our orphan")
	}
}

// The other side of the guard: a copy that is still ours must be prunable
// when its skill leaves the shipped set, or a retired skill stays in the
// agent's index forever.
func TestPruneRemovesACopyThatIsStillOurs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "orq-departed-skill")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gone, ownerMarker), []byte(ownerMarkerBody), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{Version: manifestVersion, Fingerprint: "a-previous-release"}
	m.AddLink(Link{Path: gone, Agent: "claude", Skill: "orq-departed-skill", Mode: ModeCopy})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	res, err := Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("a copy that is still ours was not pruned: %v", err)
	}
	if len(res.Disowned) != 0 {
		t.Errorf("Disowned = %v, want empty: we owned this one and removed it", res.Disowned)
	}
}
