package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

func TestPartitionConnectArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		agents  []string
		caps    []string
		wantErr bool
	}{
		"empty":          {args: nil},
		"agents only":    {args: []string{"claude", "kimi"}, agents: []string{"claude", "kimi"}},
		"caps only":      {args: []string{"gateway", "mcp"}, caps: []string{"gateway", "mcp"}},
		"mixed":          {args: []string{"claude", "gateway"}, agents: []string{"claude"}, caps: []string{"gateway"}},
		"tracing parses": {args: []string{"tracing"}, caps: []string{"tracing"}},
		"case folded":    {args: []string{"Claude", "GATEWAY"}, agents: []string{"claude"}, caps: []string{"gateway"}},
		"dedup":          {args: []string{"claude", "claude"}, agents: []string{"claude"}},
		"garbage":        {args: []string{"clod"}, wantErr: true},
		"flag-like":      {args: []string{"--gateway"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			agents, caps, err := partitionConnectArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if strings.Join(agents, ",") != strings.Join(tc.agents, ",") {
				t.Errorf("agents = %v, want %v", agents, tc.agents)
			}
			if strings.Join(caps, ",") != strings.Join(tc.caps, ",") {
				t.Errorf("caps = %v, want %v", caps, tc.caps)
			}
		})
	}
}

// The subcommand's documented case, through the new verb: a saved key and no
// --api-key wires the provider with the saved key.
func TestConnectWiresTheSavedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "model") {
			fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
			 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
			 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())

	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("config-directory", ""); viper.Set("profile", "") })
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	if err := writeAPIKeyProfile("default", "sk-orq-SAVED", ""); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	cmd := NewConnectCommand()
	cmd.SetArgs([]string{"kimi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatalf("provider not wired: %v", err)
	}
	if !strings.Contains(string(data), "sk-orq-SAVED") {
		t.Error("saved key did not reach the config")
	}

	// The inverse: disconnect removes both halves and reports them.
	dcmd := NewDisconnectCommand()
	dcmd.SetArgs([]string{"kimi"})
	if err := dcmd.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "config.toml")); !os.IsNotExist(err) {
		t.Error("provider config survives disconnect")
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "mcp.json")); !os.IsNotExist(err) {
		t.Error("mcp config survives disconnect")
	}
}

// tracing is vocabulary, not behaviour, until RES-1407 lands: it parses, says
// so, and alone it does nothing at exit 0.
func TestConnectTracingIsReservedNotImplemented(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	resetSetupMemos(t)

	var out strings.Builder
	rep := &reporter{w: &out}
	caps := dropUnavailableCaps(rep, []string{"tracing", "gateway"})
	if len(caps) != 1 || caps[0] != "gateway" {
		t.Errorf("caps = %v, want [gateway]", caps)
	}
	if !strings.Contains(out.String(), "RES-1407") {
		t.Errorf("tracing did not point at its ticket:\n%s", out.String())
	}
	if !capsWereOnlyTracing([]string{"claude", "tracing"}) {
		t.Error("tracing-only detection missed the only-tracing case")
	}
	if capsWereOnlyTracing([]string{"claude", "tracing", "mcp"}) {
		t.Error("tracing-only detection swallowed a real capability")
	}
}

// --dry-run resolves paths and writes nothing.
func TestConnectDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	cmd := NewConnectCommand()
	cmd.SetArgs([]string{"kimi", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "config.toml")); !os.IsNotExist(err) {
		t.Error("dry run wrote the provider config")
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "mcp.json")); !os.IsNotExist(err) {
		t.Error("dry run wrote the mcp config")
	}
}

// Naming agents is intent; a bare invocation is not. Without a TTY to ask,
// both verbs refuse rather than act on every agent the machine happens to
// have — and disconnect, the destructive one, refuses even after listing.
func TestBareInvocationsRefuseToActWithoutConsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// claude wired, so disconnect has something to find.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"mcpServers":{"`+mcpServerName+`":{"url":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "name the agents") {
		t.Errorf("bare connect without a TTY: err = %v, want a refusal naming the agents", err)
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{})
	err = d.Execute()
	if err == nil || !strings.Contains(err.Error(), "without confirmation") {
		t.Errorf("bare disconnect without a TTY: err = %v, want a refusal", err)
	}
	// It refused, so the config survives.
	data, readErr := os.ReadFile(filepath.Join(home, ".claude.json"))
	if readErr != nil || !strings.Contains(string(data), mcpServerName) {
		t.Error("a refused disconnect still removed the entry")
	}
}

// --dry-run previews without needing consent, and changes nothing.
func TestDisconnectDryRunRemovesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"mcpServers":{"` + mcpServerName + `":{"url":"x"}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(home, ".claude.json"))); got != before {
		t.Errorf("dry run changed the file:\n%s", got)
	}
}

// An unwired machine gets one line, not one per registered agent.
func TestDisconnectOnAnUnwiredMachineIsQuiet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code", ".config/opencode"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resetSetupMemos(t)

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if n := strings.Count(out.String(), "nothing"); n > 1 {
		t.Errorf("one line expected, got %d:\n%s", n, out.String())
	}
}
