package custom

import (
	"testing"

	"orq/cli/custom/auth"

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

// The deprecated flag still routes, below an explicit --server.
func TestDeprecatedAPIBaseFlagResolves(t *testing.T) {
	root := buildRoot(t)
	t.Cleanup(func() { auth.SetServer("", "default") })
	cmd, _, err := root.Find([]string{"whoami"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("api-base-url", "https://legacy-flag.example"); err != nil {
		t.Fatal(err)
	}
	auth.SetServer("", "default")
	resolveServer(cmd)
	if got := auth.Server(); got != "https://legacy-flag.example" {
		t.Fatalf("server: got %q", got)
	}
	if got := auth.ServerSource(); got != "flag" {
		t.Fatalf("source: got %q", got)
	}
}

// Precedence, and the provenance `orq doctor` reports. The session is layered
// on by the PreRun itself, below every source here.
func TestResolveServerPrecedence(t *testing.T) {
	root := buildRoot(t)
	t.Cleanup(func() { auth.SetServer("", "default") })

	cases := []struct {
		name       string
		flag       string
		env        map[string]string
		config     string
		wantServer string
		wantSource string
	}{
		{name: "nothing set", wantServer: "", wantSource: "default"},
		{name: "config", config: "https://config.example", wantServer: "https://config.example", wantSource: "config"},
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
			auth.SetServer("", "default")
			viper.Set("server", tc.config)
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
