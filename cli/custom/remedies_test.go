package custom

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every "run 'orq …'" we print is a promise that the command exists. Deleting
// a command leaves those strings behind, and they compile: `orq setup
// coding-agents` survived its own subcommand's removal and shipped in six
// doctor rows, telling users to run something that was gone.
func TestEveryRecommendedCommandExists(t *testing.T) {
	root := buildRoot(t)

	// Only the single-quoted form, which is how this package writes a command
	// it is telling the user to run. Prose mentions of "orq" are not promises.
	pattern := regexp.MustCompile(`'orq ([a-z][a-z-]*(?: [a-z][a-z-]*)?)'`)
	// Commands whose second word is an agent or value, not a subcommand.
	takesArg := map[string]bool{"launch": true, "connect": true, "disconnect": true}

	files, err := filepath.Glob("commands/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(data), -1) {
			words := strings.Fields(m[1])
			if !resolves(root, words[0]) {
				t.Errorf("%s recommends %q, but there is no such command", path, "orq "+words[0])
				continue
			}
			// A second word is a subcommand only when the first takes one.
			if len(words) == 2 && !takesArg[words[0]] && !resolves(root, words[0], words[1]) {
				t.Errorf("%s recommends %q, but %q has no %q subcommand", path, "orq "+m[1], words[0], words[1])
			}
		}
	}
}

func resolves(root *cobra.Command, path ...string) bool {
	cmd, _, err := root.Find(path)
	if err != nil || cmd == nil {
		return false
	}
	// Find falls back to the parent when the child is missing.
	return cmd.Name() == path[len(path)-1]
}
