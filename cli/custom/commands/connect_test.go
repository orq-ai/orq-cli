package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/launch"

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
	if capsWereOnlyTracing([]string{"claude", "tracing", "gateway"}) {
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

	for _, args := range [][]string{{"kimi"}, {"kimi", "--yes"}} {
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

// MCP support was removed, including its removal path, so an entry wired by
// v4.13.10 stays until the user deletes it. What the CLI must not do is corrupt
// it: connect and disconnect share config files with those entries.
func TestStaleMCPEntriesAreLeftUntouched(t *testing.T) {
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

	stale := map[string]string{
		filepath.Join(home, ".claude.json"):           `{"mcpServers":{"orq":{"type":"http","url":"https://api.orq.ai/v2/mcp"}}}`,
		filepath.Join(home, ".kimi-code", "mcp.json"): `{"mcpServers":{"orq":{"url":"https://api.orq.ai/v2/mcp","bearerTokenEnvVar":"ORQ_API_KEY"}}}`,
	}
	for path, body := range stale {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{{"kimi"}, {"kimi"}} {
		cmd := NewConnectCommand()
		cmd.SetArgs(args)
		_ = cmd.Execute() // no catalogue server here; the wire may fail, the files must not change
	}
	d := NewDisconnectCommand()
	d.SetArgs([]string{"kimi"})
	_ = d.Execute()

	for path, body := range stale {
		if got := string(mustRead(t, path)); got != body {
			t.Errorf("%s changed:\n got: %s\nwant: %s", path, got, body)
		}
	}
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
	if !strings.Contains(got, "API key is untouched") {
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
	if strings.Contains(out.String(), "API key is untouched") {
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
