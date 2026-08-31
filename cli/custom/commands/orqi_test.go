package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

// orqiFakeChild captures the launch instead of executing a real orqi.
func orqiFakeChild(t *testing.T, code int) (*string, *[]string, *map[string]string) {
	t.Helper()
	orig := runOrqiChild
	t.Cleanup(func() { runOrqiChild = orig })
	var binary string
	var args []string
	var env map[string]string
	runOrqiChild = func(b string, a []string, e map[string]string) (int, error) {
		binary, args, env = b, a, e
		return code, nil
	}
	return &binary, &args, &env
}

// orqiFakeConfirm answers the install prompt without a terminal.
func orqiFakeConfirm(t *testing.T, answer bool) *int {
	t.Helper()
	orig := orqiConfirm
	t.Cleanup(func() { orqiConfirm = orig })
	asked := 0
	orqiConfirm = func(string) bool {
		asked++
		return answer
	}
	return &asked
}

// orqiFakeInteractive decides whether runOrqi believes it has a terminal.
// `go test` never has one on stdin and stdout, so without this seam every
// prompt path is unreachable from a test.
func orqiFakeInteractive(t *testing.T, interactive bool) {
	t.Helper()
	orig := orqiInteractive
	t.Cleanup(func() { orqiInteractive = orig })
	orqiInteractive = func() bool { return interactive }
}

func orqiTestRoot(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "orq"}
	// The real root's --profile, which Task 0 parses before dispatch and
	// runOrqi reads back off the root. Without it Lookup returns nil.
	root.PersistentFlags().String("profile", "", "credentials profile")
	// cobra backfills the context in Execute, which these tests bypass; a nil
	// context panics exec.CommandContext inside the installer.
	root.SetContext(context.Background())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root
}

func runOrqiArgs(t *testing.T, argv ...string) (int, error) {
	t.Helper()
	return runOrqi(orqiTestRoot(t), argv)
}

func TestRunOrqiPassesArgumentsThrough(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	binary, args, _ := orqiFakeChild(t, 0)
	ran := orqiFakeRunner(t)
	if _, err := runOrqiArgs(t, "why did it fail?", "--version"); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *binary != "/usr/local/bin/orqi" {
		t.Errorf("binary = %q, want the resolved path", *binary)
	}
	if len(*args) != 2 || (*args)[0] != "why did it fail?" || (*args)[1] != "--version" {
		t.Errorf("args = %v, want both passed through verbatim", *args)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want no install for a binary already present", *ran)
	}
}

func TestRunOrqiPropagatesProfile(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	_, _, env := orqiFakeChild(t, 0)
	// As Task 0 leaves it: parsed onto the root, not sitting in argv.
	root := orqiTestRoot(t)
	if err := root.PersistentFlags().Set("profile", "staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := runOrqi(root, nil); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if (*env)["ORQ_PROFILE"] != "staging" {
		t.Errorf("env = %v, want ORQ_PROFILE=staging", *env)
	}
}

func TestRunOrqiLeavesProfileUnsetByDefault(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	_, _, env := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if _, ok := (*env)["ORQ_PROFILE"]; ok {
		t.Errorf("env = %v, want ORQ_PROFILE untouched so orqi resolves it itself", *env)
	}
}

func TestRunOrqiPropagatesExitCode(t *testing.T) {
	orqiFakeLookPath(t, "/usr/local/bin/orqi", nil)
	orqiFakeChild(t, 42)
	code, err := runOrqiArgs(t)
	if err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if code != 42 {
		t.Errorf("code = %d, want the child's 42", code)
	}
}

func TestRunOrqiInstallsAfterConfirmation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ORQI_INSTALL_DIR", dir)
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *asked != 1 {
		t.Errorf("prompted %d times, want 1", *asked)
	}
	if len(*ran) != 2 {
		t.Errorf("ran %v, want a download and an install", *ran)
	}
	if *binary != filepath.Join(dir, "orqi") {
		t.Errorf("binary = %q, want the just-installed path, not a bare lookup", *binary)
	}
}

func TestRunOrqiDeclinedInstallDoesNothing(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	orqiFakeConfirm(t, false)
	ran := orqiFakeRunner(t)
	code, err := runOrqiArgs(t)
	if err != nil || code != 0 {
		t.Fatalf("runOrqi = (%d, %v), want (0, nil)", code, err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v, want nothing", *ran)
	}
}

// --no-input reaches this through hasInteractiveTTY's viper read, exactly as
// it does for every other prompt in the CLI; TestHasInteractiveTTYHonorsNoInput
// covers that half. Here the seam stands in for "no terminal".
func TestRunOrqiRefusesWhenNotInteractive(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, false)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	_, err := runOrqiArgs(t)
	if err == nil || !strings.Contains(err.Error(), orqiInstallerCmd) {
		t.Fatalf("error = %v, want one showing the install one-liner", err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want neither", *asked, *ran)
	}
}

func TestRunOrqiInstallFlagUsesTheExistingBinarysDir(t *testing.T) {
	// Installing into ~/.local/bin regardless would fork a second copy for
	// anyone whose orqi came from source or a package manager.
	orqiFakeLookPath(t, "/opt/homebrew/bin/orqi", nil)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t, "--install"); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if len(*ran) != 2 || !strings.Contains((*ran)[1], "[ORQI_INSTALL_DIR=/opt/homebrew/bin]") {
		t.Errorf("ran %v, want the install to target the existing binary's dir", *ran)
	}
	if *binary != "" {
		t.Errorf("started %q, want --install to start no session", *binary)
	}
}

func TestRunOrqiRunsTheInstallDirBinaryWithoutPrompting(t *testing.T) {
	// install.sh only prints a PATH hint. A LookPath-only design would prompt
	// to reinstall on every run for anyone who has not acted on it.
	dir := t.TempDir()
	t.Setenv("ORQI_INSTALL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "orqi"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	binary, _, _ := orqiFakeChild(t, 0)
	if _, err := runOrqiArgs(t); err != nil {
		t.Fatalf("runOrqi error = %v, want nil", err)
	}
	if *binary != filepath.Join(dir, "orqi") {
		t.Errorf("binary = %q, want the one already in the install dir", *binary)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want neither", *asked, *ran)
	}
}

func TestRunOrqiFailedInstallStartsNoSession(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	orqiFakeInteractive(t, true)
	orqiFakeConfirm(t, true)
	orqiFakeRunner(t, nil, errors.New("exit status 1"))
	binary, _, _ := orqiFakeChild(t, 0)
	code, err := runOrqiArgs(t)
	if err == nil || code != 1 {
		t.Fatalf("runOrqi = (%d, %v), want (1, an installer error)", code, err)
	}
	if *binary != "" {
		t.Errorf("started %q, want no session after a failed install", *binary)
	}
}

func TestRunOrqiHelpNeverInstalls(t *testing.T) {
	t.Setenv("ORQI_INSTALL_DIR", t.TempDir())
	orqiFakeLookPath(t, "", errors.New("not found"))
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	code, err := runOrqiArgs(t, "--help")
	if err != nil || code != 0 {
		t.Fatalf("runOrqi = (%d, %v), want (0, nil)", code, err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want help to be free", *asked, *ran)
	}
}

// hasInteractiveTTY is the CLI's one prompt gate and had no test at all; the
// orqi command's --no-input promise now rests on this branch.
func TestHasInteractiveTTYHonorsNoInput(t *testing.T) {
	viper.Set("no-input", true)
	t.Cleanup(func() { viper.Set("no-input", false) })
	if hasInteractiveTTY() {
		t.Error("hasInteractiveTTY() = true under --no-input, want false")
	}
}

func TestOrqiHelpListsEveryWrapperFlag(t *testing.T) {
	// cobra cannot enumerate them: DisableFlagParsing means it never sees any
	// of them, so the help text is the only place they are discoverable.
	var out bytes.Buffer
	orig := bartolocli.Stderr
	t.Cleanup(func() { bartolocli.Stderr = orig })
	bartolocli.Stderr = &out
	printOrqiHelp()
	for _, flag := range append(append([]string{}, orqiFlagNames...), orqiGlobalFlagNames...) {
		if !strings.Contains(out.String(), flag) {
			t.Errorf("help does not mention %s:\n%s", flag, out.String())
		}
	}
}

func TestRunOrqiRefusesUnsupportedPlatform(t *testing.T) {
	orig := orqiPlatform
	t.Cleanup(func() { orqiPlatform = orig })
	orqiPlatform = func() string { return "windows/amd64" }
	asked := orqiFakeConfirm(t, true)
	ran := orqiFakeRunner(t)
	_, err := runOrqiArgs(t)
	if err == nil || !strings.Contains(err.Error(), "windows/amd64") {
		t.Fatalf("error = %v, want one naming the platform", err)
	}
	if *asked != 0 || len(*ran) != 0 {
		t.Errorf("prompted %d times and ran %v, want the refusal to come first", *asked, *ran)
	}
}
