package skills

import (
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
