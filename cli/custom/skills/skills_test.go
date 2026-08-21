package skills

import (
	"os"
	"path/filepath"
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
