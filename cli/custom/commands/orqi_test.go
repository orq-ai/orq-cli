package commands

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// orqiFakeLookPath answers for "orqi" only, so a test that hides the binary
// does not also hide curl and sh from the installer's preflight.
func orqiFakeLookPath(t *testing.T, path string, err error) {
	orqiFakeLookPathFunc(t, func(name string) (string, error) {
		if name == "orqi" {
			return path, err
		}
		return exec.LookPath(name)
	})
}

// orqiFakeLookPathFunc swaps the PATH lookup wholesale, for tests that need a
// different answer per binary.
func orqiFakeLookPathFunc(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := orqiLookPath
	t.Cleanup(func() { orqiLookPath = orig })
	orqiLookPath = fn
}

func TestResolveOrqiPrefersPath(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	if got := resolveOrqi(); got != "/usr/local/bin/orqi" {
		t.Errorf("resolveOrqi() = %q, want /usr/local/bin/orqi", got)
	}
}

func TestResolveOrqiFindsInstallDirWhenNotOnPath(t *testing.T) {
	// install.sh only prints a PATH hint, so an installed orqi is routinely
	// invisible to LookPath. Missing this is a reinstall loop.
	dir := t.TempDir()
	binary := filepath.Join(dir, "orqi")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQI_INSTALL_DIR", dir)
	orqiFakeLookPath(t, "", errors.New("not found"))
	if got := resolveOrqi(); got != binary {
		t.Errorf("resolveOrqi() = %q, want %q", got, binary)
	}
}

func TestResolveOrqiEmptyWhenAbsent(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	if got := resolveOrqi(); got != "" {
		t.Errorf("resolveOrqi() = %q, want empty", got)
	}
}

func TestOrqiPlatformSupported(t *testing.T) {
	orig := orqiPlatform
	t.Cleanup(func() { orqiPlatform = orig })
	for platform, want := range map[string]bool{
		"darwin/arm64":  true,
		"darwin/amd64":  true,
		"linux/amd64":   true,
		"linux/arm64":   false,
		"windows/amd64": false,
	} {
		orqiPlatform = func() string { return platform }
		if got := orqiPlatformSupported(); got != want {
			t.Errorf("orqiPlatformSupported() on %s = %v, want %v", platform, got, want)
		}
	}
}
