package custom

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"orq/cli/custom/auth"
	"orq/cli/custom/commands"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	gentleman "gopkg.in/h2non/gentleman.v2"
	gentlemancontext "gopkg.in/h2non/gentleman.v2/context"
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

func TestConfigureAPIKeyUsageNotice(t *testing.T) {
	cases := []struct {
		name, envVar, profileKey, noNotice string
		explicitKey, stderrTTY, stdoutTTY  bool
		json                               bool
		wantPending                        bool
	}{
		{name: "ORQ_API_KEY on a stderr tty", envVar: "ORQ_API_KEY", explicitKey: true, stderrTTY: true, stdoutTTY: true, wantPending: true},
		{name: "ORQ_TOKEN on a stderr tty", envVar: "ORQ_TOKEN", explicitKey: true, stderrTTY: true, stdoutTTY: true, wantPending: true},
		{name: "ORQ_AUTHORIZATION on a stderr tty", envVar: "ORQ_AUTHORIZATION", explicitKey: true, stderrTTY: true, stdoutTTY: true, wantPending: true},
		// `orq x | jq`: stdout is the pipe's, stderr is still the person's.
		{name: "stdout piped, stderr tty", envVar: "ORQ_API_KEY", explicitKey: true, stderrTTY: true, wantPending: true},
		{name: "stored profile wins", envVar: "ORQ_TOKEN", explicitKey: true, profileKey: "profile-key", stderrTTY: true, stdoutTTY: true},
		{name: "opt out", envVar: "ORQ_API_KEY", explicitKey: true, noNotice: "1", stderrTTY: true, stdoutTTY: true},
		{name: "stderr redirected", envVar: "ORQ_API_KEY", explicitKey: true, stdoutTTY: true},
		{name: "machine format", envVar: "ORQ_API_KEY", explicitKey: true, stderrTTY: true, stdoutTTY: true, json: true},
		{name: "session bridge owns exported key", envVar: "ORQ_API_KEY", stderrTTY: true, stdoutTTY: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevProfile := viper.GetString("profile")
			prevJSON := viper.GetBool("json")
			// machineFormatRequested reads output-format from viper, so an
			// inherited value would silently turn every tty case into a
			// machine-format one.
			prevFormat := viper.GetString("output-format")
			viper.Set("profile", "default")
			viper.Set("json", tc.json)
			viper.Set("output-format", "toon")
			t.Cleanup(func() {
				viper.Set("profile", prevProfile)
				viper.Set("json", prevJSON)
				viper.Set("output-format", prevFormat)
			})
			t.Setenv("ORQ_NO_API_KEY_NOTICE", tc.noNotice)
			for _, envVar := range apiKeyEnvVars {
				t.Setenv(envVar, "")
			}
			t.Setenv(tc.envVar, "sk-orq-DO-NOT-PRINT")
			commands.SetUserEnvAPIKey(os.Getenv("ORQ_API_KEY"))
			t.Cleanup(commands.ResetUserEnvAPIKey)

			creds, err := bartolocli.NewCredentialsFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			prevCreds := bartolocli.Creds
			bartolocli.Creds = creds
			t.Cleanup(func() { bartolocli.Creds = prevCreds })
			if tc.profileKey != "" {
				creds.Set("profiles.default.api_key", tc.profileKey)
			}

			root := &cobra.Command{Use: "orq"}
			root.PersistentFlags().String("output-format", "toon", "")
			cmd := &cobra.Command{Use: "models"}
			root.AddCommand(cmd)

			prevOut, prevErr := stdoutIsTerminal, stderrIsTerminal
			stdoutIsTerminal = func() bool { return tc.stdoutTTY }
			stderrIsTerminal = func() bool { return tc.stderrTTY }
			t.Cleanup(func() { stdoutIsTerminal, stderrIsTerminal = prevOut, prevErr })

			configureAPIKeyUsageNotice(cmd, tc.explicitKey)
			if got := pendingAPIKeyUsageNotice != nil; got != tc.wantPending {
				t.Errorf("pending notice = %v, want %v", got, tc.wantPending)
			}
		})
	}
}

func TestAPIKeyUsageNoticeFollowsActualRequestCredential(t *testing.T) {
	for _, envVar := range apiKeyEnvVars {
		t.Run(envVar, func(t *testing.T) {
			key := "sk-orq-" + envVar
			pendingAPIKeyUsageNotice = newAPIKeyUsageNotice(key, envVar)
			t.Cleanup(func() { pendingAPIKeyUsageNotice = nil })

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			client := gentleman.New()
			client.UseRequest(func(ctx *gentlemancontext.Context, h gentlemancontext.Handler) {
				ctx.Request.Header.Set("Authorization", "Bearer "+key)
				h.Next(ctx)
			})
			client.UseHandler("before dial", apiKeyUsageNoticeBeforeDial)

			stdout, stderr := captureOutput(t, func() {
				for range 2 {
					if _, err := client.URL(srv.URL).Get().Do(); err != nil {
						t.Fatal(err)
					}
				}
			})
			if stdout != "" {
				t.Errorf("notice wrote to stdout: %q", stdout)
			}
			if want := "Using " + envVar + " from environment\n"; stderr != want {
				t.Errorf("notice = %q, want %q", stderr, want)
			}
			if strings.Contains(stderr, key) {
				t.Errorf("notice exposed the API key: %q", stderr)
			}
		})
	}
}

func TestAPIKeyUsageNoticeIgnoresDifferentRequestCredential(t *testing.T) {
	pendingAPIKeyUsageNotice = newAPIKeyUsageNotice("environment-key", "ORQ_API_KEY")
	t.Cleanup(func() { pendingAPIKeyUsageNotice = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	client := gentleman.New()
	client.UseRequest(func(ctx *gentlemancontext.Context, h gentlemancontext.Handler) {
		ctx.Request.Header.Set("Authorization", "Bearer session-token")
		h.Next(ctx)
	})
	client.UseHandler("before dial", apiKeyUsageNoticeBeforeDial)

	_, stderr := captureOutput(t, func() {
		if _, err := client.URL(srv.URL).Get().Do(); err != nil {
			t.Fatal(err)
		}
	})
	if stderr != "" {
		t.Errorf("session-authenticated request got API-key notice: %q", stderr)
	}
}

// Precedence, and the provenance `orq doctor` reports.
func TestResolveServerPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
			name: "profile beats the env",
			env:  map[string]string{"ORQ_SERVER": "https://env.example"}, profile: "https://profile.example",
			wantServer: "https://profile.example", wantSource: "profile",
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
	previousProfile := viper.GetString("profile")
	bartolocli.SelectProfile("server-test")
	key := "profiles.server-test.server"
	prev := bartolocli.Creds.GetString(key)
	bartolocli.Creds.Set(key, server)
	t.Cleanup(func() {
		bartolocli.Creds.Set(key, prev)
		viper.Set("profile", previousProfile)
	})
}

// The notice's whole correctness argument is that it reads the credential
// bartolo actually put on the wire. The two tests above stamp the Authorization
// header themselves, so they would keep passing if Register stopped installing
// the middleware, if bartolo resolved a different credential, or if the handler
// order changed. This one runs the real chain: generated.Register installs
// bartolo's apikey handler, Register installs the notice, and a request through
// bartolocli.Client has to carry the exported key and announce it.
func TestAPIKeyUsageNoticeRidesTheRealClient(t *testing.T) {
	for _, envVar := range apiKeyEnvVars {
		t.Run(envVar, func(t *testing.T) {
			key := "sk-orq-REAL-" + envVar
			for _, name := range apiKeyEnvVars {
				t.Setenv(name, "")
			}
			t.Setenv(envVar, key)
			t.Setenv("ORQ_NO_API_KEY_NOTICE", "")

			prevProfile := viper.GetString("profile")
			prevSelected := viper.GetString("profile-selected")
			t.Cleanup(func() {
				viper.Set("profile", prevProfile)
				viper.Set("profile-selected", prevSelected)
			})
			// No profile in force, so bartolo's lookupKey falls through to the
			// environment — the state the notice exists to report.
			viper.Set("profile", "")
			viper.Set("profile-selected", "")

			creds, err := bartolocli.NewCredentialsFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			prevCreds := bartolocli.Creds
			t.Cleanup(func() { bartolocli.Creds = prevCreds })

			root := buildRoot(t)
			bartolocli.Creds = creds
			commands.SetUserEnvAPIKey(os.Getenv("ORQ_API_KEY"))
			t.Cleanup(commands.ResetUserEnvAPIKey)

			prevTTY := stderrIsTerminal
			stderrIsTerminal = func() bool { return true }
			t.Cleanup(func() { stderrIsTerminal = prevTTY })

			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			cmd, _, err := root.Find([]string{"whoami"})
			if err != nil {
				t.Fatal(err)
			}
			configureAPIKeyUsageNotice(cmd, true)
			t.Cleanup(func() { pendingAPIKeyUsageNotice = nil })

			_, stderr := captureOutput(t, func() {
				if _, err := bartolocli.Client.URL(srv.URL).Get().Do(); err != nil {
					t.Fatal(err)
				}
			})
			if gotAuth != "Bearer "+key {
				t.Fatalf("bartolo sent Authorization %q, want the exported key", gotAuth)
			}
			if want := "Using " + envVar + " from environment\n"; stderr != want {
				t.Errorf("notice = %q, want %q", stderr, want)
			}
			if strings.Contains(stderr, key) {
				t.Errorf("notice exposed the API key: %q", stderr)
			}
		})
	}
}
