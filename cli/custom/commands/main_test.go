package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/launch"
)

// TestMain keeps every test off the real coding agents. `orq connect` shells out
// to an agent's own plugin manager, and a bare connect selects every agent whose
// config directory exists, so without this a test run on a machine that has
// claude installed installs and uninstalls plugins for real. The tests that
// exercise that path replace this with a recorder of their own and assert
// against what it saw.
func TestMain(m *testing.M) {
	realHome, _ = os.UserHomeDir()
	runAgentCommand = func(name string, args ...string) error {
		return fakeTracePluginCommand(args...)
	}
	os.Exit(m.Run())
}

// realHome is read before any test redirects HOME, so the stub can tell a
// redirected config from the one belonging to whoever runs the suite.
var realHome string

// fakeTracePluginCommand stands in for the agent's plugin manager, recording an
// install the way the real one does. A stub that exits 0 and writes nothing is
// the one thing this seam must not be: connect reads the install back before it
// reports success, so a lying stub would make every bare connect fail here and
// hide the read-back from the tests that should cover it.
func fakeTracePluginCommand(args ...string) error {
	if len(args) < 2 || args[0] != "plugin" {
		return nil
	}
	path, err := claudeSettingsPath()(true)
	if err != nil {
		return nil
	}
	// Only ever a redirected config. A test that reaches the install without
	// redirecting HOME or CLAUDE_CONFIG_DIR would otherwise edit the
	// settings.json of whoever runs the suite; it fails on connect's read-back
	// instead, which is the safe way round.
	if realHome != "" && strings.HasPrefix(path, filepath.Join(realHome, ".claude")+string(filepath.Separator)) {
		return nil
	}
	switch args[1] {
	case "install":
		cfg, err := readJSONConfig(path)
		if err != nil {
			return err
		}
		enabled, _ := cfg["enabledPlugins"].(map[string]any)
		if enabled == nil {
			enabled = map[string]any{}
		}
		enabled[launch.TracePluginRef] = true
		cfg["enabledPlugins"] = enabled
		return writeJSONConfigForTest(path, cfg)
	case "uninstall":
		cfg, err := readJSONConfig(path)
		if err != nil {
			return err
		}
		if enabled, ok := cfg["enabledPlugins"].(map[string]any); ok {
			delete(enabled, launch.TracePluginRef)
		}
		return writeJSONConfigForTest(path, cfg)
	}
	return nil
}

func writeJSONConfigForTest(path string, cfg map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeJSONConfig(path, cfg)
}
