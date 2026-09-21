package launch

import (
	"fmt"
	"strings"
)

const (
	tracePluginMarketplace = "orq-ai/assistant-plugins"
	tracePluginID          = "orq-trace@orq-claude-plugin"
)

// otlpEndpoint is the base Claude Code appends /v1/metrics and /v1/logs to,
// and the orq-trace plugin appends /v1/traces to.
func otlpEndpoint(apiBase string) string {
	return firstNonEmpty(deriveFromAPIBase(apiBase, "/v2/otel"), DefaultGatewayAPIBaseURL+"/v2/otel")
}

// traceEnv returns the telemetry env for one traced session. Metrics and logs
// come from Claude Code's own exporter; traces come only from the orq-trace
// plugin, so OTEL_TRACES_EXPORTER=none is load-bearing: enabling both
// double-counts every session. No OTEL_LOG_* content flags either: the plugin's
// hooks already carry content, with local redaction, and shipping prompts
// through two redaction stories is worse than shipping them once.
func traceEnv(ctx *AgentContext) map[string]string {
	return map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY": "1",
		"OTEL_METRICS_EXPORTER":        "otlp",
		"OTEL_LOGS_EXPORTER":           "otlp",
		"OTEL_TRACES_EXPORTER":         "none",
		"OTEL_EXPORTER_OTLP_PROTOCOL":  "http/json",
		"OTEL_EXPORTER_OTLP_ENDPOINT":  otlpEndpoint(ctx.Creds.APIBaseURL),
		"OTEL_EXPORTER_OTLP_HEADERS":   "Authorization=Bearer " + ctx.Creds.APIKey,
	}
}

// wireTrace turns telemetry on and makes sure the orq-trace plugin is
// installed. The plugin resolves its own key from the ORQ_API_KEY already in
// the plan. Failure to install is a warning, not an error: a session without
// traces beats a launcher that refuses to start.
func wireTrace(ctx *AgentContext, plan *LaunchPlan) {
	for k, v := range traceEnv(ctx) {
		plan.Env[k] = v
	}
	if ctx.Flags.DryRun {
		// A dry run must not touch the user's claude configuration.
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"a real run would add the %s marketplace and install or update %s", tracePluginMarketplace, tracePluginID))
		return
	}
	if err := ensureTracePlugin(ctx.ExecProbe); err != nil {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"could not install the orq-trace plugin (%v); this session exports metrics and logs but no traces. Install it with: claude plugin marketplace add %s && claude plugin install %s",
			err, tracePluginMarketplace, tracePluginID))
	}
}

// ensureTracePlugin installs the plugin, or updates it when already present.
// An update only takes effect on the next start, which is acceptable: the
// installed version is always one the marketplace published.
func ensureTracePlugin(run func(binary string, args ...string) (string, error)) error {
	if run == nil {
		return fmt.Errorf("no way to run claude")
	}
	listed, err := run("claude", "plugin", "list", "--json")
	if err != nil {
		return err
	}
	if strings.Contains(listed, `"`+tracePluginID+`"`) {
		_, err = run("claude", "plugin", "update", tracePluginID)
		return err
	}
	if _, err := run("claude", "plugin", "marketplace", "add", tracePluginMarketplace); err != nil {
		return err
	}
	_, err = run("claude", "plugin", "install", tracePluginID)
	return err
}
