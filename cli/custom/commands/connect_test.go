package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/launch"
	"orq/cli/custom/skills"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
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
		"caps only":      {args: []string{"gateway", "tracing"}, caps: []string{"gateway", "tracing"}},
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
}

// tracing is vocabulary, not behaviour: it parses, says so, and alone it does
// nothing at exit 0.
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
	if !strings.Contains(out.String(), "tracing is not available yet") {
		t.Errorf("tracing was dropped without saying so:\n%s", out.String())
	}
	if !capsWereAllUnavailable([]string{"claude", "tracing"}) {
		t.Error("unavailable-only detection missed the only-unavailable case")
	}
	if capsWereAllUnavailable([]string{"claude", "skills"}) {
		t.Error("skills is built and must count as a real capability")
	}
	if capsWereAllUnavailable([]string{"claude", "tracing", "gateway"}) {
		t.Error("unavailable-only detection swallowed a real capability")
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
	// kimi wired, so disconnect has something to find.
	if _, err := writeKimiProviderTOML(filepath.Join(home, ".kimi-code", "config.toml"),
		"https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
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
	data, readErr := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if readErr != nil || !strings.Contains(string(data), launch.KimiChatProvider) {
		t.Error("a refused disconnect still removed the entry")
	}
}

// --dry-run previews without needing consent, and changes nothing.
func TestDisconnectDryRunRemovesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".kimi-code", "config.toml")
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, path))
	resetSetupMemos(t)

	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := string(mustRead(t, path)); got != before {
		t.Errorf("dry run changed the file:\n%s", got)
	}
}

// Non-interactive runs never create credentials: no key and no TTY (or --yes)
// keeps the hard error naming the one-command fix.
func TestConnectNoCredentialNonInteractiveKeepsTheError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	viper.Set("config-directory", t.TempDir())
	t.Cleanup(func() { viper.Set("config-directory", "") })
	ensureCreds(t)
	resetSetupMemos(t)

	// gateway named explicitly: a request that also carries skills degrades to
	// the skills leg rather than failing, because that leg needs no credential
	// (TestKeylessBareConnectStillInstallsSkills). The hard error belongs to a
	// request that has nothing left to do without a key, which is this one.
	for _, args := range [][]string{{"kimi", "gateway"}, {"kimi", "gateway", "--yes"}} {
		cmd := NewConnectCommand()
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "no saved API key") {
			t.Errorf("connect %v: err = %v, want the no-saved-key error", args, err)
		}
	}
}

// Declining the inline login offer is a clean exit, not a fault: nothing
// written, nothing wired, exit 0.
func TestConnectLoginDeclinedIsACleanExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	viper.Set("config-directory", t.TempDir())
	t.Cleanup(func() { viper.Set("config-directory", "") })
	ensureCreds(t)
	resetSetupMemos(t)

	// noInput false forces the offer; the survey prompt fails without a TTY,
	// which the CLI treats as a decline everywhere.
	opts := &setupOptions{}
	rep := newReporter(true)
	_, _, err := resolveConnectAuth(NewConnectCommand(), rep, opts)
	if !errors.Is(err, errLoginDeclined) {
		t.Fatalf("err = %v, want errLoginDeclined", err)
	}

	// The caller maps the decline to success with nothing written.
	if err := connectSelected(NewConnectCommand(), rep, opts, []string{"kimi"}, []string{capGateway}, false); err != nil {
		t.Fatalf("declined login should exit clean, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "config.toml")); !os.IsNotExist(err) {
		t.Error("declined login still wired the provider")
	}
}

// ensureCreds gives bartolocli a credentials store when the test process has
// none, so savedAPIKey reads an empty profile instead of panicking.
func ensureCreds(t *testing.T) {
	t.Helper()
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
}

// --status is the read-only question disconnect --dry-run used to answer with
// a destructive verb: no prompt, no auth, no writes, exit 0 either way.
func TestConnectStatusIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resetSetupMemos(t)

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	// Bare and without a TTY, where plain connect refuses: status just answers.
	cmd := NewConnectCommand()
	cmd.SetArgs([]string{"--status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status on an unwired machine: %v", err)
	}
	if !strings.Contains(out.String(), "nothing wired") {
		t.Errorf("unwired machine not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "detected but not wired") {
		t.Errorf("detected agents not named:\n%s", out.String())
	}

	path := filepath.Join(home, ".kimi-code", "config.toml")
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, path))
	out.Reset()
	cmd = NewConnectCommand()
	cmd.SetArgs([]string{"--status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status on a wired machine: %v", err)
	}
	if !strings.Contains(out.String(), "kimi") || !strings.Contains(out.String(), "config.toml") {
		t.Errorf("wired path not shown:\n%s", out.String())
	}
	if got := string(mustRead(t, path)); got != before {
		t.Errorf("--status changed a config file:\n%s", got)
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

// An MCP entry this CLI did not write — an old "orq" key from v4.13.10, or a
// server the user added themselves — is not ours to touch. connect and
// disconnect share those config files, and both must leave every key but
// orq-workspace exactly as they found it. An agent that was never named is not
// touched at all.
func TestForeignMCPEntriesSurviveConnectAndDisconnect(t *testing.T) {
	srv := emptyCatalogueServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-EXPORTED")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	ensureCreds(t)
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	claudeConfig := filepath.Join(home, ".claude.json")
	kimiConfig := filepath.Join(home, ".kimi-code", "mcp.json")
	untouched := `{"mcpServers":{"orq":{"type":"http","url":"https://api.orq.ai/v2/mcp"}}}`
	if err := os.WriteFile(claudeConfig, []byte(untouched), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kimiConfig, []byte(`{"mcpServers":{"orq":{"url":"https://api.orq.ai/v2/mcp","bearerTokenEnvVar":"ORQ_API_KEY"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Compared field by field, not just for presence: a write that kept the key
	// and mangled the value inside it — dropping bearerTokenEnvVar, rewriting
	// the url — is the corruption this test exists to catch.
	foreign := mcpServersIn(t, kimiConfig)["orq"]

	// Twice: the second write must be idempotent, not a second entry.
	for range 2 {
		cmd := NewConnectCommand()
		cmd.SetArgs([]string{"kimi"})
		_ = cmd.Execute() // the catalogue is empty, so the gateway leg fails; the MCP leg must not
	}
	if servers := mcpServersIn(t, kimiConfig); servers[launch.MCPServerName] == nil {
		t.Errorf("connect did not write %s: %v", launch.MCPServerName, servers)
	} else if !reflect.DeepEqual(servers["orq"], foreign) {
		t.Errorf("connect changed the foreign entry:\n got: %v\nwant: %v", servers["orq"], foreign)
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"kimi"})
	_ = d.Execute()

	servers := mcpServersIn(t, kimiConfig)
	if servers[launch.MCPServerName] != nil {
		t.Errorf("disconnect left %s behind: %v", launch.MCPServerName, servers)
	}
	if !reflect.DeepEqual(servers["orq"], foreign) {
		t.Errorf("disconnect changed the foreign entry:\n got: %v\nwant: %v", servers["orq"], foreign)
	}
	// claude was never named, so nothing in this run may have opened its config.
	if got := string(mustRead(t, claudeConfig)); got != untouched {
		t.Errorf("claude config changed:\n got: %s\nwant: %s", got, untouched)
	}
}

// mcpServersIn reads the mcpServers map out of a JSON config, so a test can ask
// which entries survived rather than compare bytes a rewrite reformats.
func mcpServersIn(t *testing.T, path string) map[string]any {
	t.Helper()
	var cfg struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(mustRead(t, path), &cfg); err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return cfg.MCPServers
}

// claude has no gateway provider config — it reads the endpoint from its
// environment — so connect cannot wire it. Offering it, or reporting it as
// "detected but not wired", promises a wire that cannot exist.
func TestUnconnectableAgentsAreNotOffered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resetSetupMemos(t)

	got := detectedAgents()
	if len(got) != 1 || got[0] != "kimi" {
		t.Errorf("detected = %v, want only the connectable agent", got)
	}
	// Still addressable by name, so `orq connect claude` explains itself
	// instead of failing at parse time with "not an agent".
	if _, _, err := partitionConnectArgs([]string{"claude"}); err != nil {
		t.Errorf("claude stopped parsing as an agent: %v", err)
	}
}

// Removing the config removes the wire, not the credential. Saying only
// "removed" leaves the user modelling a machine with no orq credential on it,
// while the key stays valid and saved.
func TestDisconnectSaysTheKeySurvives(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKimiProviderTOML(filepath.Join(home, ".kimi-code", "config.toml"),
		"https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	credsHarness(t)
	ensureFormatter(t)
	if err := saveGatewayKeyProfile("sk-orq-SAVED", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "acme"); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{"kimi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "not revoked") {
		t.Errorf("disconnect implied the credential went with the config:\n%s", got)
	}
	// The remedy names the key it would remove, so it can be acted on.
	if !strings.Contains(got, "orq api-keys delete 01HZXW2K7Y8Q9M0N1P2R3S4T5V") {
		t.Errorf("no actionable way to revoke the surviving key:\n%s", got)
	}
}

// With no key saved there is nothing surviving to warn about, and a note about
// a credential the machine does not have is noise.
func TestDisconnectStaysQuietWithNoSavedKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKimiProviderTOML(filepath.Join(home, ".kimi-code", "config.toml"),
		"https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	credsHarness(t)
	ensureFormatter(t)
	resetSetupMemos(t)

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{"kimi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if strings.Contains(out.String(), "not revoked") {
		t.Errorf("warned about a key that does not exist:\n%s", out.String())
	}
}

// ensureFormatter gives bartolocli an output formatter when the test process
// has none, so emit does not nil-panic.
func ensureFormatter(t *testing.T) {
	t.Helper()
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
}

// "agents" is orq's own entity — the platform Agents you build and invoke.
// Using it for coding agents made `orq disconnect --json` read like a listing of
// those. The payload also has to carry the surviving key, or a script sees a
// clean removal where the terminal was told a live credential remains.
func TestDisconnectPayloadNamesCodingAgentsAndTheRetainedKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKimiProviderTOML(filepath.Join(home, ".kimi-code", "config.toml"),
		"https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	credsHarness(t)
	ensureFormatter(t)
	if err := saveGatewayKeyProfile("sk-orq-SAVED", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "acme"); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	var out strings.Builder
	prev := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = prev })

	// --json is a root flag; a subcommand run standalone has no TTY, so emit
	// produces the structured payload anyway.
	cmd := NewDisconnectCommand()
	cmd.SetArgs([]string{"kimi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	var payload struct {
		CodingAgents []struct {
			Agent   string   `json:"agent"`
			Removed []string `json:"removed"`
		} `json:"coding_agents"`
		Agents any `json:"agents"`
		APIKey struct {
			Retained bool   `json:"retained"`
			KeyID    string `json:"key_id"`
		} `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, out.String())
	}
	if payload.Agents != nil {
		t.Error("payload still uses 'agents', which is the orq Agents entity")
	}
	if len(payload.CodingAgents) != 1 || payload.CodingAgents[0].Agent != "kimi" {
		t.Fatalf("coding_agents = %+v", payload.CodingAgents)
	}
	// A list, not "gateway+mcp": a caller should not have to split on a
	// separator we invented.
	if len(payload.CodingAgents[0].Removed) != 1 || payload.CodingAgents[0].Removed[0] != "gateway" {
		t.Errorf("removed = %v, want a list of capability names", payload.CodingAgents[0].Removed)
	}
	if !payload.APIKey.Retained || payload.APIKey.KeyID != "01HZXW2K7Y8Q9M0N1P2R3S4T5V" {
		t.Errorf("api_key = %+v, want the surviving key named", payload.APIKey)
	}
}

// The two filters connect and disconnect already applied, missed on --status:
// a tracing-only run left caps as ["tracing"] and found no gateway target, and
// a named claude fell through to "detected but not wired" for a wire that
// cannot exist.
func TestConnectStatusAppliesTheSameFiltersAsConnect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resetSetupMemos(t)
	path := filepath.Join(home, ".kimi-code", "config.toml")
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	run := func(args ...string) string {
		out.Reset()
		cmd := NewConnectCommand()
		cmd.SetArgs(append([]string{"--status"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("status %v: %v", args, err)
		}
		return out.String()
	}

	if got := run("tracing"); strings.Contains(got, "nothing wired") {
		t.Errorf("--status tracing reported nothing wired on a wired machine:\n%s", got)
	}
	if got := run("claude", "gateway"); strings.Contains(got, "not wired") {
		t.Errorf("claude reported unwired for the gateway, promising a wire that cannot exist:\n%s", got)
	}
	got := run("claude")
	if !strings.Contains(got, "nothing to configure") {
		t.Errorf("claude's lack of a provider config went unsaid:\n%s", got)
	}
}

// Naming an agent asks about that agent, so a machine-wide verdict overstates
// what was looked at.
func TestNothingWiredIsScopedToTheAgentsNamed(t *testing.T) {
	if got := nothingWired(false, []string{"kimi"}); got != "nothing wired on this machine" {
		t.Errorf("bare run: %q", got)
	}
	if got := nothingWired(true, []string{"codex"}); got != "nothing wired for codex" {
		t.Errorf("named run: %q", got)
	}
}

// Logout leaves kimi's config holding the key literally, and until now only
// printed a line telling the user to run disconnect themselves. The offer is
// default-no and skipped without a TTY: signing out is routine, rewriting
// another program's config on the way past is not.
func TestDisconnectOnLogoutNeedsConsent(t *testing.T) {
	wire := func(t *testing.T) (home, path string) {
		t.Helper()
		home = t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		resetSetupMemos(t)
		path = filepath.Join(home, ".kimi-code", "config.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
			t.Fatal(err)
		}
		return home, path
	}

	t.Run("no TTY leaves the config alone", func(t *testing.T) {
		_, path := wire(t)
		if rows, _ := disconnectOnLogout(&setupOptions{noInput: true}, false); rows != nil {
			t.Errorf("removed without consent: %+v", rows)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("config removed in a non-interactive logout: %v", err)
		}
	})

	t.Run("--disconnect removes without asking", func(t *testing.T) {
		_, path := wire(t)
		rows, _ := disconnectOnLogout(&setupOptions{noInput: true}, true)
		if len(rows) == 0 {
			t.Fatal("--disconnect removed nothing")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("config survived --disconnect: %v", err)
		}
	})

	// --yes means "do not ask me about signing out". Reading it as consent to
	// rewrite another program's config would be a far larger yes than the one
	// given; asking anyway hung every script that passes it.
	t.Run("--yes suppresses the question rather than answering it", func(t *testing.T) {
		_, path := wire(t)
		if rows, _ := disconnectOnLogout(&setupOptions{yes: true}, false); rows != nil {
			t.Errorf("--yes was taken as consent to remove: %+v", rows)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("config removed under --yes: %v", err)
		}
	})

	t.Run("nothing wired asks nothing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		resetSetupMemos(t)
		// noInput false and no TTY in tests: reaching the prompt at all would hang
		// or return the default, so a nil here proves the wired check ran first.
		if rows, _ := disconnectOnLogout(&setupOptions{}, false); rows != nil {
			t.Errorf("offered a removal with nothing wired: %+v", rows)
		}
	})
}

// "opencode removed" reads as though the agent itself had been uninstalled.
// orq is what is removed; the agent is where from.
func TestRemovalNamesOrqAsTheThingRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	resetSetupMemos(t)
	path := filepath.Join(home, ".kimi-code", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	removeWiring(&reporter{w: &out}, []string{"kimi"}, []string{capGateway}, &setupOptions{}, false)
	got := out.String()
	if !strings.HasPrefix(strings.TrimSpace(got), "✓ orq removed from") {
		t.Errorf("the agent reads as the thing removed:\n%s", got)
	}
	// No preview ran, so this line is the only record of where it came from.
	if !strings.Contains(got, "config.toml") {
		t.Errorf("nothing said where orq was removed from:\n%s", got)
	}
}

func TestConnectSkillsInstallsAndDisconnectRemoves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %v %d", err, len(entries))
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"claude", "skills", "--yes"})
	if err := d.Execute(); err != nil {
		t.Fatalf("disconnect claude skills: %v", err)
	}
	entries, err = os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err == nil && len(entries) != 0 {
		t.Errorf("disconnect left %d entries behind", len(entries))
	}
}

// claude has no gateway provider config, so naming it alongside "gateway" and
// "skills" must not lose the skills leg: reportUnwirableAgents used to drop
// the whole agent whenever gateway was requested at all, which silently
// skipped skills removal for an agent combining a real capability with an
// unwirable one.
func TestDisconnectCombinesGatewayAndSkillsForClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %v %d", err, len(entries))
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"claude", "gateway", "skills", "--yes"})
	if err := d.Execute(); err != nil {
		t.Fatalf("disconnect claude gateway skills: %v", err)
	}
	entries, err = os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err == nil && len(entries) != 0 {
		t.Errorf("gateway+skills disconnect left %d skill entries behind", len(entries))
	}
}

// The status equivalent of the disconnect case above: claude's missing
// gateway config must not suppress the skills it does carry.
func TestConnectStatusReportsSkillsAlongsideAnUnwirableGateway(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills: %v", err)
	}

	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	status := NewConnectCommand()
	status.SetArgs([]string{"--status", "claude", "gateway", "skills"})
	if err := status.Execute(); err != nil {
		t.Fatalf("status claude gateway skills: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "not wired") {
		t.Errorf("skills wiring went unreported, claude read as entirely unwired:\n%s", got)
	}
	if !strings.Contains(got, "skills") {
		t.Errorf("status did not report the skills capability:\n%s", got)
	}
	if !strings.Contains(got, "nothing to configure") {
		t.Errorf("claude's lack of a gateway config went unsaid:\n%s", got)
	}
}

func TestConnectSkillsDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills", "--dry-run"})
	if err := c.Execute(); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error("dry run created the skills directory")
	}
}

// captureOutput collects what a command's reporter writes, so a test can
// assert on the report rather than on the filesystem. The reporter writes to
// bartolocli.Stderr (see splash.go's newReporter), not os.Stdout, so this
// redirects that stream rather than piping the process's real stdout.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	var out strings.Builder
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })
	fn()
	return out.String()
}

func TestConnectStatusGroupsByAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Connecting skills, unlike --status, needs a resolvable credential (see
	// resolveConnectAuth): the other "claude skills" connect fixtures in this
	// file all set a fake key rather than leaving ORQ_API_KEY empty.
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		// caps defaults to gateway alone when none are named (see
		// runConnectStatus), so skills must be named explicitly to show up —
		// same as TestConnectStatusReportsSkillsAlongsideAnUnwirableGateway.
		s.SetArgs([]string{"claude", "skills", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "claude") || !strings.Contains(out, "skills") {
		t.Errorf("status did not report claude's skills:\n%s", out)
	}
}

// A manifest entry whose path no longer exists is a broken link: the skill
// stopped working silently until something happens to look. --status must
// surface it as a warning instead of staying quiet.
func TestConnectStatusWarnsAboutMissingSkillLinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	m, err := skills.LoadManifest()
	if err != nil || m == nil || len(m.Links) == 0 {
		t.Fatalf("expected a populated manifest, got %+v, err=%v", m, err)
	}
	broken := m.Links[0]
	if err := os.RemoveAll(broken.Path); err != nil {
		t.Fatal(err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "skills", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "recorded but not installed") {
		t.Errorf("status did not warn about the missing link:\n%s", out)
	}
	if !strings.Contains(out, "orq connect skills") {
		t.Errorf("status did not point at the fix:\n%s", out)
	}
}

// Session-scoped links belong to a live `orq launch` and are created and torn
// down by that process; --status must not treat their absence between
// sessions as breakage.
func TestConnectStatusIgnoresMissingSessionLinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	m := &skills.Manifest{Fingerprint: "test", Generation: "test"}
	m.AddLink(skills.Link{
		Path:    filepath.Join(home, ".claude", "skills", "gone-session-link"),
		Agent:   "claude",
		Skill:   "example",
		Mode:    skills.ModeSymlink,
		Session: true,
	})
	if err := skills.SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if strings.Contains(out, "recorded but not installed") {
		t.Errorf("status warned about a session-scoped link:\n%s", out)
	}
}

// `--status kimi` naming only kimi must not surface claude's broken links,
// and a run that never asked about the skills capability at all must not
// surface skills breakage regardless of which agent is named — the same
// scoping the rest of runConnectStatus already applies to gateway targets.
func TestConnectStatusMissingLinkWarningsAreScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	for _, d := range []string{".claude", ".kimi-code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resetSetupMemos(t)

	claudePath := filepath.Join(home, ".claude", "skills", "gone-claude-skill")
	kimiPath := filepath.Join(home, ".kimi-code", "skills", "gone-kimi-skill")
	m := &skills.Manifest{Fingerprint: "test", Generation: "test"}
	m.AddLink(skills.Link{Path: claudePath, Agent: "claude", Skill: "a", Mode: skills.ModeSymlink})
	m.AddLink(skills.Link{Path: kimiPath, Agent: "kimi", Skill: "b", Mode: skills.ModeSymlink})
	if err := skills.SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	// Naming only kimi, for skills, must report kimi's break and not claude's.
	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"kimi", "skills", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status kimi skills: %v", err)
		}
	})
	if !strings.Contains(out, "gone-kimi-skill") {
		t.Errorf("kimi's own missing link went unreported:\n%s", out)
	}
	if strings.Contains(out, "gone-claude-skill") {
		t.Errorf("status kimi reported claude's missing link, which was never asked about:\n%s", out)
	}

	// Naming kimi for gateway only must not mention skills breakage at all,
	// kimi's or anyone else's. The capability is named explicitly: a bare
	// `--status` now asks about every built capability, because a status that
	// silently omits installed skills makes a false statement about the
	// machine (see availableCapabilities).
	out = captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"kimi", "gateway", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status kimi: %v", err)
		}
	})
	if strings.Contains(out, "recorded but not installed") || strings.Contains(out, "recorded skills are not installed") {
		t.Errorf("status kimi (gateway only) reported skills breakage that was never asked about:\n%s", out)
	}
}

// Deleting a whole skills directory records a dozen missing links pointing
// at the same remedy. One line naming the directory and a count is the right
// shape; a dozen identical lines would tell the user nothing more.
func TestConnectStatusCollapsesMissingLinksPerDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	dir := filepath.Join(home, ".claude", "skills")
	m := &skills.Manifest{Fingerprint: "test", Generation: "test"}
	for _, name := range []string{"one", "two", "three"} {
		m.AddLink(skills.Link{Path: filepath.Join(dir, name), Agent: "claude", Skill: name, Mode: skills.ModeSymlink})
	}
	if err := skills.SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "skills", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if n := strings.Count(out, "recorded but not installed"); n != 0 {
		t.Errorf("expected the per-file phrasing to be collapsed, got %d occurrences:\n%s", n, out)
	}
	if !strings.Contains(out, "3 recorded skills are not installed") {
		t.Errorf("expected a single proportionate line naming the directory and a count of 3:\n%s", out)
	}
	if !strings.Contains(out, "orq connect skills") {
		t.Errorf("status did not point at the fix:\n%s", out)
	}
}

func TestAnUpdatedBinaryRelinksOnTheNextCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	before, err := skills.LoadManifest()
	if err != nil || before == nil {
		t.Fatalf("manifest after connect: %v %v", before, err)
	}

	// skills.SetFingerprintForTest lives in export_test.go, which is scoped to
	// the skills package's own tests and is not linkable from here. Simulating
	// "an older binary installed this" by staling the recorded fingerprint
	// directly has the same effect on Refresh's decision (it only ever
	// compares the manifest's recorded fingerprint against the live one) and
	// needs no new exported production API.
	before.Fingerprint = "a-previous-release"
	if err := skills.SaveManifest(before); err != nil {
		t.Fatalf("seed stale manifest: %v", err)
	}

	if _, err := skills.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := skills.LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("manifest after refresh: %v %v", after, err)
	}
	if after.Fingerprint != skills.Fingerprint() {
		t.Errorf("fingerprint = %q, want it corrected to the current build's %q", after.Fingerprint, skills.Fingerprint())
	}
	names, _ := skills.Names()
	for _, n := range names {
		p := filepath.Join(home, ".claude", "skills", n, "SKILL.md")
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("%s unreadable after refresh: %v", n, statErr)
		}
	}
}

// C1. A claude-only machine is the single most common configuration this
// feature ships into, and claude has no gateway provider config. Selecting the
// agent set on `writeProvider != nil` therefore made a skills installation
// invisible to a bare `--status` and unreachable from a bare `disconnect`: the
// user could install fourteen skills and then neither see nor remove them.
func TestBareStatusAndDisconnectSeeSkillsOnAClaudeOnlyMachine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills: %v", err)
	}
	installed := filepath.Join(home, ".claude", "skills")
	if entries, err := os.ReadDir(installed); err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %v", err)
	}

	got := captureOutput(t, func() {
		status := NewConnectCommand()
		status.SetArgs([]string{"--status"})
		if err := status.Execute(); err != nil {
			t.Fatalf("bare --status: %v", err)
		}
	})
	if strings.Contains(got, "nothing wired on this machine") {
		t.Errorf("bare --status called a machine with 14 installed skills empty:\n%s", got)
	}
	if !strings.Contains(got, "skills") {
		t.Errorf("bare --status never mentioned the skills it can see:\n%s", got)
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"--yes"})
	if err := d.Execute(); err != nil {
		t.Fatalf("bare disconnect: %v", err)
	}
	if entries, err := os.ReadDir(installed); err == nil && len(entries) != 0 {
		t.Errorf("bare disconnect left %d skills installed", len(entries))
	}
}

// The same category error one step further out: an agent that received skills
// and was then uninstalled goes undetected, and its skills become unreachable
// from every bare command. The manifest, not the machine, is the authority on
// what was installed.
func TestBareDisconnectReachesSkillsForAnUndetectedAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"codex", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect codex skills: %v", err)
	}
	installed := filepath.Join(home, ".codex", "skills")
	if entries, err := os.ReadDir(installed); err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %v", err)
	}
	// codex is now "uninstalled": detect() looks at ~/.codex, and the skills
	// directory is all that is left of it.
	for _, name := range []string{"config.toml", "auth.json"} {
		os.Remove(filepath.Join(home, ".codex", name))
	}
	resetSetupMemos(t)

	agents := agentsToInspect([]string{capSkills})
	found := false
	for _, id := range agents {
		if id == "codex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex's recorded skills are unreachable from a bare command: %v", agents)
	}
}

// C2. The skills ship inside the binary. Requiring a network credential to
// unpack an embedded tree onto the local filesystem defeats the whole premise,
// and the spec asks for it to work on a plane.
func TestConnectSkillsNeedsNoCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills", "--yes"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills with no credential: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("offline skills install wrote nothing: %v", err)
	}
}

// ...and the credential gate stays exactly where it was for the capability
// that actually talks to orq. A request that has nothing left to do without a
// key still fails loudly; one that also carries skills degrades to the leg it
// can still complete (TestKeylessBareConnectStillInstallsSkills).
func TestConnectGatewayStillNeedsACredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"kimi", "gateway", "--yes"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "no saved API key") {
		t.Fatalf("gateway without a credential: err = %v, want the saved-key error", err)
	}
}

// I5. `orq connect claude bogus` errors; `orq setup --capability bogus` used to
// accept it and silently wire nothing. One grammar, one validator.
func TestSetupCapabilityFlagIsValidated(t *testing.T) {
	if _, err := validateCapabilities([]string{"bogus"}); err == nil {
		t.Error("--capability bogus was accepted")
	}
	if _, err := validateCapabilities([]string{"claude"}); err == nil {
		t.Error("--capability claude (an agent) was accepted")
	}
	caps, err := validateCapabilities([]string{"Skills", " gateway "})
	if err != nil {
		t.Fatalf("valid capabilities rejected: %v", err)
	}
	if strings.Join(caps, ",") != "skills,gateway" {
		t.Errorf("caps = %v, want the partitioner's normalized values in order", caps)
	}
}

// The validation has to sit in front of the wizard, not inside it, or a
// mistyped capability costs a full authentication round trip first.
func TestSetupRejectsAnUnknownCapabilityBeforeDoingAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	resetSetupMemos(t)

	cmd := NewSetupCommand()
	cmd.SetArgs([]string{"--capability", "bogus", "--yes"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("setup --capability bogus: err = %v, want a refusal naming bogus", err)
	}
}

// `--capability tracing` parses but is not implemented. Accepting it silently
// left setup reporting success having connected nothing at all.
func TestSetupSaysTracingIsNotAvailable(t *testing.T) {
	got := captureOutput(t, func() {
		rep := newReporter(true)
		caps, err := validateCapabilities([]string{"tracing"})
		if err != nil {
			t.Fatalf("tracing rejected outright: %v", err)
		}
		if left := dropUnavailableCaps(rep, caps); len(left) != 0 {
			t.Errorf("tracing survived the availability filter: %v", left)
		}
	})
	if !strings.Contains(got, "not available yet") {
		t.Errorf("tracing was dropped silently:\n%s", got)
	}
}

// I3. The help is the only place a user learns the capability vocabulary, and
// `orq skills` is a different noun that happens to share the word.
func TestConnectHelpExplainsCapabilitiesAndDisambiguatesOrqSkills(t *testing.T) {
	for _, cmd := range []*cobra.Command{NewConnectCommand(), NewDisconnectCommand()} {
		long := cmd.Long
		for _, want := range []string{"capabilit", capGateway, capSkills, "orq skills"} {
			if !strings.Contains(long, want) {
				t.Errorf("%s help never mentions %q:\n%s", cmd.Name(), want, long)
			}
		}
	}
}

// M1. rep.ok is suppressed without a TTY, and the emitted payload had no
// skills key, so a non-interactive `orq connect claude skills` created
// fourteen symlinks in the user's home and reported `coding_agents: null`.
// Anything scripting the CLI could not observe the capability at all.
func TestSkillsAreVisibleInTheMachineReadableOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	payload := map[string]any{}
	emitted := func(t *testing.T, run func()) map[string]any {
		t.Helper()
		var buf strings.Builder
		prev := bartolocli.Stdout
		bartolocli.Stdout = &buf
		defer func() { bartolocli.Stdout = prev }()
		run()
		out := buf.String()
		got := map[string]any{}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not JSON (%v):\n%s", err, out)
		}
		return got
	}

	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })

	payload = emitted(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "skills", "--yes"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect claude skills: %v", err)
		}
	})
	if payload["skills"] == nil {
		t.Fatalf("connect's payload never mentions the skills it installed: %v", payload)
	}

	payload = emitted(t, func() {
		d := NewDisconnectCommand()
		d.SetArgs([]string{"claude", "skills", "--yes"})
		if err := d.Execute(); err != nil {
			t.Fatalf("disconnect claude skills: %v", err)
		}
	})
	removed, _ := payload["skills"].(map[string]any)
	if removed == nil || removed["removed"] == nil {
		t.Fatalf("disconnect's payload never mentions the skills it removed: %v", payload)
	}
}

// A bare `orq connect` on a machine with no credential must still install the
// skills it can install offline. Widening the bare capability set to
// {gateway, skills} put the two fixes in each other's way: the credential gate
// fires for the whole run if any capability needs one, so a missing key aborted
// a run whose skills leg needed nothing. The gateway leg is the part that needs
// a credential; only it should be lost.
func TestKeylessBareConnectStillInstallsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"--yes"})
		if err := c.Execute(); err != nil {
			t.Fatalf("keyless bare connect: %v", err)
		}
	})
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("keyless bare connect installed nothing: %v", err)
	}
	if !strings.Contains(out, "gateway") {
		t.Errorf("the gateway leg was dropped without saying so:\n%s", out)
	}
}

// The same degradation on the interactive path: declining the login offer must
// cost the user the gateway, not the skills. connectSelected is driven directly
// because reaching the offer needs an interactive opts, which the command-level
// entry point forces off without a TTY.
func TestDecliningTheLoginStillInstallsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	// noInput false and yes false: resolveConnectAuth offers the login, and
	// opts.confirm fails closed (no TTY in a test), which is the decline.
	opts := &setupOptions{}
	cmd := NewConnectCommand()
	out := captureOutput(t, func() {
		if err := connectSelected(cmd, newReporter(false), opts,
			[]string{"claude"}, []string{capGateway, capSkills}, false); err != nil {
			t.Fatalf("declined login: %v", err)
		}
	})
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("declining the login cost the user the skills too: %v", err)
	}
	if !strings.Contains(out, "gateway") {
		t.Errorf("the gateway leg was dropped without saying so:\n%s", out)
	}
}

// Skills unpack out of this binary onto the local filesystem, so a skills-only
// run has nothing to authenticate. `orq setup --capability skills` still walked
// through step 1 and died at "no TTY available for browser login" on a machine
// with no saved credential.
func TestSetupSkillsOnlyNeedsNoCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	resetSetupMemos(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}

	cmd := NewSetupCommand()
	cmd.SetArgs([]string{"--capability", "skills", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --capability skills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %d entries, err = %v", len(entries), err)
	}
}

// mcpMachine is a machine with one agent's directory and nothing else, ready
// for a run that needs no credential: an MCP entry is a URL, so nothing in
// these tests may reach for a key.
func mcpMachine(t *testing.T, dirs ...string) (home, project string) {
	t.Helper()
	home = t.TempDir()
	project = t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ensureCreds(t)
	ensureFormatter(t)
	resetSetupMemos(t)
	return home, project
}

// claude has no gateway provider config, so a set selected on writeProvider
// alone is empty on the most common machine there is — and `orq connect mcp`
// would report no agents on a machine that plainly has one.
func TestBareMCPRunSelectsClaude(t *testing.T) {
	mcpMachine(t, ".claude")

	if got := agentsToConnect([]string{capMCP}); len(got) != 1 || got[0] != "claude" {
		t.Errorf("agentsToConnect(mcp) = %v, want [claude]", got)
	}
	// The gateway question is unchanged by this: claude still cannot receive one.
	if got := agentsToConnect([]string{capGateway}); len(got) != 0 {
		t.Errorf("agentsToConnect(gateway) = %v, want none", got)
	}
}

// pi is detected for the gateway and has no MCP support at all. Saying nothing
// would read as an entry that was written.
func TestConnectPiMCPReportsUnsupportedAndWritesNothing(t *testing.T) {
	home, project := mcpMachine(t, filepath.Join(".pi", "agent"))

	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"pi", "mcp"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if !strings.Contains(out, "no MCP support") {
		t.Errorf("pi's lack of MCP support was not reported:\n%s", out)
	}
	for _, path := range []string{
		filepath.Join(project, ".mcp.json"),
		filepath.Join(home, ".pi", "agent", "models.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was written", path)
		}
	}
}

// The scope flags decide where a write lands, and only where: the scope not
// chosen must come out of the run exactly as it went in.
func TestMCPScopeFlagsChooseOneFileEach(t *testing.T) {
	for _, tc := range []struct {
		flag         string
		wants, spare func(home, project string) string
	}{
		{
			flag:  "--local",
			wants: func(_, project string) string { return filepath.Join(project, ".mcp.json") },
			spare: func(home, _ string) string { return filepath.Join(home, ".claude.json") },
		},
		{
			flag:  "--global",
			wants: func(home, _ string) string { return filepath.Join(home, ".claude.json") },
			spare: func(_, project string) string { return filepath.Join(project, ".mcp.json") },
		},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			home, project := mcpMachine(t, ".claude")

			c := NewConnectCommand()
			c.SetArgs([]string{"claude", "mcp", tc.flag})
			if err := c.Execute(); err != nil {
				t.Fatalf("connect: %v", err)
			}
			if servers := mcpServersIn(t, tc.wants(home, project)); servers[launch.MCPServerName] == nil {
				t.Errorf("%s wrote no entry: %v", tc.flag, servers)
			}
			if _, err := os.Stat(tc.spare(home, project)); !os.IsNotExist(err) {
				t.Errorf("%s also touched the other scope", tc.flag)
			}
		})
	}
}

// The pair is mutually exclusive with a message rather than a silent precedence
// rule, and --local from $HOME is refused: the ~/.mcp.json it would produce is
// not project config, and Claude never reads it.
func TestScopeFlagsRejectWhatTheyCannotMean(t *testing.T) {
	t.Run("both named", func(t *testing.T) {
		mcpMachine(t, ".claude")
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "mcp", "--local", "--global"})
		err := c.Execute()
		if err == nil || !strings.Contains(err.Error(), "opposite") {
			t.Fatalf("err = %v, want the mutually-exclusive message", err)
		}
	})
	t.Run("local from home", func(t *testing.T) {
		home, _ := mcpMachine(t, ".claude")
		t.Chdir(home)
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "mcp", "--local"})
		err := c.Execute()
		if err == nil || !strings.Contains(err.Error(), "home directory") {
			t.Fatalf("err = %v, want the refusal naming the home directory", err)
		}
		if _, serr := os.Stat(filepath.Join(home, ".mcp.json")); !os.IsNotExist(serr) {
			t.Error("the refused run still wrote ~/.mcp.json")
		}
	})
}

// A removal that took only the global scope would leave a project entry that
// nobody can remove without remembering which scope it landed in. --status
// finds either scope and says which.
func TestDisconnectRemovesMCPFromBothScopes(t *testing.T) {
	home, project := mcpMachine(t, ".claude")

	for _, scope := range []string{"--local", "--global"} {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "mcp", scope})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect %s: %v", scope, err)
		}
	}
	local := filepath.Join(project, ".mcp.json")
	global := filepath.Join(home, ".claude.json")

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "mcp", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	for _, want := range []string{"local", "global"} {
		if !strings.Contains(out, want) {
			t.Errorf("status did not name the %s scope:\n%s", want, out)
		}
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"claude", "mcp"})
	if err := d.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	for _, path := range []string{local, global} {
		// A config with nothing left in it is removed outright, so an absent
		// file is the strongest form of "the entry is gone".
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if servers := mcpServersIn(t, path); servers[launch.MCPServerName] != nil {
			t.Errorf("%s still holds the entry: %v", path, servers)
		}
	}
}

// A self-hosted or regional install points ORQ_API_BASE_URL somewhere else, and
// the entry must follow it rather than silently staying on production. One
// derivation, shared with `orq launch`.
func TestMCPEntryFollowsTheAPIBase(t *testing.T) {
	_, project := mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_BASE_URL", "https://eu.example.internal")

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "mcp", "--local"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	entry, _ := mcpServersIn(t, filepath.Join(project, ".mcp.json"))[launch.MCPServerName].(map[string]any)
	if entry["url"] != "https://eu.example.internal/v2/mcp" {
		t.Errorf("url = %v, want the derived endpoint", entry["url"])
	}
}

// codex, opencode and kilo read MCP config from one place. --local against one
// of them is a warning and a machine-wide write, not a failure: the entry the
// user asked for still lands, in the only file that agent reads.
func TestLocalScopeAgainstAGlobalOnlyAgentWarnsAndWritesGlobal(t *testing.T) {
	home, project := mcpMachine(t, ".codex")

	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"codex", "mcp", "--local"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if !strings.Contains(out, "one place only") {
		t.Errorf("the scope was silently ignored:\n%s", out)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(home, ".codex", "config.toml"))), launch.MCPServerName) {
		t.Error("the entry did not land in codex's machine-wide config")
	}
	if _, err := os.Stat(filepath.Join(project, ".codex")); !os.IsNotExist(err) {
		t.Error("--local created a project config codex would never read")
	}
}

// The preview is a promise: wiredTargets says it "never lists a file it will
// not touch", and on the destructive verb a scoped removal that listed both
// scopes asked for consent to something it was not going to do.
func TestScopedDisconnectPreviewsOnlyThatScope(t *testing.T) {
	home, project := mcpMachine(t, ".claude")
	for _, scope := range []string{"--local", "--global"} {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "mcp", scope})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect %s: %v", scope, err)
		}
	}

	out := captureOutput(t, func() {
		d := NewDisconnectCommand()
		d.SetArgs([]string{"claude", "mcp", "--global", "--dry-run"})
		if err := d.Execute(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
	if !strings.Contains(out, ".claude.json") {
		t.Errorf("the global entry was not previewed:\n%s", out)
	}
	if strings.Contains(out, filepath.Join(project, ".mcp.json")) {
		t.Errorf("--global previewed the project entry it will not remove:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf("the dry run removed something: %v", err)
	}
}

// The removal side says the same thing the write side does: a --local removal
// that quietly took the machine-wide file is the same surprise as a write that
// did, and only the write side used to say so.
func TestScopedDisconnectWarnsForAGlobalOnlyAgent(t *testing.T) {
	mcpMachine(t, ".codex")
	c := NewConnectCommand()
	c.SetArgs([]string{"codex", "mcp"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	out := captureOutput(t, func() {
		d := NewDisconnectCommand()
		d.SetArgs([]string{"codex", "mcp", "--local"})
		if err := d.Execute(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
	if !strings.Contains(out, "one place only") {
		t.Errorf("the removal scope was silently ignored:\n%s", out)
	}
}

// skills has no project scope until RES-1437, so --local on a run that asks for
// nothing scope-capable must say the flag did nothing rather than imply it did.
func TestLocalWarnsWhenNothingInTheRunHasAScope(t *testing.T) {
	mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")

	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "skills", "--local"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if !strings.Contains(out, "only the mcp capability has a project scope") {
		t.Errorf("--local was silently ignored for a skills-only run:\n%s", out)
	}
}
