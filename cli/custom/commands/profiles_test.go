package commands

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

func TestListAuthProfilesUnionsStateWithoutMutatingCredentials(t *testing.T) {
	credsHarness(t)
	bartolocli.Creds.Set("profiles.api.type", "apikey")
	bartolocli.Creds.Set("profiles.api.api_key", "sk-orq-API-abcdefghijkl")
	auth.SetStateValue("session", "gateway_key", "sk-orq-GATEWAY-abcdefghijkl")
	auth.SetStateValue("session", "workspace", "acme")

	before := bartolocli.Creds.GetStringMap("profiles")
	rows := listAuthProfiles()
	if got := []string{rows[0]["name"].(string), rows[1]["name"].(string)}; !reflect.DeepEqual(got, []string{"api", "session"}) {
		t.Fatalf("profile names = %v, want api and session", got)
	}
	if rows[1]["workspace"] != "acme" || rows[1]["gateway_key"] == nil {
		t.Errorf("session-only state missing from row: %v", rows[1])
	}
	after := bartolocli.Creds.GetStringMap("profiles")
	if !reflect.DeepEqual(after, before) {
		t.Errorf("credentials profiles mutated: before %v, after %v", before, after)
	}
}

func TestProfileTableColumnsUseStableHumanOrder(t *testing.T) {
	rows := []map[string]any{
		{"name": "default", "type": "", "api_key": "sk-o********wxyz"},
		{"name": "acme", "type": "", "server": "https://acme.example"},
	}

	got := profileTableColumns(rows)
	want := []string{"name", "server", "api_key"}
	if len(got) != len(want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("columns[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderProfileTableUsesBartoloTableFormatter(t *testing.T) {
	previousFormatter := bartolocli.Formatter
	previousStdout := bartolocli.Stdout
	previousRoot := bartolocli.Root
	previousFormat := viper.GetString("output-format")
	t.Cleanup(func() {
		bartolocli.Formatter = previousFormatter
		bartolocli.Stdout = previousStdout
		bartolocli.Root = previousRoot
		viper.Set("output-format", previousFormat)
	})

	viper.Set("output-format", "toon")
	bartolocli.Root = nil
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(true, true)
	var out bytes.Buffer
	bartolocli.Stdout = &out

	err := renderProfileTable(map[string]any{
		"profiles": []map[string]any{{
			"name":    "default",
			"server":  "https://api.orq.ai",
			"type":    "apikey", // retained in the machine payload, not the human table
			"api_key": "sk-o********wxyz",
		}},
	}, []string{"name", "server", "api_key"})
	if err != nil {
		t.Fatalf("renderProfileTable: %v", err)
	}
	if got := out.String(); !strings.HasPrefix(got, "┌") {
		t.Fatalf("expected bordered table output, got %q", got)
	}
	if !bytes.Contains(out.Bytes(), []byte("NAME")) ||
		!bytes.Contains(out.Bytes(), []byte("SERVER")) ||
		!bytes.Contains(out.Bytes(), []byte("https://api.orq.ai")) ||
		!bytes.Contains(out.Bytes(), []byte("sk-o********wxyz")) {
		t.Fatalf("table is missing expected cells: %q", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("TYPE")) {
		t.Fatalf("table should omit internal profile type: %q", out.String())
	}
	if got := viper.GetString("output-format"); got != "toon" {
		t.Fatalf("output-format = %q after rendering, want toon", got)
	}
}

func TestMaskProfileSecretMatchesCredentialMasking(t *testing.T) {
	if got := maskProfileSecret("api_key", "sk-orq-abcdefghijklmnop"); got != "sk-o********mnop" {
		t.Fatalf("masked key = %v, want sk-o********mnop", got)
	}
	if got := maskProfileSecret("workspace", "acme"); got != "acme" {
		t.Fatalf("non-secret field = %v, want acme", got)
	}
}
