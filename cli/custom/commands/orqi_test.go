package commands

import (
	"context"
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

// orqiFakeRunner captures what would have been executed, with the env each
// command was given, and returns errs[n] for the nth call.
func orqiFakeRunner(t *testing.T, errs ...error) *[]string {
	t.Helper()
	orig := runOrqiCommand
	t.Cleanup(func() { runOrqiCommand = orig })
	var ran []string
	runOrqiCommand = func(_ context.Context, env map[string]string, name string, args ...string) error {
		line := strings.Join(append([]string{name}, args...), " ")
		if dir, ok := env["ORQI_INSTALL_DIR"]; ok {
			line += " [ORQI_INSTALL_DIR=" + dir + "]"
		}
		ran = append(ran, line)
		if len(ran) <= len(errs) {
			return errs[len(ran)-1]
		}
		return nil
	}
	return &ran
}

func TestInstallOrqiDownloadsThenRuns(t *testing.T) {
	ran := orqiFakeRunner(t)
	if err := installOrqi(context.Background(), "/opt/bin"); err != nil {
		t.Fatalf("installOrqi error = %v, want nil", err)
	}
	if len(*ran) != 2 {
		t.Fatalf("ran %v, want a curl and an sh", *ran)
	}
	if !strings.HasPrefix((*ran)[0], "curl ") || !strings.Contains((*ran)[0], orqiInstallerURL) {
		t.Errorf("first command = %q, want a curl of the installer", (*ran)[0])
	}
	if !strings.HasPrefix((*ran)[1], "sh ") || !strings.Contains((*ran)[1], "[ORQI_INSTALL_DIR=/opt/bin]") {
		t.Errorf("second command = %q, want sh with the install dir set", (*ran)[1])
	}
}

func TestInstallOrqiReportsDownloadFailure(t *testing.T) {
	ran := orqiFakeRunner(t, errors.New("curl: (22) 404"))
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), orqiInstallerURL) {
		t.Fatalf("error = %v, want one naming the installer URL", err)
	}
	if len(*ran) != 1 {
		t.Errorf("ran %v, want the installer never to run after a failed download", *ran)
	}
}

func TestInstallOrqiReportsInstallerFailure(t *testing.T) {
	orqiFakeRunner(t, nil, errors.New("exit status 1"))
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), orqiInstallerCmd) {
		t.Fatalf("error = %v, want one showing the manual one-liner", err)
	}
}

func TestInstallOrqiRequiresCurlAndSh(t *testing.T) {
	orqiFakeLookPathFunc(t, func(name string) (string, error) {
		if name == "curl" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	})
	ran := orqiFakeRunner(t)
	err := installOrqi(context.Background(), "/opt/bin")
	if err == nil || !strings.Contains(err.Error(), "curl") {
		t.Fatalf("error = %v, want one naming curl", err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want nothing fetched when the preflight fails", *ran)
	}
}

func TestInstallOrqiRemovesItsTempDir(t *testing.T) {
	var scriptPath string
	orig := runOrqiCommand
	t.Cleanup(func() { runOrqiCommand = orig })
	runOrqiCommand = func(_ context.Context, _ map[string]string, name string, args ...string) error {
		if name == "curl" {
			scriptPath = args[len(args)-2] // -o <path> <url>
			return os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o600)
		}
		return errors.New("exit status 1")
	}
	if err := installOrqi(context.Background(), "/opt/bin"); err == nil {
		t.Fatal("installOrqi error = nil, want the installer failure")
	}
	if scriptPath == "" {
		t.Fatal("curl was never called")
	}
	if _, err := os.Stat(filepath.Dir(scriptPath)); !os.IsNotExist(err) {
		t.Errorf("temp dir still present after a failed install: %v", err)
	}
}
