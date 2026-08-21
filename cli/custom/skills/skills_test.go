package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
				Path:   filepath.Join("/home/user/.claude/skills", "skill", "dir", fmt.Sprintf("link%d", id)),
				Skill:  fmt.Sprintf("skill%d", id),
				Mode:   ModeSymlink,
				Agent:  "agent",
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
