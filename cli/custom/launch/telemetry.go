package launch

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

var lookPath = exec.LookPath

// otlpEndpoint is the base Claude Code's own exporter appends /v1/metrics and
// /v1/logs to. It does not steer the plugin, which never sees OTEL_* and
// derives the matching /v2/otel/v1/traces from ORQ_BASE_URL instead.
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
		// ORQ_TRACE_PROFILE outranks ORQ_API_KEY and ORQ_BASE_URL in the
		// plugin. ORQ_PROFILE ranks below both, but it still picks the profile
		// whose otlp_endpoint the plugin uses, and that one has no env
		// override. Clearing both is what pins the destination.
		"ORQ_TRACE_PROFILE": "",
		"ORQ_PROFILE":       "",
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
	if _, err := lookPath("node"); err != nil {
		plan.Warnings = append(plan.Warnings,
			"node is not on PATH; the orq-trace hooks run on node, so this session will export metrics and logs but no trace")
	}
	if ctx.Creds.Kind == CredentialSessionToken {
		plan.Warnings = append(plan.Warnings,
			"this login token expires in about an hour, and the export stops with it, leaving a trace that ends mid-session; run 'orq setup' to mint a 90-day key")
	}
	if ctx.Flags.DryRun {
		plan.Notes = append(plan.Notes,
			"a real run loads the orq-trace plugin for the session with --plugin-dir, or uses your installed copy if claude has one enabled")
		return nil
	}
	dir, err := os.MkdirTemp("", "orq-claude-trace-")
	if err != nil {
		return err
	}
	plan.AddCleanup(func() { _ = os.RemoveAll(dir) })
	plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: dir})
	// With both profile vars empty the plugin still resolves the profile named
	// in ~/.orq/config.json, and that profile's otlp_endpoint outranks
	// ORQ_BASE_URL. Point it at a file inside this session's own temp dir,
	// which never exists: the plugin ignores ENOENT, so the trace goes where
	// the launch decided and not where a stale profile points.
	plan.Env["ORQ_CONFIG_PATH"] = filepath.Join(dir, "no-orq-config.json")

	// A second copy of the hooks would write every span twice.
	installed, err := tracePluginInstalled(ctx.ExecProbe)
	if err != nil {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"could not read your installed claude plugins (%v); if orq-trace is installed and enabled there, this session writes every span twice", err))
	}
	if installed {
		plan.Warnings = append(plan.Warnings,
			"using the orq-trace plugin already installed in your claude config instead of loading the one this CLI ships")
		return nil
	}
	pluginDir := filepath.Join(dir, tracePluginName)
	src, err := fs.Sub(tracePlugin, "assets/orq-trace")
	if err != nil {
		return err
	}
	if err := os.CopyFS(pluginDir, src); err != nil {
		return err
	}
	plan.PreArgs = append(plan.PreArgs, "--plugin-dir", pluginDir)
	return nil
}

// tracePluginInstalled reports whether the user has orq-trace installed and
// enabled through a marketplace. A failure to tell counts as not installed, so
// the session still gets a trace, and is returned so the caller can say the
// check did not happen.
func tracePluginInstalled(run func(string, ...string) (string, error)) (bool, error) {
	if run == nil {
		return false, nil
	}
	out, err := run("claude", "plugin", "list", "--json")
	if err != nil {
		return false, err
	}
	var plugins []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(out), &plugins); err != nil {
		return false, err
	}
	for _, p := range plugins {
		// Installed ids read orq-trace@<marketplace>; name is the fallback for
		// a build that reports the plugin without one.
		if p.Enabled && (p.Name == tracePluginName || strings.HasPrefix(p.ID, tracePluginName+"@")) {
			return true, nil
		}
	}
	return false, nil
}
