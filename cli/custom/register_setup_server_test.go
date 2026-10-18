package custom

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityOnlySetupPersistsServerForNextCommand(t *testing.T) {
	bin := buildOrqBinary(t)
	for _, capability := range []string{"otel", "mcp", "skills"} {
		for _, profile := range []bool{false, true} {
			name := capability
			if profile {
				name += " with profile"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				configDir := filepath.Join(home, ".orq")
				claudeDir := filepath.Join(home, ".claude")
				for _, dir := range []string{configDir, claudeDir} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				write := func(name, data string) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(configDir, name), []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				write("config.json", `{"server-default":"https://old.example"}`)
				if profile {
					write("credentials.json", `{"profiles":{"ci":{"type":"apikey","api_key":"synthetic-profile-key","server":"https://old-profile.example"}}}`)
				}
				env := []string{
					"HOME=" + home, "USERPROFILE=" + home, "CLAUDE_CONFIG_DIR=" + claudeDir,
					"PATH=" + t.TempDir(), "ORQ_NO_UPDATE_CHECK=1", "ORQ_API_KEY=synthetic-env-key",
					"ORQ_PROFILE=", "ORQ_SERVER=", "ORQ_API_BASE_URL=", "ORQ_TOKEN=", "ORQ_AUTHORIZATION=",
				}
				run := func(useProfile bool, args ...string) (string, string) {
					t.Helper()
					flags := []string{"--no-input", "-o", "json"}
					if useProfile {
						flags = append(flags, "--profile", "ci")
					}
					cmd := exec.Command(bin, append(flags, args...)...)
					cmd.Env, cmd.Dir = env, home
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Run(); err != nil {
						t.Fatalf("%v: %v\n%s\n%s", args, err, &stdout, &stderr)
					}
					if strings.Contains(stdout.String()+stderr.String(), "synthetic-") {
						t.Fatal("command output exposed a configured credential")
					}
					return stdout.String(), stderr.String()
				}
				stdout, stderr := run(profile, "--server", "https://self.example", "setup", "--capability", capability, "--yes")
				if !json.Valid([]byte(stdout)) {
					t.Fatalf("setup stdout is not JSON: %s", stdout)
				}
				if !strings.Contains(stderr, "https://self.example") || !strings.Contains(stderr, "orq server clear") {
					t.Fatalf("noninteractive setup omitted saved-server or undo notice: %s", stderr)
				}
				check := func(useProfile bool) {
					t.Helper()
					out, _ := run(useProfile, "server", "current")
					var state struct {
						Server string `json:"server"`
					}
					if err := json.Unmarshal([]byte(out), &state); err != nil {
						t.Fatal(err)
					}
					if state.Server != "https://self.example" {
						t.Fatalf("next command uses %q instead of the saved setup server", state.Server)
					}
				}
				check(profile)
				// Profile setup must also update the global default.
				if profile {
					check(false)
				}
			})
		}
	}
}
