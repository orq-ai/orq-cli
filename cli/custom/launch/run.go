package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Run resolves and launches an agent, returning the child exit code.
func Run(def *AgentDef, argv []string) (int, error) {
	flags, passthrough, err := ParseArgv(argv, def.ParseOptions())
	if err != nil {
		// 1, not a distinct usage code: the stability contract defines 0 / 1 /
		// 130 / 143 for orq's own failures, and a bad flag is just a failure.
		// Codes outside that set can still surface here, because a launched
		// agent's own exit code is propagated verbatim (127 for a missing
		// binary, 128+signum when the agent is signalled).
		return 1, err
	}
	if flags.Help {
		printAgentHelp(def)
		return 0, nil
	}

	// Dry-run stays non-interactive: scripts use it and must get the plain
	// not-logged-in error, not a prompt.
	creds, err := resolveCredentialsOrLogin(os.Getenv, !flags.DryRun)
	if err != nil {
		return 1, err
	}

	// Before Resolve, so no resolver probes a binary that is not there and
	// warns about the probe ahead of the real error.
	if !flags.DryRun {
		if _, err := exec.LookPath(def.Binary); err != nil {
			return 1, fmt.Errorf("%s CLI not found on PATH. Install it: %s", def.Binary, def.InstallHint)
		}
	}

	plan, err := def.Resolve(&AgentContext{
		Creds:     creds,
		Getenv:    os.Getenv,
		Flags:     flags,
		ExecProbe: hostExecProbe,
	})
	if err != nil {
		return 1, err
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	reportCredentialNotices(def, creds, flags)
	if flags.MCPEnvAuth {
		plan.Warnings = append(plan.Warnings, mcpEnvAuthWarnings(creds, os.Getenv)...)
	}
	for _, w := range plan.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	args := append(append([]string{}, plan.PreArgs...), passthrough...)

	if flags.DryRun {
		printDryRun(def, args, plan, creds.APIKey)
		return 0, nil
	}

	return RunChild(def.Binary, args, plan.Env)
}

// probeTimeout bounds a probe so a hung agent binary delays the launch
// instead of blocking it.
const probeTimeout = 10 * time.Second

func hostExecProbe(binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).Output()
	if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) != 0 {
		// Output() captures stderr on the error; without this the probe's
		// real failure reason ends up as an opaque "exit status 1".
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return string(out), err
}

func printAgentHelp(def *AgentDef) {
	route := firstNonEmpty(def.HelpRoute, "the orq.ai AI Router")
	model := firstNonEmpty(def.HelpModel, "Gateway model (provider/model_id)")
	headline := fmt.Sprintf("Launch %s preconfigured to route through %s.", def.Label, route)
	if def.Traceable {
		headline = fmt.Sprintf("Launch %s through %s, with the orq MCP server, skills and session tracing.\nUse --no-gateway to keep your own Anthropic login for model calls.", def.Label, route)
	}

	fmt.Printf(`%s

Usage:
  orq launch %s [flags] [--] [agent args...]

Flags:
  --model <id>          %s
`, headline, def.Name, model)
	if def.AllowModels {
		fmt.Println("  --models <list>       Extra models: comma-separated or JSON array")
	}
	baseURLScope := ""
	if def.Traceable {
		baseURLScope = " (with gateway routing)"
	}
	fmt.Printf("  --base-url <url>      Override the gateway base URL%s\n", baseURLScope)
	if def.FetchesModels {
		fmt.Println("  --no-fetch-models     Skip fetching the enabled-model catalog")
	}
	if def.Traceable {
		fmt.Print(`  --otel                Capture the session as an orq trace: loads the orq-trace
                        plugin for this session only and turns on Claude Code's
                        metrics and logs export (default)
  --no-otel             Do not capture this session. To capture the sessions you
                        start yourself, outside orq launch, run 'orq connect otel'
  --gateway             Send model traffic through the orq.ai AI Router (default).
                        Usage bills to the orq workspace, not your subscription
  --no-gateway          Keep your own Anthropic login for model calls
`)
	}
	fmt.Print(`  --mcp                 Wire the orq MCP server (workspace tools) into the agent (default)
  --no-mcp              Do not make the orq MCP server available for this session
`)
	if def.MCPEnvAuth {
		fmt.Print(`  --mcp-auth <mode>     How the orq MCP server authenticates. oauth (default) is the
                        agent's own browser login. env is for runs with no browser:
                        the session's entry names ORQ_API_KEY, which holds the
                        credential this launch resolved. Tools then need a workspace
                        API key with their permissions; a key from 'orq setup' holds
                        only gateway access and model read. Tool approval stays with
                        the agent, so a headless run may also have to approve each tool
`)
	}
	if def.RequestHeaders {
		fmt.Print(`  --thread-id <id>      Group this session's model calls under one thread id
  --metadata <k=v>      Tag this session's model calls; repeatable, up to 20 keys
`)
		if def.Traceable {
			fmt.Print(`                        Both reach gateway calls only: the session trace from --otel
                        keeps Claude Code's own session id
`)
		}
		if def.Name == "gemini" {
			fmt.Print(`                        With no thread id, launch generates one and prints it
`)
		}
	}
	fmt.Print(`  --no-skills           Do not make the orq skills available for this session.
                        Every agent gets them: they are linked into the skills
                        directory the agent reads when it starts and removed
                        again when it exits, leaving nothing behind. Not gated
                        on --mcp. For a permanent install, orq connect skills
`)
	if def.SubscriptionAuth {
		fmt.Print(`  --subscription        Bill model calls to your own subscription: the agent keeps
                        its own login and orq traces the calls. Only the models
                        that subscription serves (codex: openai/, claude: anthropic/)
`)
	}
	if def.Prompt != nil {
		fmt.Println("  -p, --prompt <text>   One-shot prompt (mapped to the agent's own syntax)")
	}
	fmt.Print(`  --dry-run             Print the resolved command and env (key redacted) without
                        starting the agent. It still resolves credentials and any
                        model catalogue the agent needs.
  -h, --help            Show this help

Launcher flags go first: the first argument launch does not own, and everything
after it, goes to the agent.
Everything after -- is passed to the agent untouched.
`)
}

func printDryRun(def *AgentDef, args []string, plan *LaunchPlan, apiKey string) {
	fmt.Printf("binary: %s\n", def.Binary)
	fmt.Printf("args:   %s\n", strings.Join(args, " "))
	fmt.Println("env:")
	keys := make([]string, 0, len(plan.Env))
	for k := range plan.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := plan.Env[k]
		if delim, ok := headerListEnvs[k]; ok {
			v = redactHeaderList(v, delim)
		}
		if apiKey != "" {
			v = strings.ReplaceAll(v, apiKey, "<redacted>")
		}
		fmt.Printf("  %s=%s\n", k, v)
	}
	for _, dir := range plan.TempDirs {
		fmt.Printf("tempdir: %s\n", dir.HostPath)
	}
	for _, note := range plan.Notes {
		fmt.Printf("note:   %s\n", note)
	}
}

// headerListEnvs are the env values --dry-run prints through redactHeaderList.
var headerListEnvs = map[string]rune{claudeHeadersEnv: '\n', geminiHeadersEnv: ','}

// reportCredentialNotices prints the auth surprises worth interrupting for.
func reportCredentialNotices(def *AgentDef, creds *Credentials, flags GatewayFlags) {
	if creds.Kind == CredentialSessionToken {
		expiry := "the agent gets a login token that expires in about an hour"
		if flags.Trace {
			expiry += ", and the trace export stops with it, leaving a trace that ends mid-session"
		}
		remedy := "Run 'orq setup' to mint a 90-day key."
		if flags.MCPEnvAuth {
			// Not 'orq setup': the key it mints holds gateway access and model
			// read, which most orq tools refuse.
			expiry += ", and the orq tools stop with it"
			remedy = "Export a workspace API key with the permissions those tools need."
		}
		fmt.Fprintf(os.Stderr, "Note: no durable API key for this workspace; %s. %s\n", expiry, remedy)
	}
	if creds.ShadowsSession {
		fmt.Fprintln(os.Stderr, "Note: ORQ_API_KEY may not belong to the workspace 'orq auth login' selected; the key wins. Pass --model against that workspace's catalogue, or re-run 'orq setup' to mint a key for the one you logged into.")
	}
	// RefreshProfile can repoint ActiveWorkspaceKey, leaving both equal — the
	// note would then name the same workspace twice.
	if creds.SupersededWorkspace != "" && creds.SupersededWorkspace != creds.Workspace {
		// Differs from the active workspace by construction here, so
		// RemedyForWorkspace always names 'orq setup'.
		remedy := RemedyForWorkspace(def.Name, creds.SupersededWorkspace, creds.Workspace)
		fmt.Fprintf(os.Stderr, "Note: using workspace %s from your login. ORQ_API_KEY was minted for %s, which this run ignores; the agent's own configuration is unchanged. Run '%s' to mint a key for %s, then 'orq connect %s' to repoint the agent to it.\n", creds.Workspace, creds.SupersededWorkspace, remedy, creds.Workspace, def.Name)
	}
}
