package launch

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"golang.org/x/term"
)

// Run resolves and launches an agent, returning the child exit code. Local
// mode execs directly; --sandbox dispatches to RunSandbox.
func Run(def *AgentDef, argv []string) (int, error) {
	flags, passthrough, err := ParseArgv(argv, ParseArgvOptions{
		Prompt:      def.Prompt,
		AllowModels: def.AllowModels,
	})
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

	if !flags.Sandbox && !flags.DryRun {
		switch promptLocalWarning(os.Getenv) {
		case localCancel:
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return 0, nil
		case localSandbox:
			flags.Sandbox = true
		}
	}
	if flags.Sandbox {
		return RunSandbox(def, flags, passthrough)
	}

	// Dry-run stays non-interactive: scripts use it and must get the plain
	// not-logged-in error, not a prompt.
	creds, err := resolveCredentialsOrLogin(os.Getenv, !flags.DryRun)
	if err != nil {
		return 1, err
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
	reportCredentialNotices(creds, flags)
	for _, w := range plan.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	args := append(append([]string{}, plan.PreArgs...), passthrough...)

	if flags.DryRun {
		printDryRun(def, args, plan, creds.APIKey)
		return 0, nil
	}

	if _, err := exec.LookPath(def.Binary); err != nil {
		return 1, fmt.Errorf("%s CLI not found on PATH. Install it: %s", def.Binary, def.InstallHint)
	}

	return RunChild(def.Binary, args, plan.Env)
}

func hostExecProbe(binary string, args ...string) (string, error) {
	out, err := exec.Command(binary, args...).Output()
	if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) != 0 {
		// Output() captures stderr on the error; without this the probe's
		// real failure reason ends up as an opaque "exit status 1".
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return string(out), err
}

type localChoice int

const (
	localOk localChoice = iota
	localSandbox
	localCancel
)

// promptLocalWarning warns that local mode gives the agent full host access
// and offers sandbox instead. TTY-only; skipped for non-interactive runs.
func promptLocalWarning(getenv func(string) string) localChoice {
	if getenv("ORQ_LAUNCH_NON_INTERACTIVE") == "1" {
		return localOk
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return localOk
	}

	fmt.Fprintln(os.Stderr, "⚠  Local execution: the agent will have full access to your filesystem, shell, and network.")
	fmt.Fprint(os.Stderr, "Proceed? [o]k / [s]andbox / [c]ancel: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return localCancel // read failure fails closed
	}
	return parseLocalChoice(line)
}

// parseLocalChoice maps the safety-prompt answer to an action. Bare Enter
// accepts: the prompt already interrupted the user's explicit `orq launch
// <agent>` intent, and Enter-to-continue is the CLI convention there.
func parseLocalChoice(line string) localChoice {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "ok", "y", "yes", "":
		return localOk
	case "s", "sandbox":
		return localSandbox
	default:
		return localCancel
	}
}

func printAgentHelp(def *AgentDef) {
	fmt.Printf(`Launch %s preconfigured to route through the orq.ai AI Router.

Usage:
  orq launch %s [flags] [--] [agent args...]

Flags:
  --model <id>          Gateway model (provider/model_id)
`, def.Label, def.Name)
	if def.AllowModels {
		fmt.Println("  --models <list>       Extra models: comma-separated or JSON array")
	}
	fmt.Println("  --base-url <url>      Override the gateway base URL")
	if def.FetchesModels {
		fmt.Println("  --no-fetch-models     Skip fetching the enabled-model catalog")
	}
	fmt.Print(`  --no-mcp              Do not wire the orq MCP server into the agent
  --no-skills           Do not load the orq skills plugin (claude only)
`)
	if def.Prompt != nil {
		fmt.Println("  -p, --prompt <text>   One-shot prompt (mapped to the agent's own syntax)")
	}
	fmt.Print(`  --sandbox             Run inside a throwaway Docker container
  --mount-cwd           Sandbox only: mount current directory at /workspace
  --rebuild             Sandbox only: rebuild the Docker image
  --dry-run             Print the resolved command and env (key redacted) without
                        starting the agent. It still resolves credentials and any
                        model catalogue the agent needs.
  -h, --help            Show this help

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
		if v != "" && v == apiKey {
			v = "<redacted>"
		}
		fmt.Printf("  %s=%s\n", k, v)
	}
	for _, dir := range plan.TempDirs {
		fmt.Printf("tempdir: %s\n", dir.HostPath)
	}
}

// reportCredentialNotices prints the auth surprises worth interrupting for.
// Shared by the local and sandbox paths: --sandbox used to skip both, so a
// container silently ran against the ORQ_API_KEY workspace while the user
// believed their `orq auth login` choice applied.
func reportCredentialNotices(creds *Credentials, flags GatewayFlags) {
	if creds.ShadowsSession {
		fmt.Fprintln(os.Stderr, "Note: authenticating with ORQ_API_KEY from the environment; the workspace picked by 'orq auth login' is ignored. Unset it to use the session.")
	}
	if !creds.SupportsMCP() && !flags.NoMCP {
		fmt.Fprintln(os.Stderr, "Note: orq MCP server skipped — this login session predates MCP scopes. Re-run 'orq auth login' (or export ORQ_API_KEY) to enable MCP tools.")
	}
}
