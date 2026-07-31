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

// Run resolves and launches an agent locally, returning the child exit code.
// Sandbox mode is dispatched from here once docker support lands.
func Run(def *AgentDef, argv []string) (int, error) {
	flags, passthrough, err := ParseArgv(argv, ParseArgvOptions{
		Prompt:      def.Prompt,
		AllowModels: def.AllowModels,
	})
	if err != nil {
		return 2, err
	}
	if flags.Help {
		printAgentHelp(def)
		return 0, nil
	}

	if !flags.Sandbox {
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

	creds, err := ResolveCredentials(os.Getenv)
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
		return localCancel
	}
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
	fmt.Print(`  --base-url <url>      Override the gateway base URL
  --no-fetch-models     Skip fetching the enabled-model catalog
`)
	if def.Prompt != nil {
		fmt.Println("  -p, --prompt <text>   One-shot prompt (mapped to the agent's own syntax)")
	}
	fmt.Print(`  --sandbox             Run inside a throwaway Docker container
  --mount-cwd           Sandbox only: mount current directory at /workspace
  --rebuild             Sandbox only: rebuild the Docker image
  --dry-run             Print the resolved command without launching
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
