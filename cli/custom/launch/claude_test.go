package launch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func claudeCtx(envMap map[string]string, flags GatewayFlags) *AgentContext {
	return &AgentContext{
		Creds:  &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv: env(envMap),
		Flags:  flags,
	}
}

func routerFlags(f GatewayFlags) GatewayFlags {
	f.Router = true
	return f
}

// The default launch leaves claude on the user's own login: nothing may point
// it at the gateway, blank its API key, or pick a model for it. A Max
// subscriber used to be moved onto workspace billing by this command.
func TestClaudeDefaultDoesNotRoute(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	for k := range plan.Env {
		if strings.HasPrefix(k, "ANTHROPIC_") {
			t.Errorf("default launch exports %s=%q", k, plan.Env[k])
		}
	}
	if plan.Env["ORQ_API_KEY"] != "orq-key" {
		t.Errorf("ORQ_API_KEY: %q", plan.Env["ORQ_API_KEY"])
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

func TestClaudeDefaultForwardsAnExplicitModel(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, GatewayFlags{Model: "opus"}))
	if plan.Env["ANTHROPIC_MODEL"] != "opus" {
		t.Fatalf("ANTHROPIC_MODEL: %q", plan.Env["ANTHROPIC_MODEL"])
	}
	// The user's own environment reaches claude untouched, so it is not copied.
	plan, _ = resolveClaude(claudeCtx(map[string]string{"ANTHROPIC_MODEL": "sonnet"}, GatewayFlags{}))
	if _, set := plan.Env["ANTHROPIC_MODEL"]; set {
		t.Fatalf("env model must not be re-exported: %v", plan.Env)
	}
}

func TestClaudeRouter(t *testing.T) {
	ctx := claudeCtx(nil, routerFlags(GatewayFlags{}))
	ctx.Creds.Workspace = "acme"
	plan, err := resolveClaude(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL":   DefaultClaudeGatewayURL,
		"ANTHROPIC_AUTH_TOKEN": "orq-key",
		"ANTHROPIC_API_KEY":    "",
		"ORQ_API_KEY":          "orq-key",
	}
	for k, v := range want {
		got, present := plan.Env[k]
		if !present || got != v {
			t.Fatalf("%s: got %q (present=%v), want %q", k, got, present, v)
		}
	}
	// Not asked for, so not forced.
	for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		if _, set := plan.Env[k]; set {
			t.Errorf("%s exported without being asked for", k)
		}
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "acme") {
		t.Fatalf("router must name the billed workspace: %v", plan.Warnings)
	}
}

func TestClaudeBaseURLPriority(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(
		map[string]string{"ORQ_ANTHROPIC_BASE_URL": "https://env.example/v3/anthropic"},
		routerFlags(GatewayFlags{BaseURL: "https://flag.example/v3/anthropic"}),
	))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://flag.example/v3/anthropic" {
		t.Fatalf("flag should win: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}

	plan, _ = resolveClaude(claudeCtx(
		map[string]string{"ORQ_ANTHROPIC_BASE_URL": "https://env.example/v3/anthropic"}, routerFlags(GatewayFlags{})))
	if plan.Env["ANTHROPIC_BASE_URL"] != "https://env.example/v3/anthropic" {
		t.Fatalf("env should win over default: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}

	// The OpenAI-shaped shared router var must NOT reach claude — it speaks
	// the Anthropic-native API (review finding: silent misroute).
	plan, _ = resolveClaude(claudeCtx(
		map[string]string{"ORQ_GATEWAY_URL": "https://api.orq.ai/v3/router"}, routerFlags(GatewayFlags{})))
	if plan.Env["ANTHROPIC_BASE_URL"] != DefaultClaudeGatewayURL {
		t.Fatalf("ORQ_GATEWAY_URL must be ignored by claude: %v", plan.Env["ANTHROPIC_BASE_URL"])
	}
}

func TestClaudeRouterModelFlag(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, routerFlags(GatewayFlags{Model: "anthropic/flag-model"})))
	if plan.Env["ANTHROPIC_MODEL"] != "anthropic/flag-model" {
		t.Fatalf("model: %v", plan.Env["ANTHROPIC_MODEL"])
	}
}

func TestClaudeRouterWarnsBareModel(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, routerFlags(GatewayFlags{Model: "claude-sonnet-4-6"})))
	var found bool
	for _, w := range plan.Warnings {
		found = found || strings.Contains(w, "provider/")
	}
	if !found {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	// Without --router the bare id is exactly what Anthropic expects.
	plan, _ = resolveClaude(claudeCtx(nil, GatewayFlags{Model: "claude-sonnet-4-6"}))
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// Gateway-prefixed tier refs sent to Anthropic directly would be rejected, so
// they belong to --router only.
func TestClaudeTiersOnlyWithRouter(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, GatewayFlags{}))
	if _, set := plan.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; set {
		t.Fatal("tier aliases exported without --router")
	}
}

// Claude Code resolves /model opus|sonnet|haiku through three tier variables.
// Unset, it sends the bare alias, which the gateway rejects for having no
// provider/ prefix, so a launched session cannot switch tiers at all.
func TestClaudeWiresEveryModelTier(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, routerFlags(GatewayFlags{})))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	for _, tc := range []struct{ env, want string }{
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", DefaultClaudeOpusModel},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", DefaultClaudeSonnetModel},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", DefaultClaudeHaikuModel},
	} {
		got := plan.Env[tc.env]
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.env, got, tc.want)
		}
		if !strings.Contains(got, "/") {
			t.Errorf("%s = %q has no provider/ prefix; the gateway rejects bare ids", tc.env, got)
		}
	}
}

// The tiers are overridable, like ANTHROPIC_MODEL already is: a user who has
// pinned one in their shell keeps it.
func TestClaudeTiersHonourTheEnvironment(t *testing.T) {
	ctx := claudeCtx(map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "anthropic/claude-opus-4-8"}, routerFlags(GatewayFlags{}))
	plan, err := resolveClaude(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if got := plan.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "anthropic/claude-opus-4-8" {
		t.Errorf("env override ignored: got %q", got)
	}
}

// --base-url only has a target when claude is pointed at the gateway. Applying
// it silently to a direct session would be worse, so it is reported.
func TestClaudeWarnsBaseURLWithoutRouter(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(nil, GatewayFlags{BaseURL: "https://flag.example/v3/anthropic"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := plan.Env["ANTHROPIC_BASE_URL"]; set {
		t.Fatal("--base-url must not route a session on the user's own login")
	}
	if !warningsContain(plan, "--base-url") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// A gateway ref reaching Anthropic directly is rejected by Anthropic, and the
// error it returns says nothing about --router.
func TestClaudeWarnsGatewayRefWithoutRouter(t *testing.T) {
	plan, _ := resolveClaude(claudeCtx(nil, GatewayFlags{Model: "anthropic/claude-opus-5"}))
	if !warningsContain(plan, "--router") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	// Left over in the shell from a routed setup, it reaches claude the same way.
	plan, _ = resolveClaude(claudeCtx(map[string]string{"ANTHROPIC_MODEL": "anthropic/claude-opus-5"}, GatewayFlags{}))
	if !warningsContain(plan, "--router") {
		t.Fatalf("inherited ref, warnings: %v", plan.Warnings)
	}
}

// Without --router the launcher sets no Anthropic variables, so an inherited
// ANTHROPIC_BASE_URL silently keeps billing someone other than the user's own
// login, which is the surprise the flag split exists to remove.
func TestClaudeWarnsAboutInheritedAnthropicRouting(t *testing.T) {
	ctx := claudeCtx(map[string]string{"ANTHROPIC_BASE_URL": "https://someone-elses.example/v3/anthropic"}, GatewayFlags{DryRun: true})
	plan, err := resolveClaude(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !warningsContain(plan, "ANTHROPIC_BASE_URL") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	// An API key bills the API account, not the subscription.
	plan, _ = resolveClaude(claudeCtx(map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}, GatewayFlags{DryRun: true}))
	if !warningsContain(plan, "ANTHROPIC_API_KEY") {
		t.Fatalf("api key, warnings: %v", plan.Warnings)
	}
}

// --router points claude at the gateway through ANTHROPIC_BASE_URL, which
// claude does not read once a provider switch selects Bedrock, Vertex or any
// of their siblings. The switch is the user's own and stays in force, so
// --router has to say it routes nothing rather than claim workspace billing.
func TestRouterWarnsAboutProviderSwitches(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(map[string]string{
		"CLAUDE_CODE_USE_BEDROCK": "1",
		"CLAUDE_CODE_USE_VERTEX":  "true",
	}, routerFlags(GatewayFlags{})))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range claudeProviderSwitches {
		if _, ok := plan.Env[k]; ok {
			t.Errorf("%s overridden to %q; the shell's choice must stand", k, plan.Env[k])
		}
	}
	var said bool
	for _, w := range plan.Warnings {
		if strings.Contains(w, "CLAUDE_CODE_USE_BEDROCK") && strings.Contains(w, "CLAUDE_CODE_USE_VERTEX") && strings.Contains(w, "routes nothing") {
			said = true
		}
	}
	if !said {
		t.Errorf("no warning naming the switches that defeat --router: %v", plan.Warnings)
	}
}

// Without --router claude stays on the user's own login, and a provider switch
// there is their own arrangement: clearing it would move the session off the
// provider they chose.
func TestDefaultLaunchLeavesProviderSwitchesAlone(t *testing.T) {
	plan, err := resolveClaude(claudeCtx(map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plan.Env["CLAUDE_CODE_USE_BEDROCK"]; ok {
		t.Errorf("a default launch touched the provider switch: %v", plan.Env)
	}
}

// The gateway rejects a bare Anthropic model id, and a model can reach claude
// from a settings file as easily as from a flag. Before this, a user whose
// ~/.claude/settings.json pinned one got no warning and an opaque gateway
// refusal instead.
func TestRouterWarnsAboutAModelPinnedInSettings(t *testing.T) {
	configDir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(configDir, "settings.json"), `{"model":"claude-sonnet-4-5"}`)

	plan, err := resolveClaude(claudeCtx(map[string]string{"CLAUDE_CONFIG_DIR": configDir}, routerFlags(GatewayFlags{})))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w string) bool {
		return strings.Contains(w, "claude-sonnet-4-5") && strings.Contains(w, "provider/")
	}) {
		t.Errorf("no prefix warning for the pinned model: %v", plan.Warnings)
	}
	// Warned about, never re-exported: claude reads its own settings.
	if _, ok := plan.Env["ANTHROPIC_MODEL"]; ok {
		t.Errorf("the pinned model was re-exported: %v", plan.Env)
	}

	// A project file outranks the user one, the way claude resolves them.
	t.Chdir(t.TempDir())
	write(filepath.Join(".claude", "settings.json"), `{"model":"anthropic/claude-sonnet-5"}`)
	plan, err = resolveClaude(claudeCtx(map[string]string{"CLAUDE_CONFIG_DIR": configDir}, routerFlags(GatewayFlags{})))
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, "provider/") }) {
		t.Errorf("warned although the nearer settings file names a gateway ref: %v", plan.Warnings)
	}
}

// The tier aliases are the common thing to pin, and the launch resolves them
// itself through the three ANTHROPIC_DEFAULT_*_MODEL vars it sets, so they are
// not models missing a prefix. A real run warned about "opus" before this.
func TestRouterDoesNotWarnAboutATierAlias(t *testing.T) {
	for _, alias := range []string{"opus", "sonnet", "haiku", "opusplan", "sonnet[1m]", "opus[1m]", "opusplan[1m]"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"`+alias+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := resolveClaude(claudeCtx(map[string]string{"CLAUDE_CONFIG_DIR": dir}, routerFlags(GatewayFlags{})))
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, "provider/") }) {
			t.Errorf("%s: warned about a tier alias: %v", alias, plan.Warnings)
		}
	}
}

// `fable` and `best` are not tier aliases this launch resolves: it sets no
// fable tier variable, so the model does reach the gateway as itself and the
// warning is the right answer.
func TestRouterStillWarnsAboutAnUnresolvedAlias(t *testing.T) {
	for _, alias := range []string{"fable", "best"} {
		plan, err := resolveClaude(claudeCtx(map[string]string{"ANTHROPIC_MODEL": alias}, routerFlags(GatewayFlags{})))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, "provider/") }) {
			t.Errorf("%s: no warning about a model the launch does not resolve: %v", alias, plan.Warnings)
		}
	}
}

// A settings file that does not parse, or that pins a tier object rather than
// an id, must not invent a model to warn about.
func TestSettingsModelIgnoresWhatItCannotRead(t *testing.T) {
	for _, body := range []string{`{ this is not json`, `{"model":{"opus":"x"}}`, `{}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := claudeSettingsModel(env(map[string]string{"CLAUDE_CONFIG_DIR": dir})); got != "" {
			t.Errorf("%s: model = %q, want none", body, got)
		}
	}
}
