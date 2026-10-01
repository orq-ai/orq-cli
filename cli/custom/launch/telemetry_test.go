package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type probeCall []string

func recordingProbe(calls *[]probeCall, listOutput string, failOn string) func(string, ...string) (string, error) {
	return func(binary string, args ...string) (string, error) {
		*calls = append(*calls, append(probeCall{binary}, args...))
		joined := strings.Join(args, " ")
		if failOn != "" && strings.Contains(joined, failOn) {
			return "", errors.New("boom")
		}
		if strings.HasPrefix(joined, "plugin list") {
			return listOutput, nil
		}
		return "", nil
	}
}

// resolveTraced resolves and releases the plan when the test ends, so the
// session plugin directory does not outlive it.
func resolveTraced(t *testing.T, ctx *AgentContext) *LaunchPlan {
	t.Helper()
	plan, err := resolveClaude(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		t.Cleanup(plan.Cleanup)
	}
	return plan
}

func traceCtx(flags GatewayFlags, probe func(string, ...string) (string, error)) *AgentContext {
	ctx := claudeCtx(nil, flags)
	ctx.ExecProbe = probe
	return ctx
}

// The plugin is the only trace writer. Turning the native trace exporter on as
// well double-counts every session, so this holds however the launch is
// configured.
func TestTraceNeverEnablesTheNativeTraceExporter(t *testing.T) {
	for _, flags := range []GatewayFlags{
		{Trace: true, DryRun: true},
		{Trace: true, Router: true, DryRun: true},
	} {
		plan := resolveTraced(t, traceCtx(flags, nil))
		if got := plan.Env["OTEL_TRACES_EXPORTER"]; got != "none" {
			t.Errorf("%+v: OTEL_TRACES_EXPORTER = %q, want none", flags, got)
		}
		if plan.Env["OTEL_METRICS_EXPORTER"] != "otlp" || plan.Env["OTEL_LOGS_EXPORTER"] != "otlp" {
			t.Errorf("%+v: metrics and logs must export: %v", flags, plan.Env)
		}
		// The exporters above do nothing without the switch, and nothing
		// fails loudly when they do nothing.
		if plan.Env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
			t.Errorf("%+v: telemetry is off, so nothing exports: %v", flags, plan.Env)
		}
		if plan.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/json" {
			t.Errorf("%+v: protocol = %q; ingest takes http/json", flags, plan.Env["OTEL_EXPORTER_OTLP_PROTOCOL"])
		}
		if _, set := plan.Env["CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"]; set {
			t.Errorf("%+v: the beta trace schema must stay off", flags)
		}
		for k := range plan.Env {
			if strings.HasPrefix(k, "OTEL_LOG_") {
				t.Errorf("%+v: %s ships content twice; the hooks already carry it", flags, k)
			}
		}
	}
}

// ParseArgv turns tracing on; the resolver is what --no-otel reaches, and a
// declined session must carry no telemetry env at all.
func TestNoOtelLeavesNoTelemetryEnv(t *testing.T) {
	plan, _ := resolveClaude(traceCtx(GatewayFlags{}, nil))
	for k := range plan.Env {
		if strings.HasPrefix(k, "OTEL_") || k == "CLAUDE_CODE_ENABLE_TELEMETRY" {
			t.Errorf("telemetry env %s set after --no-otel", k)
		}
	}
}

func TestTraceEndpoint(t *testing.T) {
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true, DryRun: true}, nil))
	if got := plan.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]; got != DefaultGatewayAPIBaseURL+"/v2/otel" {
		t.Errorf("hosted endpoint: %q", got)
	}
	if got := plan.Env["OTEL_EXPORTER_OTLP_HEADERS"]; got != "Authorization=Bearer orq-key" {
		t.Errorf("headers: %q", got)
	}

	// Self-hosted installs must keep their telemetry inside their own network.
	ctx := traceCtx(GatewayFlags{Trace: true, DryRun: true}, nil)
	ctx.Creds.APIBaseURL = "https://orq.acme.internal/"
	plan = resolveTraced(t, ctx)
	if got := plan.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]; got != "https://orq.acme.internal/v2/otel" {
		t.Errorf("on-prem endpoint: %q", got)
	}
}

func pluginDirArg(plan *LaunchPlan) string {
	for i, a := range plan.PreArgs {
		if a == "--plugin-dir" && i+1 < len(plan.PreArgs) {
			return plan.PreArgs[i+1]
		}
	}
	return ""
}

// The plugin is loaded for one session and never installed: the only claude
// command the launcher may run is the read-only list.
func TestTraceLoadsThePluginForTheSessionOnly(t *testing.T) {
	var calls []probeCall
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "[]", "")))
	dir := pluginDirArg(plan)
	if dir == "" {
		t.Fatalf("no --plugin-dir: %v", plan.PreArgs)
	}
	for _, f := range []string{".claude-plugin/plugin.json", "hooks/session-end.js", "src/otlp.js", "package.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("plugin file %s missing: %v", f, err)
		}
	}
	assertDeclaredIfPath(t, dir, plan.TempDirs)
	for _, c := range calls {
		if strings.Join(c, " ") != "claude plugin list --json" {
			t.Errorf("launcher changed claude config: %v", c)
		}
	}
	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("plugin dir outlived the session: %v", err)
	}
}

func TestTraceDefersToAnInstalledPlugin(t *testing.T) {
	var calls []probeCall
	listed := `[{"id": "orq-trace@orq-claude-plugin", "enabled": true}]`
	plan, _ := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, listed, "")))
	if dir := pluginDirArg(plan); dir != "" {
		t.Fatalf("second copy of the hooks loaded (%s): every span would land twice", dir)
	}
	if !warningsContain(plan, "already installed") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	if plan.Env["OTEL_TRACES_EXPORTER"] != "none" {
		t.Fatalf("env: %v", plan.Env)
	}
}

// A disabled install does not trace, so the session copy is still needed.
func TestTraceIgnoresADisabledInstall(t *testing.T) {
	var calls []probeCall
	listed := `[{"id": "orq-trace@orq-claude-plugin", "enabled": false}]`
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, listed, "")))
	if pluginDirArg(plan) == "" {
		t.Fatal("disabled install must not suppress the session plugin")
	}
}

func TestTraceListFailureStillLoadsThePlugin(t *testing.T) {
	var calls []probeCall
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "", "plugin list")))
	if pluginDirArg(plan) == "" {
		t.Fatal("no plugin loaded")
	}
}

// The MCP server is on by default too, so a plain `orq launch claude` runs
// both writers. An earlier revision assigned PreArgs in the MCP block and
// dropped the --plugin-dir the trace had just added: the default traced launch
// then started with no plugin and wrote no spans at all.
func TestTraceAndMCPBothReachTheSession(t *testing.T) {
	var calls []probeCall
	ctx := traceCtx(GatewayFlags{Trace: true, MCP: true}, recordingProbe(&calls, "[]", ""))
	plan := resolveTraced(t, ctx)
	if pluginDirArg(plan) == "" {
		t.Errorf("MCP dropped the trace plugin: %v", plan.PreArgs)
	}
	var mcpConfig string
	for i, a := range plan.PreArgs {
		if a == "--mcp-config" && i+1 < len(plan.PreArgs) {
			mcpConfig = plan.PreArgs[i+1]
		}
	}
	if mcpConfig == "" {
		t.Fatalf("no --mcp-config: %v", plan.PreArgs)
	}
	// Both writers' directories have to survive the earlier append, or the
	// sandbox mount list loses one of them.
	assertDeclaredIfPath(t, mcpConfig, plan.TempDirs)
	assertDeclaredIfPath(t, pluginDirArg(plan), plan.TempDirs)
}

// A dry run resolves and starts nothing, so it may not copy the plugin out or
// ask claude what is installed.
func TestTraceDryRunTouchesNothing(t *testing.T) {
	var calls []probeCall
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true, DryRun: true}, recordingProbe(&calls, "[]", "")))
	if dir := pluginDirArg(plan); dir != "" {
		t.Errorf("dry run copied the plugin to %s", dir)
	}
	for _, c := range calls {
		if strings.Contains(strings.Join(c, " "), "plugin list") {
			t.Errorf("dry run probed claude: %v", c)
		}
	}
	if plan.Env["ORQ_CONFIG_PATH"] == "" {
		t.Errorf("dry run hides ORQ_CONFIG_PATH, which a real run sets: %v", plan.Env)
	}
	var noted bool
	for _, n := range plan.Notes {
		noted = noted || strings.Contains(n, "orq-trace")
	}
	if !noted {
		t.Fatalf("dry run must say what a real run loads: %v", plan.Notes)
	}
}

// The hooks are a node script. Without node the session still exports metrics
// and logs, so nothing fails loudly and the missing trace looks like an
// ingestion bug.
func TestTraceWarnsWithoutNode(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPath = original })
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true, DryRun: true}, nil))
	if !warningsContain(plan, "node") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// Both writers price the same call, with different trace ids, so a dashboard
// summing across them doubles the session's cost.
func TestRouterWithTraceWarnsAboutDoubleCounting(t *testing.T) {
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true, Router: true, DryRun: true}, nil))
	if !warningsContain(plan, "twice") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// The plugin, not this package, decides where spans go, so the pin is checked
// against the vendored plugin itself: a stale profile in the user's shell and
// ~/.orq/config.json must lose to the session's own profile, on a host where
// the plugin's own base-URL derivation would pick the wrong one.
func TestTracePinsThePluginDestination(t *testing.T) {
	home := t.TempDir()
	stale := `{"current":"staging","profiles":{"staging":{"api_key":"stale-key","otlp_endpoint":"https://stale.example/v2/otel"}}}`
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".orq", "config.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := traceCtx(GatewayFlags{Trace: true}, recordingProbe(new([]probeCall), "[]", ""))
	ctx.Creds.APIBaseURL = "https://my.staging.orq.ai"
	plan := resolveTraced(t, ctx)
	want := plan.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] + "/v1/traces"

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	// A bare path is not a valid import specifier on Windows, where the plugin
	// directory reads C:\..., so the specifier is built as a file URL.
	script := `const { pathToFileURL } = await import("node:url");
const root = pathToFileURL(process.argv[1]).href;
const c = await import(root + "/src/config.js");
const o = await import(root + "/src/otlp.js");
console.log(o.getEndpoint() + " " + c.getApiKey());`
	cmd := exec.Command(node, "--input-type=module", "-e", script, pluginDirArg(plan))
	env := map[string]string{"HOME": home, "ORQ_PROFILE": "staging", "ORQ_TRACE_PROFILE": "staging"}
	for k, v := range plan.Env {
		if !strings.HasPrefix(k, "OTEL_") { // claude strips these from hook env
			env[k] = v
		}
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("plugin config: %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != want+" orq-key" {
		t.Fatalf("plugin resolves %q, want %q", got, want+" orq-key")
	}
	if stderr.Len() != 0 {
		t.Fatalf("plugin warned on every hook: %s", stderr.String())
	}
}

// A probe that cannot answer leaves the launcher loading its own copy beside
// an installed one, and both sets of hooks write every span.
func TestTraceWarnsWhenThePluginProbeFails(t *testing.T) {
	probe := func(string, ...string) (string, error) { return "", errors.New("claude: not found") }
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true}, probe))
	if !warningsContain(plan, "twice") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

// Output that does not parse is not an installed plugin. Reading it as one
// skips the --plugin-dir the session's only span writer arrives on, so the
// session records nothing and says nothing.
func TestTracePluginProbeTreatsBadJSONAsNotInstalled(t *testing.T) {
	for _, out := range []string{"", "not json", `{"plugins":[{"id":"orq-trace@orq","enabled":true}]}`} {
		probe := func(string, ...string) (string, error) { return out, nil }
		plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: true}, probe))
		if pluginDirArg(plan) == "" {
			t.Errorf("probe output %q: no --plugin-dir, so the session writes no spans: %v", out, plan.PreArgs)
		}
	}
}

// --no-otel has to reach a plugin the user installed to trace every session.
// `orq connect otel` leaves orq-trace enabled in the user's claude config, and
// its hooks start as soon as they find a key, which the launch supplies, so
// without the plugin's own switch the flag would decline nothing.
func TestNoOtelSwitchesOffAnInstalledPlugin(t *testing.T) {
	installed := `[{"id":"orq-trace@orq-claude-plugin","name":"orq-trace","version":"0.5.0","enabled":true}]`
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: false}, recordingProbe(new([]probeCall), installed, "")))

	if got := plan.Env["ORQ_TRACE_DISABLED"]; got != "1" {
		t.Errorf("ORQ_TRACE_DISABLED = %q, want 1", got)
	}
	// Nothing may turn telemetry on either, or the native exporter traces
	// what the plugin was just told not to.
	if plan.Env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "" {
		t.Errorf("--no-otel still enabled telemetry: %v", plan.Env)
	}
	if pluginDirArg(plan) != "" {
		t.Errorf("--no-otel still loaded a plugin: %v", plan.PreArgs)
	}
	// The user who installed it expects every session captured, so say which
	// session is not.
	if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "--no-otel") }) {
		t.Errorf("no note about the installed plugin being off: %v", plan.Notes)
	}
}

// An installed copy older than the switch keeps tracing the session, so the
// promise has to become a warning. Telling the user their session was switched
// off when it was not is the one outcome worse than saying nothing.
func TestNoOtelWarnsWhenTheInstalledPluginPredatesTheSwitch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{"an older release", `"version":"0.4.1",`, "older than the 0.5.0"},
		{"a version it does not report", ``, "reports no version"},
		{"a version it cannot parse", `"version":"nightly",`, "older than the 0.5.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listing := `[{"id":"orq-trace@orq-claude-plugin","name":"orq-trace",` + tc.version + `"enabled":true}]`
			plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: false}, recordingProbe(new([]probeCall), listing, "")))

			if got := plan.Env["ORQ_TRACE_DISABLED"]; got != "1" {
				t.Errorf("ORQ_TRACE_DISABLED = %q, want 1", got)
			}
			if !slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, tc.want) }) {
				t.Errorf("no warning containing %q: %v", tc.want, plan.Warnings)
			}
			if slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "switched off") }) {
				t.Errorf("promised the session was switched off anyway: %v", plan.Notes)
			}
		})
	}
}

// A newer plugin honours the switch, so the note stands rather than the warning.
func TestNoOtelTrustsANewerPlugin(t *testing.T) {
	listing := `[{"id":"orq-trace@orq-claude-plugin","name":"orq-trace","version":"1.2.0","enabled":true}]`
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: false}, recordingProbe(new([]probeCall), listing, "")))
	if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "--no-otel") }) {
		t.Errorf("no note about the installed plugin being off: %v", plan.Notes)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("warned about a plugin that honours the switch: %v", plan.Warnings)
	}
}

// The switch is set whether or not a plugin is installed: the probe cannot run
// in a dry run, and a user can install the plugin after this launch is planned.
func TestNoOtelSetsTheSwitchWithoutProbing(t *testing.T) {
	var calls []probeCall
	plan := resolveTraced(t, traceCtx(GatewayFlags{Trace: false, DryRun: true}, recordingProbe(&calls, "[]", "")))
	if got := plan.Env["ORQ_TRACE_DISABLED"]; got != "1" {
		t.Errorf("ORQ_TRACE_DISABLED = %q, want 1", got)
	}
	if len(calls) != 0 {
		t.Errorf("a dry run ran claude: %v", calls)
	}
}
