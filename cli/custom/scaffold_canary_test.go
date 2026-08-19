package custom

import (
	"os"
	"strings"
	"testing"
)

// `bartolo sync` rewrites cmd/orq/main.go from templates/main.tmpl. The revert
// compiles and nothing else asserts what main.go does, so this is what catches
// it. `bartolo generate`, which the release pipeline runs, leaves it alone.
func TestMainIsNotBartoloScaffold(t *testing.T) {
	const mainPath = "../../cmd/orq/main.go"

	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("reading %s: %v", mainPath, err)
	}
	got := string(src)

	for _, want := range []struct{ marker, loses string }{
		{"custom.Run(", "signal handling and the 0/1/130/143 exit contract"},
		{`var version = "dev"`, "ldflags version stamping"},
	} {
		if !strings.Contains(got, want.marker) {
			t.Errorf("%s no longer contains %q — it looks like `bartolo sync` "+
				"rewrote it from templates/main.tmpl, which loses %s. Restore the "+
				"custom entrypoint rather than committing the regenerated file.",
				mainPath, want.marker, want.loses)
		}
	}
}
