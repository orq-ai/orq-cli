package launch

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// DefaultClaudeGatewayURL is the anthropic-native gateway path — not the
	// router URL. Claude Code speaks the Anthropic API directly.
	DefaultClaudeGatewayURL = DefaultGatewayAPIBaseURL + "/v3/anthropic"
)

func claudeAgent() AgentDef {
	return AgentDef{
		Name:             "claude",
		Binary:           "claude",
		Label:            "Claude Code",
		InstallHint:      "npm install -g @anthropic-ai/claude-code",
		AllowModels:      false,
		Traceable:        true,
		SubscriptionAuth: true,
		MCPEnvAuth:       true,
		RequestHeaders:   true,
		HelpRoute:        helpRoute("Anthropic", DefaultClaudeGatewayURL),
		HelpModel:        "Model to start with (with gateway routing, a gateway ref: provider/model_id)",
		Prompt:           nil, // claude's own -p passes through untouched
		Resolve:          resolveClaude,
	}
}

// resolveClaude routes claude through the gateway and adds the orq MCP server,
// skills and session tracing. --no-gateway, --no-mcp, --no-skills and --no-otel
// disable those capabilities. MCP is a --mcp-config PreArg pointing at a temp file; skills
// are linked into ~/.claude/skills for the session rather than fetched as a
// plugin, unless ORQ_SKILLS_URL pins a bundle, which is loaded with
// --plugin-url instead.
func resolveClaude(ctx *AgentContext) (*LaunchPlan, error) {
	plan := &LaunchPlan{
		Env: map[string]string{
			// A nested `orq` invocation from inside the session reads these, so the
			// launch-follows-session invariant holds even one process down. The
			// orq-trace plugin resolves its key from ORQ_API_KEY too.
			"ORQ_API_KEY": ctx.Creds.APIKey,
			"ORQ_SERVER":  ctx.Creds.APIBaseURL,
		},
	}

	fail := func(err error) (*LaunchPlan, error) {
		if plan.Cleanup != nil {
			plan.Cleanup()
		}
		return nil, err
	}

	if ctx.Flags.Subscription {
		if !ctx.Flags.Router {
			return nil, fmt.Errorf("--subscription needs gateway routing; drop --no-gateway")
		}
		if m := ctx.Flags.Model; m != "" && !strings.HasPrefix(m, "anthropic/") {
			return nil, fmt.Errorf("--subscription bills calls to your Claude subscription, which serves only anthropic/ models; got %q", m)
		}
	}
	routed := false
	if ctx.Flags.Router {
		if len(threadHeaders(ctx.Flags)) > 0 {
			if switches := claudeProviderSwitchesSet(ctx.Getenv); len(switches) > 0 {
				return nil, fmt.Errorf("%s set in your shell sends claude to that provider, so --thread-id and --metadata would never reach the gateway; unset it or drop those flags", strings.Join(switches, ", "))
			}
		}
		routed = routeThroughGateway(ctx, plan)
	} else {
		if ctx.Flags.Model != "" {
			plan.Env["ANTHROPIC_MODEL"] = ctx.Flags.Model
		}
		// An inherited ANTHROPIC_MODEL left over from a routed setup is the
		// same mistake as passing one, so both get the warning.
		if model := firstNonEmpty(ctx.Flags.Model, ctx.Getenv("ANTHROPIC_MODEL")); model != "" && !ShouldWarnMissingProviderPrefix(model, noopNormalize) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"model %q is a gateway ref, but with --no-gateway claude talks to Anthropic directly, which expects e.g. claude-sonnet-5-5", model))
		}
		if ctx.Flags.BaseURL != "" {
			plan.Warnings = append(plan.Warnings, "--base-url only applies with gateway routing; ignoring it")
		}
		// Without gateway routing the launcher sets none of these, so whatever the
		// shell exports reaches claude. Saying "your own login" while an
		// inherited ANTHROPIC_BASE_URL bills someone else is the surprise this
		// ticket is about, so name it.
		var inherited []string
		for _, k := range loginOverrides {
			if claudeEnvOn(k, ctx.Getenv(k)) {
				inherited = append(inherited, k)
			}
		}
		if len(inherited) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%s set in your shell, so claude uses it rather than your own login; unset it to use your own login", strings.Join(inherited, ", ")))
		}
	}
	if ctx.Flags.Trace {
		if err := wireTrace(ctx, plan); err != nil {
			return fail(fmt.Errorf("session tracing: %w", err))
		}
		if routed {
			plan.Warnings = append(plan.Warnings,
				"gateway routing records each model call twice, once in the AI Router and once in the session trace, so summed costs across both double-count; pass --no-otel to keep one copy")
			if len(threadHeaders(ctx.Flags)) > 0 {
				plan.Warnings = append(plan.Warnings,
					"--thread-id and --metadata tag the gateway calls only; the session trace keeps Claude Code's own session id, so a lookup by your id or tag does not find it")
			}
		}
	} else {
		declineTrace(ctx, plan)
	}

	if url := sessionMCPURL(ctx, "claude"); url != "" {
		path, cleanup, err := writeClaudeMCPConfig(url, ctx.Flags.MCPEnvAuth)
		if err != nil {
			return fail(err)
		}
		plan.PreArgs = append(plan.PreArgs, "--mcp-config", path)
		plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: filepath.Dir(path)})
		plan.AddCleanup(cleanup)
	}
	if url := skillsPluginURL(ctx); url != "" {
		// Explicit ORQ_SKILLS_URL only: session-only plugin load, where claude
		// fetches the zip itself and nothing is installed into the user's
		// ~/.claude config.
		plan.PreArgs = append(plan.PreArgs, "--plugin-url", url)
	}
	maybeInstallSessionSkills(ctx, plan, "claude")
	return plan, nil
}

// loginOverrides are the variables that take claude off the user's own login.
var loginOverrides = append([]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}, claudeProviderSwitches...)

func noopNormalize(model string) string { return model }

// claudeProviderSwitches are the env switches that move claude off the
// Anthropic API and onto a provider with its own endpoint variable. Read from
// the claude 2.1.281 bundle, which keeps them in one list next to
// ANTHROPIC_BEDROCK_BASE_URL and its siblings.
var claudeProviderSwitches = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_GATEWAY",
}

// claudeEnvOn reports whether claude acts on variable k set to v. The 2.1.281
// bundle reads its CLAUDE_CODE_USE_* switches through a check that takes 1, true,
// yes and on, in any case, as on, except CLAUDE_CODE_USE_GATEWAY, which any
// non-empty value turns on, 0 too. Every other variable counts once it is set.
func claudeEnvOn(k, v string) bool {
	if !strings.HasPrefix(k, "CLAUDE_CODE_USE_") || k == "CLAUDE_CODE_USE_GATEWAY" {
		return v != ""
	}
	return slices.Contains([]string{"1", "true", "yes", "on"}, strings.ToLower(strings.TrimSpace(v)))
}

// claudeProviderSwitchesSet returns the switches claude would act on.
func claudeProviderSwitchesSet(getenv func(string) string) []string {
	var set []string
	for _, k := range claudeProviderSwitches {
		if claudeEnvOn(k, getenv(k)) {
			set = append(set, k)
		}
	}
	return set
}

// routeThroughGateway points claude at the anthropic-native gateway path. Only
// the model the user asked for is exported: an unset ANTHROPIC_MODEL leaves
// claude's own default and any /model choice alone, and the tier variables
// below turn its default aliases into concrete model ids. It returns false when a
// provider switch in the shell overrides the route.
func routeThroughGateway(ctx *AgentContext, plan *LaunchPlan) bool {
	getenv := ctx.Getenv

	// Deliberately NOT ORQ_GATEWAY_URL: that is the OpenAI-shaped router URL
	// shared by the other agents, and claude speaks the Anthropic-native API —
	// inheriting it would misroute every request. Claude gets its own key.
	baseURL := firstNonEmpty(
		ctx.Flags.BaseURL,
		getenv("ORQ_ANTHROPIC_BASE_URL"),
		deriveFromAPIBase(ctx.Creds.APIBaseURL, "/v3/anthropic"),
		DefaultClaudeGatewayURL,
	)
	if ctx.Flags.Model != "" {
		plan.Env["ANTHROPIC_MODEL"] = ctx.Flags.Model
	}
	plan.Env["ANTHROPIC_BASE_URL"] = baseURL
	plan.Env["ANTHROPIC_API_KEY"] = "" // explicitly empty so claude uses the auth token (or its own login)
	if ctx.Flags.Subscription {
		// Empty AUTH_TOKEN leaves claude on its own OAuth login, which it sends
		// as the bearer; the orq key rides in a header instead.
		plan.Env["ANTHROPIC_AUTH_TOKEN"] = ""
		plan.Env[claudeHeadersEnv] = orqAPIKeyHeader + ": " + ctx.Creds.APIKey
	} else {
		plan.Env["ANTHROPIC_AUTH_TOKEN"] = ctx.Creds.APIKey
	}
	// Each of these picks a provider whose own base URL claude reads instead of
	// ANTHROPIC_BASE_URL. One left in the shell is the user's own arrangement,
	// so it stays in force, but gateway routing then routes nothing and says so.
	providerSwitches := claudeProviderSwitchesSet(getenv)
	headers := threadHeaders(ctx.Flags)
	if len(providerSwitches) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s set in your shell sends claude to that provider, so gateway routing does nothing and nothing bills to orq; unset it to route through the orq.ai AI Router", strings.Join(providerSwitches, ", ")))
	} else {
		if len(headers) > 0 {
			// Under --subscription this variable is launch's own, as it is without
			// these flags, so the base is the x-orq-api-key entry set above and the
			// shell's value stays ignored. Otherwise the shell's entries are kept.
			base := firstNonEmpty(plan.Env[claudeHeadersEnv], getenv(claudeHeadersEnv))
			plan.Env[claudeHeadersEnv] = mergeHeaderEnv(base, '\n', headers)
		}
		if ctx.Flags.Subscription {
			plan.Warnings = append(plan.Warnings,
				"--subscription sends this session's model calls through the orq.ai AI Router on your own Claude login; usage bills to your Claude subscription, and orq traces the calls")
		} else {
			billed := firstNonEmpty(ctx.Creds.Workspace, "the workspace this API key belongs to")
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"gateway routing sends this session's model calls through the orq.ai AI Router; usage bills to %s, not your Claude subscription", billed))
		}
	}
	// The /model tiers (opus, sonnet, haiku) are left to Claude Code. It resolves
	// each alias to a current model id itself and the gateway serves those ids, so
	// no ANTHROPIC_DEFAULT_*_MODEL is set here: a pin went stale with every new
	// tier and needed a CLI release to move. A value the user exported still
	// reaches the session, because the child inherits the environment.

	// Claude Code turns tool search off whenever ANTHROPIC_BASE_URL is not a
	// first-party Anthropic host, which the gateway never is, so a launched
	// session sends all built-in tools up front (about 43k first-turn tokens
	// against 29k with search). The router forwards defer_loading and
	// tool_reference blocks, so turning it back on is safe and cuts the first-turn
	// prompt by about a third, more with MCP servers. Only on the
	// gateway path: without routing Claude Code talks to Anthropic directly and
	// enables search itself. A value the user exported wins, and
	// CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS is never set here because it would
	// force search back off.
	plan.Env["ENABLE_TOOL_SEARCH"] = firstNonEmpty(getenv("ENABLE_TOOL_SEARCH"), "true")
	return len(providerSwitches) == 0
}
