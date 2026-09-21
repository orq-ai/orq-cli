package launch

import (
	"errors"
	"os"
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
	// The plugin cannot see OTEL_* (claude strips them from hook env), so its
	// own destination has to arrive through ORQ_BASE_URL.
	if got := plan.Env["ORQ_BASE_URL"]; got != "https://orq.acme.internal/" {
		t.Errorf("plugin base URL: %q", got)
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
	plan, err := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "[]", "")))
	if err != nil {
		t.Fatal(err)
	}
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
	plan, _ := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, listed, "")))
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if pluginDirArg(plan) == "" {
		t.Fatal("disabled install must not suppress the session plugin")
	}
}

func TestTraceListFailureStillLoadsThePlugin(t *testing.T) {
	var calls []probeCall
	plan, err := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "", "plugin list")))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if pluginDirArg(plan) == "" {
		t.Fatal("no plugin loaded")
	}
}
