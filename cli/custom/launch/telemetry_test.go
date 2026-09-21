package launch

import (
	"errors"
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
		plan, err := resolveClaude(traceCtx(flags, nil))
		if err != nil {
			t.Fatal(err)
		}
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
	plan, _ := resolveClaude(traceCtx(GatewayFlags{Trace: true, DryRun: true}, nil))
	if got := plan.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]; got != DefaultGatewayAPIBaseURL+"/v2/otel" {
		t.Errorf("hosted endpoint: %q", got)
	}
	if got := plan.Env["OTEL_EXPORTER_OTLP_HEADERS"]; got != "Authorization=Bearer orq-key" {
		t.Errorf("headers: %q", got)
	}

	// Self-hosted installs must keep their telemetry inside their own network.
	ctx := traceCtx(GatewayFlags{Trace: true, DryRun: true}, nil)
	ctx.Creds.APIBaseURL = "https://orq.acme.internal/"
	plan, _ = resolveClaude(ctx)
	if got := plan.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]; got != "https://orq.acme.internal/v2/otel" {
		t.Errorf("on-prem endpoint: %q", got)
	}
}

func TestTraceDryRunTouchesNothing(t *testing.T) {
	var calls []probeCall
	plan, _ := resolveClaude(traceCtx(GatewayFlags{Trace: true, DryRun: true}, recordingProbe(&calls, "", "")))
	if len(calls) != 0 {
		t.Fatalf("dry run ran claude: %v", calls)
	}
	if !strings.Contains(strings.Join(plan.Notes, "\n"), tracePluginID) {
		t.Fatalf("notes: %v", plan.Notes)
	}
}

func TestTraceInstallsWhenMissing(t *testing.T) {
	var calls []probeCall
	plan, err := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "[]", "")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude plugin list --json",
		"claude plugin marketplace add " + tracePluginMarketplace,
		"claude plugin install " + tracePluginID,
	}
	if len(calls) != len(want) {
		t.Fatalf("calls: %v", calls)
	}
	for i, c := range calls {
		if strings.Join(c, " ") != want[i] {
			t.Errorf("call %d: %q, want %q", i, strings.Join(c, " "), want[i])
		}
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

func TestTraceUpdatesWhenInstalled(t *testing.T) {
	var calls []probeCall
	listed := `[{"id": "` + tracePluginID + `", "enabled": true}]`
	resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, listed, "")))
	if len(calls) != 2 || strings.Join(calls[1], " ") != "claude plugin update "+tracePluginID {
		t.Fatalf("calls: %v", calls)
	}
}

// A failed install must not stop the session: it degrades to metrics and logs
// with a warning that says how to finish the job by hand.
func TestTraceInstallFailureIsAWarning(t *testing.T) {
	var calls []probeCall
	plan, err := resolveClaude(traceCtx(GatewayFlags{Trace: true}, recordingProbe(&calls, "[]", "marketplace add")))
	if err != nil {
		t.Fatalf("install failure must not abort the launch: %v", err)
	}
	if !warningsContain(plan, "orq-trace") || !warningsContain(plan, "claude plugin install "+tracePluginID) {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	if plan.Env["OTEL_TRACES_EXPORTER"] != "none" {
		t.Fatalf("env dropped on failure: %v", plan.Env)
	}
}
