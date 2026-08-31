package commands

import (
	"strings"
	"testing"
)

func TestParseOrqiArgvStopsAtFirstUnownedArg(t *testing.T) {
	// --install after a passthrough argument is orqi's, not ours: the same
	// rule that keeps orqi's own future flags reachable.
	flags, rest, err := parseOrqiArgv([]string{"why did it fail?", "--install"})
	if err != nil {
		t.Fatalf("parse error = %v, want nil", err)
	}
	if flags.Install {
		t.Error("install = true, want false: it came after an orqi argument")
	}
	if len(rest) != 2 {
		t.Errorf("rest = %v, want both args", rest)
	}
}

func TestParseOrqiArgvDoubleDashEndsScanning(t *testing.T) {
	flags, rest, err := parseOrqiArgv([]string{"--", "--install"})
	if err != nil {
		t.Fatalf("parse error = %v, want nil", err)
	}
	if flags.Install {
		t.Error("install = true, want false: --install after -- is orqi's")
	}
	if len(rest) != 1 || rest[0] != "--install" {
		t.Errorf("rest = %v, want [--install]", rest)
	}
}

func TestParseOrqiArgvInstallIsTerminal(t *testing.T) {
	_, _, err := parseOrqiArgv([]string{"--install", "extra"})
	if err == nil || !strings.Contains(err.Error(), "--install") {
		t.Fatalf("error = %v, want one naming --install", err)
	}
}

func TestOrqiCompletionFlagsMatchParser(t *testing.T) {
	// orqiGlobalFlagNames are deliberately absent here: Task 0 lifts them out
	// before this scanner ever sees them. TestSplitPassthroughGlobalsOnOrqi in
	// cli/custom is what proves those reach the right place.
	for _, name := range orqiFlagNames {
		argv := []string{name}
		flags, _, err := parseOrqiArgv(argv)
		if err != nil {
			t.Fatalf("parseOrqiArgv(%v) error = %v, want the flag consumed", argv, err)
		}
		if flags == (orqiFlags{}) {
			t.Errorf("%s is advertised for completion but sets nothing in the parser", name)
		}
	}
	if got := orqiCompletionFlags("--in"); len(got) != 1 || got[0] != "--install" {
		t.Errorf("orqiCompletionFlags(--in) = %v, want [--install]", got)
	}
	if got := orqiCompletionFlags("why"); got != nil {
		t.Errorf("orqiCompletionFlags(why) = %v, want nil: non-flag input belongs to orqi", got)
	}
}
