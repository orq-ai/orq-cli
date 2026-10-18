package custom

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/launch"
)

func TestConnectNativeOtelUsesResolvedServerWithoutPlugin(t *testing.T) {
	bin := buildOrqBinary(t)
	for _, tc := range []struct {
		name, envServer, profileServer, wantServer string
		flags                                      []string
	}{
		{name: "saved default", wantServer: "https://saved.example"},
		{name: "environment", envServer: "https://env.example", wantServer: "https://env.example"},
		{name: "profile", envServer: "https://env.example", profileServer: "https://profile.example", flags: []string{"--profile", "ci"}, wantServer: "https://profile.example"},
		{name: "flag", envServer: "https://env.example", profileServer: "https://profile.example", flags: []string{"--profile", "ci", "--server", "https://flag.example"}, wantServer: "https://flag.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".orq")
			claudeDir := filepath.Join(home, ".claude")
			for _, dir := range []string{configDir, claudeDir} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, value any) {
				t.Helper()
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(configDir, "config.json"), map[string]string{"server-default": "https://saved.example"})
			const envKey = "synthetic-native-otel-env-canary"
			const profileKey = "synthetic-native-otel-profile-canary"
			wantKey := envKey
			if tc.profileServer != "" {
				write(filepath.Join(configDir, "credentials.json"), map[string]any{"profiles": map[string]any{"ci": map[string]string{"api_key": profileKey, "type": "apikey", "server": tc.profileServer}}})
				wantKey = profileKey
			}
			settingsPath := filepath.Join(claudeDir, "settings.json")
			write(settingsPath, map[string]any{
				"env":            map[string]string{"KEEP_ME": "untouched"},
				"enabledPlugins": map[string]bool{launch.TracePluginRef: true},
				"hooks":          map[string]any{"SessionStart": []any{}},
			})
			// There is no agent binary on PATH: native configuration must not
			// invoke Claude's plugin manager, even for an existing hook install.
			env := []string{
				"HOME=" + home, "USERPROFILE=" + home, "PATH=" + t.TempDir(),
				"CLAUDE_CONFIG_DIR=" + claudeDir, "ORQ_NO_UPDATE_CHECK=1",
				"ORQ_SERVER=" + tc.envServer, "ORQ_API_KEY=" + envKey,
				"ORQ_TOKEN=", "ORQ_AUTHORIZATION=", "ORQ_API_BASE_URL=", "ORQ_PROFILE=",
			}
			run := func(args ...string) string {
				t.Helper()
				argv := append([]string{"-o", "json", "--no-input"}, tc.flags...)
				cmd := exec.Command(bin, append(argv, args...)...)
				cmd.Env, cmd.Dir = env, home
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %v\n%s", args, err, out)
				}
				if strings.Contains(string(out), envKey) || strings.Contains(string(out), profileKey) {
					t.Fatal("command output exposed a telemetry credential")
				}
				return string(out)
			}
			readSettings := func() map[string]json.RawMessage {
				t.Helper()
				data, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatal(err)
				}
				var config map[string]json.RawMessage
				if err := json.Unmarshal(data, &config); err != nil {
					t.Fatal(err)
				}
				return config
			}
			run("connect", "claude", "otel")
			config := readSettings()
			var exporter map[string]string
			if err := json.Unmarshal(config["env"], &exporter); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
				"OTEL_METRICS_EXPORTER":        "otlp", "OTEL_LOGS_EXPORTER": "otlp",
				"OTEL_TRACES_EXPORTER":        "none",
				"OTEL_EXPORTER_OTLP_PROTOCOL": "http/json",
				"OTEL_EXPORTER_OTLP_ENDPOINT": tc.wantServer + "/v2/otel",
				"OTEL_EXPORTER_OTLP_HEADERS":  "Authorization=Bearer " + wantKey,
				"KEEP_ME":                     "untouched",
			} {
				if exporter[key] != want {
					t.Errorf("native setting %s does not match the resolved configuration", key)
				}
			}
			pluginsBefore := string(config["enabledPlugins"])
			hooksBefore := string(config["hooks"])
			var plugins map[string]bool
			if err := json.Unmarshal(config["enabledPlugins"], &plugins); err != nil || !plugins[launch.TracePluginRef] || len(config["hooks"]) == 0 {
				t.Fatal("native OTel connect changed an existing hook or plugin install")
			}
			status := run("connect", "claude", "otel", "--status")
			if !strings.Contains(status, "otel") {
				t.Fatalf("status omitted native telemetry: %s", status)
			}
			run("disconnect", "claude", "otel", "--yes")
			config = readSettings()
			if string(config["enabledPlugins"]) != pluginsBefore || string(config["hooks"]) != hooksBefore {
				t.Fatal("native OTel disconnect changed existing plugins or hooks")
			}
			exporter = nil
			if err := json.Unmarshal(config["env"], &exporter); err != nil {
				t.Fatal(err)
			}
			if exporter["KEEP_ME"] != "untouched" || exporter["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" {
				t.Fatal("disconnect did not remove native OTel while preserving unrelated settings")
			}
		})
	}
}
