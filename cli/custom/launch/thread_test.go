package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/auth"
)

func TestParseArgvSessionFlagsBothForms(t *testing.T) {
	for _, argv := range [][]string{
		{"--mcp-auth", "env", "--thread-id", "run-1", "--metadata", "suite=smoke", "--metadata", "case=a=b", "rest"},
		{"--mcp-auth=env", "--thread-id=run-1", "--metadata=suite=smoke", "--metadata=case=a=b", "rest"},
	} {
		flags, rest, err := ParseArgv(argv, FindAgent("claude").ParseOptions())
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		want := []RequestHeader{
			{Name: "X-ORQ-METADATA-suite", Value: "smoke"},
			{Name: "X-ORQ-METADATA-case", Value: "a=b"},
		}
		if !flags.MCPEnvAuth || flags.ThreadID != "run-1" || !reflect.DeepEqual(flags.Metadata, want) {
			t.Errorf("%v: flags = %+v", argv, flags)
		}
		if !reflect.DeepEqual(rest, []string{"rest"}) {
			t.Errorf("%v: rest = %v", argv, rest)
		}
	}
}

func TestParseArgvRefusesBadSessionFlagValues(t *testing.T) {
	tooManyKeys := make([]string, 0, 2*(maxMetadataKeys+1))
	for i := range maxMetadataKeys + 1 {
		tooManyKeys = append(tooManyKeys, "--metadata", fmt.Sprintf("key%d=v", i))
	}
	for name, argv := range map[string][]string{
		"unknown mode":        {"--mcp-auth", "token"},
		"empty mode":          {"--mcp-auth="},
		"mode missing":        {"--mcp-auth"},
		"thread id missing":   {"--thread-id"},
		"thread id is a flag": {"--thread-id", "--no-mcp"},
		"thread id dash":      {"--thread-id=-x"},
		"thread id comma":     {"--thread-id=a,b"},
		"thread id variable":  {"--thread-id={env:HOME}"},
		"thread id spaces":    {"--thread-id= x"},
		"thread id non-ascii": {"--thread-id=héllo"},
		"thread id newline":   {"--thread-id=a\nb"},
		"thread id length":    {"--thread-id=" + strings.Repeat("t", maxThreadIDLength+1)},
		"metadata no equals":  {"--metadata", "suite"},
		"metadata no key":     {"--metadata", "=v"},
		"metadata no value":   {"--metadata", "k="},
		"metadata underscore": {"--metadata", "under_score=v"},
		"metadata hyphens":    {"--metadata", "a--b=v"},
		"metadata key length": {"--metadata", strings.Repeat("k", maxMetadataKeyLength+1) + "=v"},
		"metadata value size": {"--metadata", "k=" + strings.Repeat("v", maxMetadataValueLength+1)},
		"metadata comma":      {"--metadata", "k=a,b"},
		"metadata reserved":   {"--metadata", "repo=x"},
		"metadata mixed case": {"--metadata", "User-Agent=x"},
		"metadata repeated":   {"--metadata", "SUITE=a", "--metadata", "suite=b"},
		"metadata too many":   tooManyKeys,
	} {
		if _, _, err := ParseArgv(argv, FindAgent("claude").ParseOptions()); err == nil {
			t.Errorf("%s: %v was accepted", name, argv)
		}
	}
}

func TestParseArgvAcceptsMetadataAtTheGatewayLimits(t *testing.T) {
	argv := []string{"--metadata", strings.Repeat("k", maxMetadataKeyLength) + "=" + strings.Repeat("v", maxMetadataValueLength)}
	for i := range maxMetadataKeys - 1 {
		argv = append(argv, "--metadata", fmt.Sprintf("key%d=v", i))
	}
	flags, _, err := ParseArgv(argv, FindAgent("claude").ParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(flags.Metadata) != maxMetadataKeys {
		t.Fatalf("got %d keys", len(flags.Metadata))
	}
}

func TestParseArgvAcceptsAThreadIDAtTheLimit(t *testing.T) {
	id := strings.Repeat("t", maxThreadIDLength)
	flags, _, err := ParseArgv([]string{"--thread-id", id}, FindAgent("claude").ParseOptions())
	if err != nil || flags.ThreadID != id {
		t.Fatalf("%q %v", flags.ThreadID, err)
	}
}

// A key on this list that the pattern refuses can never be typed, so it would
// protect nothing while looking as if it did.
func TestReservedMetadataKeysAreReachable(t *testing.T) {
	for _, key := range reservedMetadataKeys {
		if key != strings.ToLower(key) || !metadataKeyPattern.MatchString(key) {
			t.Errorf("reserved key %q can never match a --metadata key", key)
		}
	}
}

// --gateway, --no-gateway, --mcp and --no-mcp are last-wins, so the check has to
// see the final state in either flag order.
func TestParseArgvChecksSessionFlagsAgainstTheFinalState(t *testing.T) {
	opts := FindAgent("claude").ParseOptions()
	for _, argv := range [][]string{
		{"--thread-id", "x", "--no-gateway"},
		{"--no-gateway", "--thread-id", "x"},
		{"--metadata", "k=v", "--no-gateway"},
		{"--mcp-auth", "env", "--no-mcp"},
		{"--no-mcp", "--mcp-auth", "env"},
	} {
		if _, _, err := ParseArgv(argv, opts); err == nil {
			t.Errorf("%v was accepted", argv)
		}
	}
	for _, argv := range [][]string{
		{"--no-gateway", "--thread-id", "x", "--gateway"},
		{"--no-mcp", "--mcp-auth", "env", "--mcp"},
		{"--mcp-auth", "env", "--no-gateway"},
		{"--mcp-auth", "oauth", "--no-mcp"},
	} {
		if _, _, err := ParseArgv(argv, opts); err != nil {
			t.Errorf("%v: %v", argv, err)
		}
	}
}

func TestParseArgvScopesSessionFlagsToAgentsThatHonourThem(t *testing.T) {
	supported := []string{"claude", "codex", "opencode", "kilo", "gemini"}
	for _, def := range Agents() {
		opts := def.ParseOptions()
		_, _, authErr := ParseArgv([]string{"--mcp-auth", "env"}, opts)
		_, _, threadErr := ParseArgv([]string{"--thread-id", "x"}, opts)
		_, _, metadataErr := ParseArgv([]string{"--metadata", "k=v"}, opts)
		if slices.Contains(supported, def.Name) {
			// Router defaults off for every agent but claude, and that must not
			// read as --no-gateway.
			if authErr != nil || threadErr != nil || metadataErr != nil {
				t.Errorf("%s: %v / %v / %v", def.Name, authErr, threadErr, metadataErr)
			}
		} else if authErr == nil || threadErr == nil || metadataErr == nil {
			t.Errorf("%s: accepted a flag launch cannot honour there", def.Name)
		}

		// Help lists --mcp-auth only where env is supported, so elsewhere even
		// oauth is more likely the agent's own flag than launch's.
		_, _, oauthErr := ParseArgv([]string{"--mcp-auth", "oauth"}, opts)
		if supports := slices.Contains(supported, def.Name); supports != (oauthErr == nil) {
			t.Errorf("%s: --mcp-auth oauth: %v", def.Name, oauthErr)
		}
		flags, _, err := ParseArgv([]string{"--mcp-auth", "env", "--thread-id", "x", "--help"}, opts)
		if err != nil || !flags.Help {
			t.Errorf("%s: help must win: %+v %v", def.Name, flags, err)
		}
	}
}

func TestParseArgvLeavesAgentOwnedSessionFlagsAlone(t *testing.T) {
	opts := FindAgent("claude").ParseOptions()
	for _, tc := range []struct{ argv, rest []string }{
		{[]string{"exec", "--thread-id", "x"}, []string{"exec", "--thread-id", "x"}},
		{[]string{"--", "--metadata", "k=v"}, []string{"--metadata", "k=v"}},
	} {
		flags, rest, err := ParseArgv(tc.argv, opts)
		if err != nil {
			t.Fatalf("%v: %v", tc.argv, err)
		}
		if flags.ThreadID != "" || flags.Metadata != nil {
			t.Errorf("%v: launcher took an agent flag: %+v", tc.argv, flags)
		}
		if !reflect.DeepEqual(rest, tc.rest) {
			t.Errorf("%v: rest = %v", tc.argv, rest)
		}
	}
}

func TestMergeHeaderEnv(t *testing.T) {
	headers := []RequestHeader{{Name: threadIDHeader, Value: "run-1"}, {Name: "X-ORQ-METADATA-suite", Value: "smoke"}}
	for name, tc := range map[string]struct {
		existing string
		delim    rune
		want     string
	}{
		"empty": {"", '\n', "X-ORQ-THREAD-ID: run-1\nX-ORQ-METADATA-suite: smoke"},
		"kept byte for byte": {"Foo:bar\nBaz:  qux ", '\n',
			"Foo:bar\nBaz:  qux \nX-ORQ-THREAD-ID: run-1\nX-ORQ-METADATA-suite: smoke"},
		"replaced whatever the case": {"x-orq-thread-id: old\nFoo: bar", '\n',
			"Foo: bar\nX-ORQ-THREAD-ID: run-1\nX-ORQ-METADATA-suite: smoke"},
		"comma list": {"Foo: bar", ',', "Foo: bar,X-ORQ-THREAD-ID: run-1,X-ORQ-METADATA-suite: smoke"},
		"comma inside a kept value": {"Foo: a,b,X-ORQ-THREAD-ID: old", ',',
			"Foo: a,b,X-ORQ-THREAD-ID: run-1,X-ORQ-METADATA-suite: smoke"},
	} {
		if got := mergeHeaderEnv(tc.existing, tc.delim, headers); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}

// resolveSession resolves an agent for the session flags with the side effects
// this file does not test turned off.
func resolveSession(t *testing.T, agent string, flags GatewayFlags, shell map[string]string) *LaunchPlan {
	t.Helper()
	def := FindAgent(agent)
	flags.NoSkills = true
	plan, err := def.Resolve(&AgentContext{
		Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(shell),
		Flags:  flags,
		Fetch: func(_, _ string) ([]ModelInfo, error) {
			return []ModelInfo{{ID: "openai/gpt-5.6-terra"}, {ID: "anthropic/claude-sonnet-4-6"}, {ID: "google/gemini-3.5-flash"}}, nil
		},
		ExecProbe: func(string, ...string) (string, error) { return "", io.EOF },
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		t.Cleanup(plan.Cleanup)
	}
	return plan
}

func claudeMCPConfig(t *testing.T, plan *LaunchPlan) string {
	t.Helper()
	i := slices.Index(plan.PreArgs, "--mcp-config")
	if i < 0 {
		return ""
	}
	data, err := os.ReadFile(plan.PreArgs[i+1])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMCPEnvAuthEntryNamesTheVariableForEachAgent(t *testing.T) {
	on := GatewayFlags{MCP: true, MCPEnvAuth: true}
	off := GatewayFlags{MCP: true}

	t.Run("claude", func(t *testing.T) {
		var doc struct {
			MCPServers map[string]struct {
				Type    string            `json:"type"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(claudeMCPConfig(t, resolveSession(t, "claude", on, nil))), &doc); err != nil {
			t.Fatal(err)
		}
		entry := doc.MCPServers[MCPServerName]
		if entry.Headers["Authorization"] != "Bearer ${ORQ_API_KEY}" || entry.Type != "http" || entry.URL == "" {
			t.Errorf("entry = %+v", entry)
		}
		if got := claudeMCPConfig(t, resolveSession(t, "claude", off, nil)); strings.Contains(got, "Authorization") {
			t.Errorf("default entry carries auth: %s", got)
		}
	})

	t.Run("codex", func(t *testing.T) {
		want := `mcp_servers.` + MCPServerName + `.bearer_token_env_var="ORQ_API_KEY"`
		plan := resolveSession(t, "codex", on, nil)
		if !slices.Contains(plan.PreArgs, want) {
			t.Errorf("no %s in %v", want, plan.PreArgs)
		}
		if plan = resolveSession(t, "codex", off, nil); slices.Contains(plan.PreArgs, want) {
			t.Errorf("default args carry it: %v", plan.PreArgs)
		}
	})

	for agent, envVar := range map[string]string{"opencode": "OPENCODE_CONFIG_CONTENT", "kilo": "KILO_CONFIG_CONTENT"} {
		t.Run(agent, func(t *testing.T) {
			var doc struct {
				MCP map[string]map[string]any `json:"mcp"`
			}
			if err := json.Unmarshal([]byte(resolveSession(t, agent, on, nil).Env[envVar]), &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc.MCP[MCPServerName]
			if oauth, ok := entry["oauth"]; !ok || oauth != false {
				t.Errorf("oauth = %v, %v", oauth, ok)
			}
			headers, _ := entry["headers"].(map[string]any)
			if headers["Authorization"] != "Bearer {env:ORQ_API_KEY}" {
				t.Errorf("headers = %v", headers)
			}
			if got := resolveSession(t, agent, off, nil).Env[envVar]; strings.Contains(got, "Authorization") {
				t.Errorf("default config carries auth: %s", got)
			}
		})
	}

	t.Run("gemini", func(t *testing.T) {
		read := func(flags GatewayFlags) string {
			data, err := os.ReadFile(filepath.Join(resolveSession(t, "gemini", flags, nil).Env["GEMINI_CLI_HOME"], ".gemini", "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		if got := read(on); !strings.Contains(got, `"Authorization": "Bearer $ORQ_API_KEY"`) {
			t.Errorf("settings = %s", got)
		}
		if got := read(off); strings.Contains(got, "Authorization") {
			t.Errorf("default settings carry auth: %s", got)
		}
	})
}

func TestMCPEnvAuthWritesOverAPersistedEntry(t *testing.T) {
	PersistedMCPHook = func(string) bool { return true }
	t.Cleanup(func() { PersistedMCPHook = nil })
	on := GatewayFlags{MCP: true, MCPEnvAuth: true}
	off := GatewayFlags{MCP: true}

	if claudeMCPConfig(t, resolveSession(t, "claude", off, nil)) != "" {
		t.Error("claude: the persisted entry must suppress the default session entry")
	}
	if !strings.Contains(claudeMCPConfig(t, resolveSession(t, "claude", on, nil)), "${ORQ_API_KEY}") {
		t.Error("claude: --mcp-auth env must write a session entry")
	}
	hasCodexEntry := func(plan *LaunchPlan) bool {
		return slices.ContainsFunc(plan.PreArgs, func(a string) bool { return strings.HasPrefix(a, "mcp_servers.") })
	}
	if hasCodexEntry(resolveSession(t, "codex", off, nil)) {
		t.Error("codex: the persisted entry must suppress the default session entry")
	}
	if !hasCodexEntry(resolveSession(t, "codex", on, nil)) {
		t.Error("codex: --mcp-auth env must write a session entry")
	}
	if strings.Contains(resolveSession(t, "opencode", off, nil).Env["OPENCODE_CONFIG_CONTENT"], `"mcp"`) {
		t.Error("opencode: the persisted entry must suppress the default session entry")
	}
	if !strings.Contains(resolveSession(t, "opencode", on, nil).Env["OPENCODE_CONFIG_CONTENT"], `"mcp"`) {
		t.Error("opencode: --mcp-auth env must write a session entry")
	}
	if strings.Contains(resolveSession(t, "kilo", off, nil).Env["KILO_CONFIG_CONTENT"], `"mcp"`) {
		t.Error("kilo: the persisted entry must suppress the default session entry")
	}
	if !strings.Contains(resolveSession(t, "kilo", on, nil).Env["KILO_CONFIG_CONTENT"], `"mcp"`) {
		t.Error("kilo: --mcp-auth env must write a session entry")
	}
}

func TestRequestHeadersReachEachAgent(t *testing.T) {
	metadata := []RequestHeader{{Name: metadataHeaderPrefix + "suite", Value: "42"}}
	flags := GatewayFlags{Router: true, ThreadID: "123", Metadata: metadata}

	t.Run("claude", func(t *testing.T) {
		plan := resolveSession(t, "claude", flags, map[string]string{claudeHeadersEnv: "Foo: bar"})
		want := "Foo: bar\nX-ORQ-THREAD-ID: 123\nX-ORQ-METADATA-suite: 42"
		if got := plan.Env[claudeHeadersEnv]; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// The shell's value stays ignored under --subscription, as it is without
	// these flags, and the orq key entry survives the merge.
	t.Run("claude subscription", func(t *testing.T) {
		withHeaders := flags
		withHeaders.Subscription = true
		plan := resolveSession(t, "claude", withHeaders, map[string]string{claudeHeadersEnv: "Foo: bar"})
		want := "x-orq-api-key: sk-test\nX-ORQ-THREAD-ID: 123\nX-ORQ-METADATA-suite: 42"
		if got := plan.Env[claudeHeadersEnv]; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// The session trace is the second copy of a claude run, so a caller who looks
	// up by id has to be told it is not under that id.
	t.Run("claude session trace", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		notice := func(flags GatewayFlags) bool {
			flags.Trace = true
			return slices.ContainsFunc(resolveSession(t, "claude", flags, nil).Warnings, func(w string) bool {
				return strings.Contains(w, "tag the gateway calls only")
			})
		}
		if !notice(flags) {
			t.Error("no notice with a thread id")
		}
		if notice(GatewayFlags{Router: true}) {
			t.Error("notice with no thread id or metadata")
		}
	})

	// The provider never sees the headers, so starting anyway would lose the
	// tags with only a warning to say so; --no-gateway is refused for the same reason.
	t.Run("claude provider switch", func(t *testing.T) {
		for _, f := range []GatewayFlags{
			{Router: true, ThreadID: "123"},
			{Router: true, Metadata: metadata},
		} {
			f.NoSkills = true
			_, err := FindAgent("claude").Resolve(&AgentContext{
				Creds:  &Credentials{APIKey: "sk-test", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
				Getenv: env(map[string]string{claudeProviderSwitches[0]: "1"}),
				Flags:  f,
			})
			if err == nil || !strings.Contains(err.Error(), claudeProviderSwitches[0]) {
				t.Errorf("%+v: err = %v", f, err)
			}
		}
		// Without the flags the switch is the user's own arrangement and only warns.
		plan := resolveSession(t, "claude", GatewayFlags{Router: true}, map[string]string{claudeProviderSwitches[0]: "1"})
		if !slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, claudeProviderSwitches[0]) }) {
			t.Errorf("no warning naming %s in %v", claudeProviderSwitches[0], plan.Warnings)
		}
	})

	t.Run("codex", func(t *testing.T) {
		plan := resolveSession(t, "codex", flags, nil)
		// Quoted: a bare 123 would be a TOML integer in a table of strings.
		for _, want := range []string{
			`model_providers.orq.http_headers.X-ORQ-THREAD-ID="123"`,
			`model_providers.orq.http_headers.X-ORQ-METADATA-suite="42"`,
		} {
			if !slices.Contains(plan.PreArgs, want) {
				t.Errorf("no %s in %v", want, plan.PreArgs)
			}
		}
	})

	for agent, envVar := range map[string]string{"opencode": "OPENCODE_CONFIG_CONTENT", "kilo": "KILO_CONFIG_CONTENT"} {
		t.Run(agent, func(t *testing.T) {
			var doc struct {
				Provider map[string]struct {
					Options struct {
						Headers map[string]string `json:"headers"`
					} `json:"options"`
				} `json:"provider"`
			}
			if err := json.Unmarshal([]byte(resolveSession(t, agent, flags, nil).Env[envVar]), &doc); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"X-ORQ-THREAD-ID": "123", "X-ORQ-METADATA-suite": "42"}
			if len(doc.Provider) == 0 {
				t.Fatal("no providers")
			}
			for name, provider := range doc.Provider {
				if !reflect.DeepEqual(provider.Options.Headers, want) {
					t.Errorf("%s: headers = %v", name, provider.Options.Headers)
				}
			}
		})
	}

	t.Run("gemini", func(t *testing.T) {
		plan := resolveSession(t, "gemini", flags, map[string]string{geminiHeadersEnv: "Foo: bar"})
		want := "Foo: bar,X-ORQ-THREAD-ID: 123,X-ORQ-METADATA-suite: 42"
		if got := plan.Env[geminiHeadersEnv]; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// A value with a quote, a backslash or colons has to arrive unchanged in each
// agent's own encoding: TOML for codex, JSON for opencode and kilo, a header line
// for claude and gemini.
func TestMetadataValueWithSpecialCharactersSurvives(t *testing.T) {
	const value = `a"b\c:d:e`
	parsed, _, err := ParseArgv([]string{"--metadata", "q=" + value}, FindAgent("claude").ParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	flags := GatewayFlags{Router: true, Metadata: parsed.Metadata}
	name := metadataHeaderPrefix + "q"

	if got := resolveSession(t, "claude", flags, nil).Env[claudeHeadersEnv]; got != name+": "+value {
		t.Errorf("claude: %q", got)
	}
	if got := resolveSession(t, "gemini", GatewayFlags{Router: true, ThreadID: "t", Metadata: parsed.Metadata}, nil).Env[geminiHeadersEnv]; !strings.HasSuffix(got, ","+name+": "+value) {
		t.Errorf("gemini: %q", got)
	}
	codexArg := "model_providers.orq.http_headers." + name + "=" + TOMLString(value)
	if want := `"a\"b\\c:d:e"`; TOMLString(value) != want {
		t.Errorf("TOMLString = %s, want %s", TOMLString(value), want)
	}
	if plan := resolveSession(t, "codex", flags, nil); !slices.Contains(plan.PreArgs, codexArg) {
		t.Errorf("codex: no %s in %v", codexArg, plan.PreArgs)
	}
	for agent, envVar := range map[string]string{"opencode": "OPENCODE_CONFIG_CONTENT", "kilo": "KILO_CONFIG_CONTENT"} {
		var doc struct {
			Provider map[string]struct {
				Options struct {
					Headers map[string]string `json:"headers"`
				} `json:"options"`
			} `json:"provider"`
		}
		if err := json.Unmarshal([]byte(resolveSession(t, agent, flags, nil).Env[envVar]), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Provider) == 0 {
			t.Fatalf("%s: no providers", agent)
		}
		for provider, p := range doc.Provider {
			if p.Options.Headers[name] != value {
				t.Errorf("%s %s: %q", agent, provider, p.Options.Headers[name])
			}
		}
	}
}

// gemini-cli sends no session id, so it is the one agent that gets a generated
// thread id. An id the caller chose, by flag or in the variable, stays.
func TestGeminiGetsAGeneratedThreadID(t *testing.T) {
	plain := GatewayFlags{Router: true}
	threadID := func(t *testing.T, list string) string {
		t.Helper()
		var ids []string
		for _, entry := range strings.Split(list, ",") {
			name, value, _ := strings.Cut(entry, ":")
			if strings.EqualFold(strings.TrimSpace(name), threadIDHeader) {
				ids = append(ids, strings.TrimSpace(value))
			}
		}
		if len(ids) != 1 {
			t.Fatalf("thread ids in %q: %v", list, ids)
		}
		return ids[0]
	}
	// The id is only useful if the caller can see which one this run used.
	printsID := func(plan *LaunchPlan, id string) bool {
		return slices.ContainsFunc(plan.Warnings, func(w string) bool {
			return strings.Contains(w, "generated thread id "+id)
		})
	}

	t.Run("no id given", func(t *testing.T) {
		plan := resolveSession(t, "gemini", plain, map[string]string{geminiHeadersEnv: "Foo: bar"})
		list := plan.Env[geminiHeadersEnv]
		if !strings.HasPrefix(list, "Foo: bar,") {
			t.Errorf("the caller's header is gone: %q", list)
		}
		first := threadID(t, list)
		if err := checkHeaderValue("--thread-id", first); err != nil {
			t.Errorf("generated id %q: %v", first, err)
		}
		if !printsID(plan, first) {
			t.Errorf("id %q is not printed: %v", first, plan.Warnings)
		}
		if second := threadID(t, resolveSession(t, "gemini", plain, nil).Env[geminiHeadersEnv]); second == first {
			t.Errorf("two launches share the id %q", first)
		}
	})
	t.Run("exported id stays", func(t *testing.T) {
		flags := plain
		flags.Metadata = []RequestHeader{{Name: "X-ORQ-METADATA-suite", Value: "42"}}
		plan := resolveSession(t, "gemini", flags, map[string]string{geminiHeadersEnv: "x-orq-thread-id: mine"})
		list := plan.Env[geminiHeadersEnv]
		if got := threadID(t, list); got != "mine" {
			t.Errorf("got %q from %q", got, list)
		}
		if printsID(plan, "") {
			t.Errorf("a generated id is reported: %v", plan.Warnings)
		}
	})
	t.Run("blank exported id holds none", func(t *testing.T) {
		list := resolveSession(t, "gemini", plain, map[string]string{geminiHeadersEnv: "X-ORQ-THREAD-ID: "}).Env[geminiHeadersEnv]
		if threadID(t, list) == "" {
			t.Errorf("no id in %q", list)
		}
	})
	t.Run("flag beats exported id", func(t *testing.T) {
		flags := plain
		flags.ThreadID = "flag"
		plan := resolveSession(t, "gemini", flags, map[string]string{geminiHeadersEnv: "X-ORQ-THREAD-ID: mine"})
		list := plan.Env[geminiHeadersEnv]
		if got := threadID(t, list); got != "flag" {
			t.Errorf("got %q from %q", got, list)
		}
		if printsID(plan, "") {
			t.Errorf("a generated id is reported: %v", plan.Warnings)
		}
	})
}

// Without the flags nothing changes: the variable a user exports stays theirs
// and launch does not start owning it.
func TestNoRequestHeadersWithoutTheFlags(t *testing.T) {
	shell := map[string]string{claudeHeadersEnv: "Foo: bar"}
	plain := GatewayFlags{Router: true}
	if _, ok := resolveSession(t, "claude", plain, shell).Env[claudeHeadersEnv]; ok {
		t.Error("claude: launch took over the header variable")
	}
	if slices.ContainsFunc(resolveSession(t, "codex", plain, nil).PreArgs, func(a string) bool { return strings.Contains(a, "http_headers") }) {
		t.Error("codex: header args without the flags")
	}
	if strings.Contains(resolveSession(t, "opencode", plain, nil).Env["OPENCODE_CONFIG_CONTENT"], `"headers"`) {
		t.Error("opencode: headers without the flags")
	}
	if strings.Contains(resolveSession(t, "kilo", plain, nil).Env["KILO_CONFIG_CONTENT"], `"headers"`) {
		t.Error("kilo: headers without the flags")
	}
}

// `orq connect` writes through the exported builders, so session-only wiring
// must not be able to reach a persisted config.
func TestPersistedOpenCodeConfigCarriesNoSessionWiring(t *testing.T) {
	got, err := BuildOpenCodeConfigContent(DefaultGatewayAPIBaseURL, "openai/gpt-5.6-terra", []string{"openai/gpt-5.6-terra"}, nil, DefaultMCPURL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `"headers"`) || strings.Contains(got, "Authorization") {
		t.Errorf("persisted config carries session wiring: %s", got)
	}
}

func TestDryRunShowsTagsAndHidesOtherHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		env   string
		value string
	}{
		"claude": {claudeHeadersEnv, "Authorization: Bearer shell-secret\nx-orq-api-key: sk-secret\nX-ORQ-THREAD-ID: run-1\nX-ORQ-METADATA-suite: smoke"},
		"gemini": {geminiHeadersEnv, "Authorization: Bearer shell-secret,x-orq-api-key: sk-secret,X-ORQ-THREAD-ID: run-1,X-ORQ-METADATA-suite: smoke"},
	} {
		out := captureStdout(t, func() {
			printDryRun(&AgentDef{Binary: name}, nil, &LaunchPlan{Env: map[string]string{tc.env: tc.value}}, "sk-secret")
		})
		for _, secret := range []string{"shell-secret", "sk-secret"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s: dry run printed %s:\n%s", name, secret, out)
			}
		}
		want := tc.env + "=<redacted>, <redacted>, X-ORQ-THREAD-ID: run-1, X-ORQ-METADATA-suite: smoke\n"
		if !strings.Contains(out, want) {
			t.Errorf("%s: want %q in:\n%s", name, want, out)
		}
	}
}

// The text before a colon is not always a header name.
func TestDryRunHidesMalformedHeaderEntries(t *testing.T) {
	for _, list := range []string{
		"Authorization=Bearer shell-secret",
		"X-Token: abc,shell-secret:ghi",
		"X-ORQ-METADATA-a: b,Authorization=Bearer shell-secret",
		"X-ORQ-THREAD-ID: b,Authorization=Bearer shell-secret",
	} {
		if got := redactHeaderList(list, ','); strings.Contains(got, "shell-secret") {
			t.Errorf("%q printed as %q", list, got)
		}
	}
}

func TestMCPEnvAuthRefusesAnotherOrigin(t *testing.T) {
	check := func(override string) error {
		return checkMCPEnvAuthOrigin(DefaultGatewayAPIBaseURL, env(map[string]string{"ORQ_MCP_URL": override}))
	}

	for _, allowed := range []string{
		"",
		DefaultGatewayAPIBaseURL + "/v2/mcp",
		auth.LegacyDefaultAPIBaseURL + "/v2/mcp",
	} {
		if err := check(allowed); err != nil {
			t.Errorf("%q: %v", allowed, err)
		}
	}
	// Local development serves the API and MCP on one origin, so it needs no exemption.
	if err := checkMCPEnvAuthOrigin("http://localhost:9000", env(map[string]string{"ORQ_MCP_URL": "http://localhost:9000/v2/mcp"})); err != nil {
		t.Errorf("local development: %v", err)
	}
	// A local MCP server on another port than the local API base is refused too.
	if err := checkMCPEnvAuthOrigin("http://localhost:4200", env(map[string]string{"ORQ_MCP_URL": "http://localhost:3000/v2/mcp"})); err == nil || !strings.Contains(err.Error(), "which is not on") {
		t.Errorf("local port mismatch: got %v, want an origin refusal", err)
	}
	for _, refused := range []string{
		"http://localhost:9000/v2/mcp",
		"http://127.0.0.1:9000/v2/mcp",
		"http://[::1]:9000/v2/mcp",
		"https://mcp.example.com/v2/mcp",
		"https://api.orq.ai@mcp.example.com/v2/mcp",
		"https://localhost.example.com/v2/mcp",
		"http://127.0.0.1.evil.com/v2/mcp",
	} {
		if err := check(refused); err == nil || !strings.Contains(err.Error(), "which is not on") {
			t.Errorf("%q: got %v, want an origin refusal", refused, err)
		}
	}
	for _, schemeless := range []string{"localhost:9000/v2/mcp", "my.orq.ai/v2/mcp", "not a url"} {
		if err := check(schemeless); err == nil || !strings.Contains(err.Error(), "http:// or https://") {
			t.Errorf("%q: got %v, want it to ask for a scheme", schemeless, err)
		}
	}
	for _, unparsable := range []string{"https://mcp.example.com:abc/v2/mcp", "https://user:s3cret@mcp.example.com:abc/v2/mcp"} {
		err := check(unparsable)
		if err == nil || !strings.Contains(err.Error(), "not a valid URL") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: got %v, want a parse refusal without the password", unparsable, err)
		}
	}
	for _, secret := range []string{"https://user:s3cret@mcp.example.com/v2/mcp", "user:s3cret@mcp.example.com/v2/mcp"} {
		if err := check(secret); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: got %v, want a refusal without the password", secret, err)
		}
	}
}

// --mcp-auth env is for runs with no browser, and the origin check needs the
// resolved credentials, so a login prompt would come before a possible refusal.
func TestLoginPromptIsOffForRunsWithNoBrowser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := resolveLaunchCredentials
	t.Cleanup(func() { resolveLaunchCredentials = prev })
	stop := errors.New("stop after credentials")
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"--no-fetch-models", "--no-skills"}, true},
		{[]string{"--dry-run", "--no-fetch-models", "--no-skills"}, false},
		{[]string{"--no-fetch-models", "--no-skills", "--mcp-auth", "env"}, false},
	} {
		var got bool
		resolveLaunchCredentials = func(_ func(string) string, allowPrompt bool) (*Credentials, error) {
			got = allowPrompt
			return nil, stop
		}
		if _, err := Run(FindAgent("gemini"), tc.argv); !errors.Is(err, stop) {
			t.Fatalf("%v: err=%v, want the stub's error", tc.argv, err)
		}
		if got != tc.want {
			t.Errorf("%v: allowPrompt=%v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestMCPEnvAuthWarnsAboutTheSetupKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	writeSessionFile(t, home, map[string]any{
		"version": 1, "activeWorkspaceKey": "acme",
		"apiBaseUrl": "https://api.orq.ai", "v1BaseUrl": "https://api.orq.ai",
		"authBaseUrl": "https://api.orq.ai", "profileBaseUrl": "https://api.orq.ai/me",
		"refreshToken":    "rt",
		"bootstrapToken":  map[string]any{"token": "bt", "expiresAt": future},
		"workspaceTokens": map[string]any{"acme": map[string]any{"token": "wt", "expiresAt": future}},
		"gatewayKey":      "sk-setup",
	})

	warned := func(key string) bool {
		creds := &Credentials{APIKey: key, APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey}
		return slices.ContainsFunc(mcpEnvAuthWarnings(creds), func(w string) bool { return strings.Contains(w, "orq setup") })
	}
	if !warned("sk-setup") {
		t.Error("the saved key must warn")
	}
	if warned("sk-exported") {
		t.Error("an exported workspace key must not warn")
	}
	useProfile(t, "ci", "sk-setup")
	if warned("sk-setup") {
		t.Error("a profile's key says nothing about what orq setup mints")
	}
}

func TestCredentialNoticeNamesAWorkspaceKeyUnderMCPEnvAuth(t *testing.T) {
	notice := func(flags GatewayFlags) string {
		t.Helper()
		return captureStderr(t, func() {
			reportCredentialNotices(FindAgent("codex"), &Credentials{Kind: CredentialSessionToken}, flags)
		})
	}

	if got := notice(GatewayFlags{}); !strings.Contains(got, "orq setup") {
		t.Errorf("default notice: %s", got)
	}
	got := notice(GatewayFlags{MCPEnvAuth: true})
	if strings.Contains(got, "orq setup") || !strings.Contains(got, "workspace API key") || !strings.Contains(got, "orq tools") {
		t.Errorf("env auth notice: %s", got)
	}
}

func TestHelpListsSessionFlagsOnlyWhereAccepted(t *testing.T) {
	for _, def := range Agents() {
		help := captureStdout(t, func() { printAgentHelp(&def) })
		if got := strings.Contains(help, "--mcp-auth"); got != def.MCPEnvAuth {
			t.Errorf("%s: --mcp-auth in help = %v, accepted = %v", def.Name, got, def.MCPEnvAuth)
		}
		if got := strings.Contains(help, "--thread-id"); got != def.RequestHeaders {
			t.Errorf("%s: --thread-id in help = %v, accepted = %v", def.Name, got, def.RequestHeaders)
		}
		if got := strings.Contains(help, "credential this launch resolved"); got != def.MCPEnvAuth {
			t.Errorf("%s: credential note in help = %v", def.Name, got)
		}
		// Only claude has a second trace that keeps its own id.
		if got := strings.Contains(help, "keeps Claude Code's own session id"); got != (def.Name == "claude") {
			t.Errorf("%s: session trace note = %v", def.Name, got)
		}
	}
}

// MCPEnvAuth and RequestHeaders promise that the resolver wires the flag. An agent
// that accepted one and ignored it would fail much later with no tools or no tags.
func TestEveryAgentThatAcceptsASessionFlagWiresIt(t *testing.T) {
	everything := func(plan *LaunchPlan) string {
		parts := slices.Clone(plan.PreArgs)
		for _, value := range plan.Env {
			parts = append(parts, value)
		}
		for _, arg := range plan.PreArgs {
			if data, err := os.ReadFile(arg); err == nil {
				parts = append(parts, string(data))
			}
		}
		for _, dir := range plan.TempDirs {
			_ = filepath.WalkDir(dir.HostPath, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					if data, err := os.ReadFile(path); err == nil {
						parts = append(parts, string(data))
					}
				}
				return nil
			})
		}
		return strings.Join(parts, "\n")
	}
	bearer := regexp.MustCompile(`Bearer |bearer_token_env_var`)
	for _, def := range Agents() {
		if def.MCPEnvAuth {
			plan := resolveSession(t, def.Name, GatewayFlags{MCP: true, MCPEnvAuth: true}, nil)
			if got := everything(plan); !bearer.MatchString(got) {
				t.Errorf("%s: --mcp-auth env is accepted and not wired", def.Name)
			}
			// The entry only names the variable, so the plan has to export it.
			if plan.Env[mcpKeyEnv] != "sk-test" {
				t.Errorf("%s: %s is not exported", def.Name, mcpKeyEnv)
			}
			if got := everything(resolveSession(t, def.Name, GatewayFlags{MCP: true}, nil)); bearer.MatchString(got) {
				t.Errorf("%s: a bearer reference without --mcp-auth env", def.Name)
			}
		}
		if def.RequestHeaders {
			flags := GatewayFlags{Router: true, ThreadID: "wired-thread", Metadata: []RequestHeader{{Name: metadataHeaderPrefix + "suite", Value: "wired-tag"}}}
			got := everything(resolveSession(t, def.Name, flags, nil))
			if !strings.Contains(got, "wired-thread") || !strings.Contains(got, "wired-tag") {
				t.Errorf("%s: --thread-id or --metadata is accepted and not wired", def.Name)
			}
		}
	}
}

func TestSessionFlagsWorkWithSubscription(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		if _, _, err := ParseArgv([]string{"--subscription", "--thread-id", "x"}, FindAgent(agent).ParseOptions()); err != nil {
			t.Errorf("%s: %v", agent, err)
		}
	}
	flags := GatewayFlags{Router: true, Subscription: true, ThreadID: "run-1"}
	if !slices.ContainsFunc(resolveSession(t, "codex", flags, nil).PreArgs, func(a string) bool {
		return strings.Contains(a, "http_headers."+threadIDHeader) && strings.Contains(a, "run-1")
	}) {
		t.Error("codex: thread header is missing under --subscription")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	w.Close()
	os.Stderr = prev
	return <-done
}

func TestRunRefusesMCPEnvAuthToAnotherOrigin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORQ_API_KEY", "test-key")
	t.Setenv("ORQ_LAUNCH_NON_INTERACTIVE", "1")
	t.Setenv("ORQ_MCP_URL", "https://elsewhere.example/mcp")
	code, err := Run(FindAgent("gemini"), []string{"--dry-run", "--no-fetch-models", "--no-skills", "--mcp-auth", "env"})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "elsewhere.example") {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if _, err := Run(FindAgent("gemini"), []string{"--dry-run", "--no-fetch-models", "--no-skills"}); err != nil {
		t.Fatalf("without --mcp-auth env the override stays allowed: %v", err)
	}
}
