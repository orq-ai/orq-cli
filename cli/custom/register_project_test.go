package custom

import (
	"os"
	"path/filepath"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// The key `orq setup` exports into ~/.orq/env is ours, not a user override, so
// it must not outrank the session — that is what made `orq workspace use` and
// `orq projects use` silent no-ops after setup (RES-1465). Anything else keeps
// winning.
func TestOwnExportedKeyDefersOnlyToOurOwnKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prevProfile := viper.GetString("profile")
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("profile", prevProfile) })

	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewCredentialsFile: %v", err)
	}
	// Restore rather than nil out: other tests in this package share the
	// global and a nil one silently changes what they are testing.
	prevCreds := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() { bartolocli.Creds = prevCreds })
	creds.Set("profiles.default.gateway_key", "sk-orq-OURS")

	writeSession := func() {
		dir := filepath.Join(home, ".orq", "sessions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "default.json"), []byte(`{"version":1,"apiBaseUrl":"https://api.orq.ai","v1BaseUrl":"https://api.orq.ai/v1","authBaseUrl":"https://api.orq.ai/v2/auth","profileBaseUrl":"https://api.orq.ai/v2/auth/profile","user":{"id":"u"},"workspaces":[],"activeWorkspaceKey":"acme","refreshToken":"r","bootstrapToken":{"token":"t","expiresAt":"2099-01-01T00:00:00Z"},"workspaceTokens":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// No session: the exported key is the only credential there is.
	t.Setenv("ORQ_API_KEY", "sk-orq-OURS")
	if ownExportedKey() {
		t.Error("without a session the exported key must keep winning")
	}

	writeSession()
	if !ownExportedKey() {
		t.Error("our own exported key must defer to the session")
	}

	// A key we did not mint is a deliberate override.
	t.Setenv("ORQ_API_KEY", "sk-orq-THEIRS")
	if ownExportedKey() {
		t.Error("a key we did not mint must keep winning")
	}

	// A credentials profile is an explicit choice too.
	t.Setenv("ORQ_API_KEY", "sk-orq-OURS")
	creds.Set("profiles.default.api_key", "sk-orq-PROFILE")
	if ownExportedKey() {
		t.Error("a profile api_key must keep winning")
	}
}
