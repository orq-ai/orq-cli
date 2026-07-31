package launch

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
)

const containerLabel = "orq.launch=1"

// dockerfileTemplate builds a per-agent sandbox image. Non-root user; npm
// prefix so -g installs work unprivileged; PATH covers claude's native
// installer target so exec-form argv resolves without a login shell.
const dockerfileTemplate = `FROM node:22-bookworm
RUN useradd -m -s /bin/bash agent
USER agent
WORKDIR /home/agent
ENV PATH="/home/agent/.npm-global/bin:/home/agent/.claude/local/bin:/home/agent/.local/bin:${PATH}"
RUN npm config set prefix /home/agent/.npm-global && %s
CMD ["sleep", "infinity"]
`

// agentInstallCmd is the Dockerfile RUN payload installing the agent CLI.
// claude prefers its native installer (tracks releases better than npm).
func agentInstallCmd(def *AgentDef) string {
	if def.Name == "claude" {
		return "(curl -fsSL https://claude.ai/install.sh | bash) || npm install -g " + def.NpmPackage
	}
	return "npm install -g " + def.NpmPackage
}

func ImageTag(agent string) string { return "orq-launch-" + agent + ":v1" }

// --- pure argv builders (unit-testable without a docker daemon) ---

func BuildImageArgs(agent string, rebuild bool) []string {
	args := []string{"build", "-t", ImageTag(agent)}
	if rebuild {
		args = append(args, "--no-cache", "--pull")
	}
	return append(args, "-")
}

func RunContainerArgs(agent, name, cwd string, mountCwd bool) []string {
	args := []string{"run", "-d", "--name", name, "--label", containerLabel}
	if mountCwd {
		args = append(args, "-v", cwd+":/workspace", "-w", "/workspace")
	}
	return append(args, ImageTag(agent), "sleep", "infinity")
}

func ExecArgs(container string, tty bool, env map[string]string, argv []string) []string {
	args := []string{"exec", "-i"}
	if tty {
		args = append(args, "-t")
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	return append(append(args, container), argv...)
}

func containerName(agent string) string {
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return "orq-launch-" + agent + "-" + hex.EncodeToString(suffix)
}

// --- docker CLI wrapper ---

func dockerOutput(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("docker %s: %s", args[0], strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return string(out), nil
}

// CheckDocker verifies the docker CLI exists and the daemon responds.
// No auto-install — error with a platform hint instead.
func CheckDocker() error {
	if _, err := exec.LookPath("docker"); err == nil {
		cmd := exec.Command("docker", "info")
		cmd.Stdout, cmd.Stderr = nil, nil
		done := make(chan error, 1)
		if err := cmd.Start(); err == nil {
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					return nil
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
	}
	hint := "install docker (https://docs.docker.com/engine/install/)"
	if runtime.GOOS == "darwin" {
		hint = "install OrbStack (brew install orbstack) or Docker Desktop"
	}
	return fmt.Errorf("docker is not available (binary missing or daemon not running); --sandbox needs it. %s", hint)
}

// pruneOrphans removes exited containers from previous runs (crash leftovers).
// Best-effort; random container names mean live sessions are never touched.
func pruneOrphans() {
	out, err := dockerOutput("ps", "-aq", "--filter", "label="+containerLabel, "--filter", "status=exited")
	if err != nil {
		return
	}
	for _, id := range strings.Fields(out) {
		_ = exec.Command("docker", "rm", "-f", id).Run()
	}
}

func ensureImage(def *AgentDef, rebuild bool) error {
	if !rebuild {
		if _, err := dockerOutput("image", "inspect", ImageTag(def.Name)); err == nil {
			return nil
		}
	}
	fmt.Fprintf(os.Stderr, "Building sandbox image %s (first run only)...\n", ImageTag(def.Name))
	cmd := exec.Command("docker", BuildImageArgs(def.Name, rebuild)...)
	cmd.Stdin = strings.NewReader(fmt.Sprintf(dockerfileTemplate, agentInstallCmd(def)))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// copyTempDirs replicates each resolved TempDir into the container at its
// host path, so env vars and argv paths embedded by Resolve stay valid.
// Host temp paths (e.g. /var/folders on macOS) don't exist in the container
// and need root to create; the session itself still runs as the agent user.
func copyTempDirs(container string, dirs []TempDir) error {
	for _, dir := range dirs {
		if _, err := dockerOutput("exec", "-u", "root", container, "mkdir", "-p", dir.HostPath); err != nil {
			return err
		}
		if _, err := dockerOutput("cp", dir.HostPath+"/.", container+":"+dir.HostPath); err != nil {
			return err
		}
		if _, err := dockerOutput("exec", "-u", "root", container, "chown", "-R", "agent:agent", dir.HostPath); err != nil {
			return err
		}
	}
	return nil
}

// setupClaudeSandbox pre-accepts claude's onboarding + workspace-trust
// prompts inside the throwaway container (pattern from OpenRouter spawn).
// Deliberately NOT bypassPermissionsModeAccepted — auto-YOLO stays user opt-in.
func setupClaudeSandbox(container string) error {
	config := `{"hasCompletedOnboarding":true,"projects":{"/workspace":{"hasTrustDialogAccepted":true}}}`
	script := fmt.Sprintf("printf '%%s' '%s' > ~/.claude.json && chmod 600 ~/.claude.json", config)
	_, err := dockerOutput("exec", container, "bash", "-c", script)
	return err
}

// RunSandbox launches the agent inside a throwaway container. The container
// is removed when the session ends.
func RunSandbox(def *AgentDef, flags GatewayFlags, passthrough []string) (int, error) {
	if err := CheckDocker(); err != nil {
		return 1, err
	}
	pruneOrphans()
	if err := ensureImage(def, flags.Rebuild); err != nil {
		return 1, fmt.Errorf("sandbox image build failed: %w", err)
	}

	creds, err := ResolveCredentials(os.Getenv)
	if err != nil {
		return 1, err
	}

	container := containerName(def.Name)
	cwd, _ := os.Getwd()
	if flags.MountCwd {
		fmt.Fprintf(os.Stderr, "Warning: --mount-cwd mounts %s read-write at /workspace inside the sandbox.\n", cwd)
	}
	if _, err := dockerOutput(RunContainerArgs(def.Name, container, cwd, flags.MountCwd)...); err != nil {
		return 1, err
	}
	removeContainer := func() { _ = exec.Command("docker", "rm", "-f", container).Run() }
	defer removeContainer()

	plan, err := def.Resolve(&AgentContext{
		Creds:  creds,
		Getenv: os.Getenv,
		Flags:  flags,
		ExecProbe: func(binary string, args ...string) (string, error) {
			return dockerOutput(append([]string{"exec", container, binary}, args...)...)
		},
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

	if err := copyTempDirs(container, plan.TempDirs); err != nil {
		return 1, fmt.Errorf("sandbox config delivery failed: %w", err)
	}
	env := plan.Env
	if def.Name == "claude" {
		if err := setupClaudeSandbox(container); err != nil {
			return 1, fmt.Errorf("claude sandbox setup failed: %w", err)
		}
		env = map[string]string{"CLAUDE_CODE_SKIP_ONBOARDING": "1"}
		for k, v := range plan.Env {
			env[k] = v
		}
	}

	argv := append(append([]string{def.Binary}, plan.PreArgs...), passthrough...)
	tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	execArgs := ExecArgs(container, tty, env, argv)

	if flags.DryRun {
		fmt.Printf("docker %s\n", strings.Join(redactArgs(execArgs, creds.APIKey), " "))
		return 0, nil
	}

	// docker CLI handles raw TTY + signal forwarding into the container.
	return RunChild("docker", execArgs, nil)
}

func redactArgs(args []string, apiKey string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, apiKey, "<redacted>")
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
