package commands

import (
	"bytes"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

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
