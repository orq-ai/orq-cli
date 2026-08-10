package custom

import (
	"os"
	"path/filepath"
	"testing"

	toon "github.com/toon-format/toon-go"
)

// TOON is the CLI's default human-facing output format, but it is
// presentation-only: the stability-guaranteed machine contract is --json (see
// CHANGELOG.md). toon-go has no tagged releases, so the dependency is pinned
// to an exact pseudo-version in go.mod. This golden test is the tripwire for
// bumping that pin: if a newer toon-go renders differently, the test fails
// loudly and the change ships as a conscious golden update instead of a
// silent format shift under every user's terminal.
//
// Regenerate after a deliberate bump:
//
//	UPDATE_GOLDEN=1 go test ./cli/custom -run TestToonGolden
func TestToonGoldenOutput(t *testing.T) {
	// Shapes chosen to cover what API responses actually contain: nested
	// objects, a uniform object list (TOON's tabular case), strings with
	// spaces and reserved characters, numbers, bools, null, and empties.
	fixture := map[string]any{
		"object": map[string]any{
			"id":      "01ABC234DEF567GH",
			"key":     "my-deployment",
			"enabled": true,
			"weight":  0.75,
			"count":   42,
			"note":    "has spaces, a comma, and a: colon",
			"empty":   "",
			"missing": nil,
		},
		"rows": []any{
			map[string]any{"name": "alpha", "status": "live", "requests": 120},
			map[string]any{"name": "beta", "status": "paused", "requests": 0},
		},
		"tags":  []any{"a", "b", "c"},
		"empty": []any{},
	}

	got, err := toon.Marshal(fixture)
	if err != nil {
		t.Fatalf("toon.Marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "toon_golden.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(got) != string(want) {
		t.Fatalf("TOON output changed relative to the pinned golden.\n"+
			"If this is a deliberate toon-go bump, regenerate with UPDATE_GOLDEN=1,\n"+
			"review the rendering diff, and mention it in CHANGELOG.md.\n--- got ---\n%s\n--- want ---\n%s",
			got, want)
	}
}
