package custom

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"orq/cli/custom/skills"
)

// A machine that never ran `orq connect` has no manifest. registerCommands
// wires the skills refresh/sweep hook onto every command, and both
// skills.Refresh and skills.SweepDeadSessions return before touching the
// filesystem when there is nothing recorded — this proves the wiring
// preserves that, rather than just proving the functions do (skills_test.go
// already covers that in isolation).
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
}

// The hook is what makes an update take effect the next time the user opens
// their agent, not just the next time they happen to run `orq connect` again.
// Simulating "an older binary installed this" by staling the manifest's
// recorded fingerprint proves the pre-run hook itself calls Refresh before
// the command body runs (skills.SetFingerprintForTest, the seam
// skills_test.go uses to move the *real* fingerprint, lives in that
// package's own test scope and cannot drive this from here).
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
	if _, err := skills.Install([]string{"claude"}); err != nil {
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

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"man-pages", "--dir", filepath.Join(t.TempDir(), "man")})
	if err := root.Execute(); err != nil {
		t.Fatalf("man-pages: %v", err)
	}

	after, err := skills.LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("manifest after command: %v %v", after, err)
	}
	if after.Fingerprint != skills.Fingerprint() {
		t.Errorf("fingerprint = %q, want it refreshed to the current build's %q before the command ran", after.Fingerprint, skills.Fingerprint())
	}
}
