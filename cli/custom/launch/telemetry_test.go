package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

func TestTraceOffByDefault(t *testing.T) {
	plan, _ := resolveClaude(traceCtx(GatewayFlags{}, nil))
	for k := range plan.Env {
		if strings.HasPrefix(k, "OTEL_") || k == "CLAUDE_CODE_ENABLE_TELEMETRY" {
			t.Errorf("telemetry env %s set without --trace", k)
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

// The MCP server is on by default, so a plain `orq launch claude --trace` runs
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
	script := `const c = await import(process.argv[1] + "/src/config.js");
const o = await import(process.argv[1] + "/src/otlp.js");
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
