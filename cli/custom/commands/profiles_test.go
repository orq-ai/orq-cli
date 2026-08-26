package commands

import (
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// The bug this replaces printed the key verbatim, so the one thing worth
// guarding is that no stored secret survives into either view.
func TestListProfilesMasksSecrets(t *testing.T) {
	const secret = "sk-orq-abcdefghijklmnopqrstuvwxyz"

	restore := bartolocli.Creds
	bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
	bartolocli.Creds.Set("profiles.default.api_key", secret)
	bartolocli.Creds.Set("profiles.default.type", "apikey")
	t.Cleanup(func() { bartolocli.Creds = restore })

	profiles := listProfiles()
	if len(profiles) != 1 || profiles[0]["name"] != "default" {
		t.Fatalf("got %v, want one profile named default", profiles)
	}
	if got := profiles[0]["api_key"]; got == secret || !strings.HasSuffix(got, "…") {
		t.Errorf("api_key = %q, want a masked value", got)
	}
	if got := profiles[0]["type"]; got != "apikey" {
		t.Errorf("type = %q, want apikey passed through unmasked", got)
	}
}

func TestSecretKey(t *testing.T) {
	for _, key := range []string{"api_key", "API-KEY", "access_token", "client_secret", "password"} {
		if !secretKey(key) {
			t.Errorf("secretKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"type", "name", "server", "base_url"} {
		if secretKey(key) {
			t.Errorf("secretKey(%q) = true, want false", key)
		}
	}
}
