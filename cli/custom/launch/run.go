package launch

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Run resolves and launches an agent, returning the child exit code.
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
	reportCredentialNotices(def, creds)
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
	fmt.Print(`  --mcp                 Wire the orq MCP server (workspace tools) into the agent (default)
  --no-mcp              Do not make the orq MCP server available for this session
  --no-skills           Do not make the orq skills available for this session.
                        Every agent gets them: they are linked into the skills
                        directory the agent reads when it starts and removed
                        again when it exits, leaving nothing behind. Not gated
                        on --mcp. For a permanent install, orq connect skills
`)
	if def.Prompt != nil {
		fmt.Println("  -p, --prompt <text>   One-shot prompt (mapped to the agent's own syntax)")
	}
	fmt.Print(`  --dry-run             Print the resolved command and env (key redacted) without
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
	for _, note := range plan.Notes {
		fmt.Printf("note:   %s\n", note)
	}
}

// reportCredentialNotices prints the auth surprises worth interrupting for.
func reportCredentialNotices(def *AgentDef, creds *Credentials) {
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
