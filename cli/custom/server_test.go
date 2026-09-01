package custom

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// The user-visible contract of ENG-2852: one name for the host on every
// command. `--server` reaches all six commands that used to own
// `--api-base-url`, and that flag survives one release as a deprecated,
// help-hidden alias rather than breaking scripts on upgrade.
func TestServerFlagReplacesAPIBaseURL(t *testing.T) {
	root := buildRoot(t)
	for _, path := range [][]string{
		{"auth", "login"}, {"auth", "logout"}, {"whoami"},
		{"workspace", "list"}, {"workspace", "use"}, {"doctor"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if f := cmd.InheritedFlags().Lookup("server"); f == nil {
			t.Errorf("%v does not inherit --server", path)
		}
		f := cmd.Flags().Lookup("api-base-url")
		if f == nil {
			t.Errorf("%v dropped --api-base-url with no deprecation release", path)
			continue
		}
		// Hidden, and NOT cobra-deprecated: pflag's own notice goes to stdout
		// and would corrupt --json. resolveServer warns on stderr instead.
		if !f.Hidden {
			t.Errorf("%v: --api-base-url must be hidden from help", path)
		}
		if f.Deprecated != "" {
			t.Errorf("%v: cobra's deprecation notice prints to stdout; warn from resolveServer instead", path)
		}
	}
}

// The deprecated flag still routes, below an explicit --server, and its warning
// goes to stderr: on stdout it would corrupt --json.
func TestDeprecatedAPIBaseFlagResolves(t *testing.T) {
	root := buildRoot(t)
	t.Cleanup(func() { auth.SetServer("", "default") })
	cmd, _, err := root.Find([]string{"whoami"})
	if err != nil {
		t.Fatal(err)
	}
	setDeprecatedFlag(t, cmd, "https://legacy-flag.example")

	auth.SetServer("", "default")
	stdout, stderr := captureOutput(t, func() { resolveServer(cmd) })
	if got := auth.Server(); got != "https://legacy-flag.example" {
		t.Fatalf("server: got %q", got)
	}
	if got := auth.ServerSource(); got != "flag" {
		t.Fatalf("source: got %q", got)
	}
	if !strings.Contains(stderr, "--api-base-url is deprecated") {
		t.Errorf("deprecation warning missing from stderr: %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout must stay clean for --json, got %q", stdout)
	}

	// An explicit --server outranks the alias.
	flags := root.PersistentFlags()
	if err := flags.Set("server", "https://explicit.example"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flags.Lookup("server").Changed = false })
	resolveServer(cmd)
	if got := auth.Server(); got != "https://explicit.example" {
		t.Fatalf("--server must beat --api-base-url, got %q", got)
	}
}

// setDeprecatedFlag sets the hidden alias and restores it afterwards: buildRoot
// hands out the process-global root, so a flag left Changed would resolve for
// every later test in this package.
func setDeprecatedFlag(t *testing.T, cmd *cobra.Command, value string) {
	t.Helper()
	f := cmd.Flags().Lookup("api-base-url")
	if f == nil {
		t.Fatalf("%s has no --api-base-url", cmd.Name())
	}
	prev := f.Value.String()
	if err := cmd.Flags().Set("api-base-url", value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("api-base-url", prev)
		f.Changed = false
	})
}

// captureOutput swaps both bartolo writers for buffers for the duration of fn.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	prevOut, prevErr := bartolocli.Stdout, bartolocli.Stderr
	var out, errBuf bytes.Buffer
	bartolocli.Stdout, bartolocli.Stderr = &out, &errBuf
	defer func() { bartolocli.Stdout, bartolocli.Stderr = prevOut, prevErr }()
	fn()
	return out.String(), errBuf.String()
}

// Precedence, and the provenance `orq doctor` reports. The session is layered
// on by the PreRun itself, below every source here.
func TestResolveServerPrecedence(t *testing.T) {
	root := buildRoot(t)
	t.Cleanup(func() {
		auth.SetServer("", "default")
		viper.Set("server", "") // viper is process-global; do not leak a host
	})

	cases := []struct {
		name         string
		flag         string
		env          map[string]string
		profile      string
		config       string
		legacyConfig string
		wantServer   string
		wantSource   string
	}{
		{name: "nothing set", wantServer: "", wantSource: "default"},
		{name: "config", config: "https://config.example", wantServer: "https://config.example", wantSource: "config"},
		{
			// A config.json written before bartolo moved the key, which its
			// migration has not rewritten yet.
			name:         "legacy config key",
			legacyConfig: "https://legacy-config.example",
			wantServer:   "https://legacy-config.example", wantSource: "config",
		},
		{
			name:   "migrated key beats the legacy one",
			config: "https://config.example", legacyConfig: "https://legacy-config.example",
			wantServer: "https://config.example", wantSource: "config",
		},
		{
			// Selecting a profile is how you select a backend, so a host bound
			// to one outranks a host persisted globally with `orq server set`.
			name:    "profile beats the global config",
			profile: "https://profile.example", config: "https://config.example",
			wantServer: "https://profile.example", wantSource: "profile",
		},
		{
			name: "env beats the profile",
			env:  map[string]string{"ORQ_SERVER": "https://env.example"}, profile: "https://profile.example",
			wantServer: "https://env.example", wantSource: "env",
		},
		{name: "env", env: map[string]string{"ORQ_SERVER": "https://env.example"}, wantServer: "https://env.example", wantSource: "env"},
		{name: "deprecated env", env: map[string]string{"ORQ_API_BASE_URL": "https://legacy.example"}, wantServer: "https://legacy.example", wantSource: "env"},
		{
			name:       "ORQ_SERVER beats the deprecated spelling",
			env:        map[string]string{"ORQ_SERVER": "https://env.example", "ORQ_API_BASE_URL": "https://legacy.example"},
			wantServer: "https://env.example", wantSource: "env",
		},
		{
			name: "flag beats everything",
			flag: "https://flag.example",
			env:  map[string]string{"ORQ_SERVER": "https://env.example"},
			// A flag whose value equals the session host must still report as
			// a flag, so doctor cannot infer provenance from the value.
			wantServer: "https://flag.example", wantSource: "flag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A stale value, so "nothing set" asserts that resolveServer clears
			// rather than that it happened to leave the priming value alone.
			auth.SetServer("https://stale.example", "flag")
			// server-default is the key bartolo persists `orq server set` under.
			// Driving the old `server` key here made every config case test the
			// legacy fallback, so the resolver reading the wrong key passed.
			viper.Set("server-default", tc.config)
			viper.Set("server", tc.legacyConfig)
			t.Cleanup(func() { viper.Set("server-default", ""); viper.Set("server", "") })
			setProfileServer(t, tc.profile)
			t.Setenv("ORQ_SERVER", tc.env["ORQ_SERVER"])
			t.Setenv("ORQ_API_BASE_URL", tc.env["ORQ_API_BASE_URL"])
			flags := root.PersistentFlags()
			if err := flags.Set("server", tc.flag); err != nil {
				t.Fatal(err)
			}
			flags.Lookup("server").Changed = tc.flag != ""
			t.Cleanup(func() { flags.Lookup("server").Changed = false })

			resolveServer(root)
			if got := auth.Server(); got != tc.wantServer {
				t.Errorf("server: got %q, want %q", got, tc.wantServer)
			}
			if got := auth.ServerSource(); got != tc.wantSource {
				t.Errorf("source: got %q, want %q", got, tc.wantSource)
			}
			// The generated commands read viper directly; they must land on
			// the same host rather than their own OpenAPI default.
			if tc.wantServer != "" && viper.GetString("server") != tc.wantServer {
				t.Errorf("viper mirror: got %q, want %q", viper.GetString("server"), tc.wantServer)
			}
		})
	}
}

// setProfileServer binds a host to the active credentials profile for one test.
func setProfileServer(t *testing.T, server string) {
	t.Helper()
	key := "profiles." + auth.ActiveProfile() + ".server"
	prev := bartolocli.Creds.GetString(key)
	bartolocli.Creds.Set(key, server)
	t.Cleanup(func() { bartolocli.Creds.Set(key, prev) })
}

// An explicitly typed --profile is a more specific statement of intent than an
// exported key, so the profile's own key must reach the request instead.
func TestProfileAPIKeyBeatsTheEnvironment(t *testing.T) {
	root := buildRoot(t)
	profile := auth.ActiveProfile()
	key := "profiles." + profile + ".api_key"
	prev := bartolocli.Creds.GetString(key)
	bartolocli.Creds.Set(key, "profile-key")
	t.Cleanup(func() { bartolocli.Creds.Set(key, prev) })

	flags := root.PersistentFlags()
	t.Cleanup(func() { flags.Lookup("profile").Changed = false })

	// Without the flag, env versus env is bartolo's call and nothing moves.
	t.Setenv("ORQ_API_KEY", "env-key")
	flags.Lookup("profile").Changed = false
	applyProfileAPIKey(root)
	if got := os.Getenv("ORQ_API_KEY"); got != "env-key" {
		t.Fatalf("no --profile: got %q, want the environment untouched", got)
	}

	// With it, the profile wins and says so on stderr. The flag carries the
	// name: bartolo 0.8 retired the implicit `default` profile, so a changed
	// but empty --profile puts no profile in force.
	t.Setenv("ORQ_TOKEN", "env-token")
	if err := flags.Set("profile", profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flags.Set("profile", "") })
	flags.Lookup("profile").Changed = true
	_, stderr := captureOutput(t, func() { applyProfileAPIKey(root) })
	if got := os.Getenv("ORQ_API_KEY"); got != "profile-key" {
		t.Fatalf("--profile: got %q, want the profile key", got)
	}
	// The other spellings are cleared, or bartolo would read them first.
	if got := os.Getenv("ORQ_TOKEN"); got != "" {
		t.Errorf("ORQ_TOKEN: got %q, want it cleared", got)
	}
	if !strings.Contains(stderr, "takes precedence") {
		t.Errorf("no warning for the shadowed key: %q", stderr)
	}
}
