package custom

import (
	"os"
	"strings"
	"testing"
)

// cmd/orq/main.go is a bartolo scaffold file that we have customized, and
// `bartolo sync` rewrites it from templates/main.tmpl without asking. The
// revert compiles and every other test still passes, because nothing else
// asserts what main.go does — so this is the only thing standing between a
// sync and a silent loss of:
//
//	custom.Run     signal handling, and with it the documented exit contract
//	               (0/1/130/143) plus the context that cancels a device-login
//	               poll on Ctrl-C rather than waiting out the code's expiry
//	version        ldflags stamping; the template hardcodes .bartolo.json's
//	               app_version, so every build would report one frozen number
//
// `bartolo generate`, which the release pipeline runs, does not touch this
// file. Only `bartolo sync` (make sync) does.
func TestMainIsNotBartoloScaffold(t *testing.T) {
	const mainPath = "../../cmd/orq/main.go"

	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("reading %s: %v", mainPath, err)
	}
	got := string(src)

	// Each marker is absent from templates/main.tmpl, so the scaffold cannot
	// satisfy them: the template calls bartolocli.Init and os.Exit directly and
	// hardcodes its version.
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
