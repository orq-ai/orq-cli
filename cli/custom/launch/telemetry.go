package launch

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tracePlugin is the orq-trace Claude Code plugin, vendored from
// orq-ai/assistant-plugins by scripts/vendor-skills.sh at the same ref as the
// skills.
//
//go:embed all:assets/orq-trace
var tracePlugin embed.FS

const tracePluginName = "orq-trace"

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
		// Claude Code strips OTEL_* from the env its hooks run with, so the
		// plugin never sees the endpoint above. It derives its own from this
		// instead, which keeps a self-hosted install's traces on its own host.
		"ORQ_BASE_URL": firstNonEmpty(ctx.Creds.APIBaseURL, DefaultGatewayAPIBaseURL),
	}
}

// wireTrace turns telemetry on and loads the orq-trace plugin for this session
// only, through --plugin-dir, so nothing is left in the user's claude config
// to trace sessions they did not launch with --trace. The plugin resolves its
// key from the ORQ_API_KEY already in the plan.
func wireTrace(ctx *AgentContext, plan *LaunchPlan) error {
	for k, v := range traceEnv(ctx) {
		plan.Env[k] = v
	}
	// A second copy of the hooks would write every span twice.
	if tracePluginInstalled(ctx.ExecProbe) {
		plan.Warnings = append(plan.Warnings,
			"using the orq-trace plugin already installed in your claude config instead of loading the one this CLI ships")
		return nil
	}
	dir, err := os.MkdirTemp("", "orq-claude-trace-")
	if err != nil {
		return err
	}
	plan.AddCleanup(func() { _ = os.RemoveAll(dir) })
	plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: dir})
	pluginDir := filepath.Join(dir, tracePluginName)
	src, _ := fs.Sub(tracePlugin, "assets/orq-trace")
	if err := os.CopyFS(pluginDir, src); err != nil {
		return err
	}
	plan.PreArgs = append(plan.PreArgs, "--plugin-dir", pluginDir)
	return nil
}

// tracePluginInstalled reports whether the user has orq-trace installed and
// enabled through a marketplace. Any failure to tell counts as not installed:
// the worst case is then a duplicate span, not a session with no trace at all.
func tracePluginInstalled(run func(string, ...string) (string, error)) bool {
	if run == nil {
		return false
	}
	out, err := run("claude", "plugin", "list", "--json")
	if err != nil {
		return false
	}
	var plugins []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal([]byte(out), &plugins) != nil {
		return false
	}
	for _, p := range plugins {
		if p.Enabled && strings.HasPrefix(p.ID, tracePluginName+"@") {
			return true
		}
	}
	return false
}
