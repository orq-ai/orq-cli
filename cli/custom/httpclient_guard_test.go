package custom

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every client that sends an orq credential must come from auth.NewHTTPClient,
// or it misses the authz_stale retry (RES-1636). launch and doctor each built
// their own once; this keeps a third from appearing.
func TestNoBareHTTPClients(t *testing.T) {
	allowed := map[string]bool{
		"auth/stale.go":           true, // NewHTTPClient itself
		"commands/updatecheck.go": true, // GitHub releases, no orq credential
	}
	bare := regexp.MustCompile(`&http\.Client\{|http\.DefaultClient|http\.(Get|Post|Head)\(`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bare.Match(src) && !allowed[filepath.ToSlash(path)] {
			t.Errorf("%s builds its own HTTP client; use auth.NewHTTPClient", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
