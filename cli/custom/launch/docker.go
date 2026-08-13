package launch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN useradd -m -s /bin/bash agent
USER agent
WORKDIR /home/agent
ENV PATH="/home/agent/.npm-global/bin:/home/agent/.claude/local/bin:/home/agent/.local/bin:${PATH}"
RUN npm config set prefix /home/agent/.npm-global && %s && command -v %s
CMD ["sleep", "infinity"]
`

// agentInstallCmd is the Dockerfile RUN payload installing the agent CLI.
// claude prefers its native installer (tracks releases better than npm).
// pipefail (SHELL above) matters: without it a failed `curl | bash` exits 0,
// the fallback never runs, and docker caches a broken image; the trailing
// `command -v <binary>` makes any silent install failure fail the build.
func agentInstallCmd(def *AgentDef) string {
	if def.Name == "claude" {
		return "((curl -fsSL https://claude.ai/install.sh | bash) || npm install -g " + def.NpmPackage + ")"
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

// ExecArgs deliberately emits name-only -e flags: -e K=V would put secrets
// (ORQ_API_KEY, ANTHROPIC_AUTH_TOKEN) in host `ps` for the whole session.
// docker exec reads each value from the docker client's environment, which
// RunSandbox seeds via RunChild's env override.
func ExecArgs(container string, tty bool, env map[string]string, argv []string) []string {
	args := []string{"exec", "-i"}
	if tty {
		args = append(args, "-t")
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k)
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
		hint = "install Docker Desktop (https://www.docker.com/products/docker-desktop/)"
	}
	return fmt.Errorf("docker is not available (binary missing or daemon not running); --sandbox needs it. %s", hint)
}

// pruneOrphans removes exited launch containers. The status=exited filter is
// what protects concurrent live sessions — do not remove it. Known gap: a
// SIGKILL/host-crash leftover keeps running `sleep infinity` and is never
// `exited`, so it is not reclaimed here (see README for manual cleanup).
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
	cmd.Stdin = strings.NewReader(fmt.Sprintf(dockerfileTemplate, agentInstallCmd(def), def.Binary))
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
// prompts inside the throwaway container, so a sandbox session does not open
// on a dialog the user cannot meaningfully answer.
// Deliberately NOT bypassPermissionsModeAccepted — auto-YOLO stays user opt-in.
func setupClaudeSandbox(container string) error {
	// Trust both working dirs: /workspace is the cwd under --mount-cwd, but
	// the Dockerfile WORKDIR (where a plain --sandbox session starts) is
	// /home/agent — keying only /workspace left the trust dialog visible.
	config := `{"hasCompletedOnboarding":true,"projects":{"/workspace":{"hasTrustDialogAccepted":true},"/home/agent":{"hasTrustDialogAccepted":true}}}`
	script := fmt.Sprintf("printf '%%s' '%s' > ~/.claude.json && chmod 600 ~/.claude.json", config)
	_, err := dockerOutput("exec", container, "bash", "-c", script)
	return err
}

// RunSandbox launches the agent inside a throwaway container. The container
// is removed when the session ends. --dry-run resolves and prints the exec
// command without touching docker at all (no build, no container, nothing
// written to the user's config). Resolution itself still runs: temp config
// files are created and cleaned up, and /v2/models is still fetched unless
// --no-fetch-models.
func RunSandbox(def *AgentDef, flags GatewayFlags, passthrough []string) (int, error) {
	if runtime.GOOS == "windows" {
		// ponytail: Windows would need volume/`docker cp` path mapping into
		// the Linux container (C:\… paths break both); gate until someone
		// needs it.
		return 1, errors.New("--sandbox is not supported on Windows yet; run locally or use WSL")
	}
	dry := flags.DryRun
	if !dry {
		if err := CheckDocker(); err != nil {
			return 1, err
		}
		pruneOrphans()
		if err := ensureImage(def, flags.Rebuild); err != nil {
			return 1, fmt.Errorf("sandbox image build failed: %w", err)
		}
	}

	creds, err := resolveCredentialsOrLogin(os.Getenv, !flags.DryRun)
	if err != nil {
		return 1, err
	}
	reportCredentialNotices(creds, flags)

	container := containerName(def.Name)
	cwd, err := os.Getwd()
	if err != nil && flags.MountCwd {
		return 1, fmt.Errorf("--mount-cwd: cannot determine current directory: %w", err)
	}
	if flags.MountCwd {
		fmt.Fprintf(os.Stderr, "Warning: --mount-cwd mounts %s read-write at /workspace inside the sandbox.\n", cwd)
	}
	ctx := &AgentContext{
		Creds:  creds,
		Getenv: os.Getenv,
		Flags:  flags,
	}
	if !dry {
		if _, err := dockerOutput(RunContainerArgs(def.Name, container, cwd, flags.MountCwd)...); err != nil {
			return 1, err
		}
		defer func() { _ = exec.Command("docker", "rm", "-f", container).Run() }()
		// ExecProbe is nil on dry-run: resolvers nil-guard it and skip
		// container probes (codex catalog degrades to a warning).
		ctx.ExecProbe = func(binary string, args ...string) (string, error) {
			return dockerOutput(append([]string{"exec", container, binary}, args...)...)
		}
	}

	plan, err := def.Resolve(ctx)
	if err != nil {
		return 1, err
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	for _, w := range plan.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	if !dry {
		if err := copyTempDirs(container, plan.TempDirs); err != nil {
			return 1, fmt.Errorf("sandbox config delivery failed: %w", err)
		}
	}
	env := plan.Env
	if def.Name == "claude" {
		if !dry {
			if err := setupClaudeSandbox(container); err != nil {
				return 1, fmt.Errorf("claude sandbox setup failed: %w", err)
			}
		}
		env = map[string]string{"CLAUDE_CODE_SKIP_ONBOARDING": "1"}
		for k, v := range plan.Env {
			env[k] = v
		}
	}

	argv := append(append([]string{def.Binary}, plan.PreArgs...), passthrough...)
	tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	execArgs := ExecArgs(container, tty, env, argv)

	if dry {
		fmt.Printf("docker %s\n", strings.Join(execArgs, " "))
		for _, k := range sortedKeys(env) {
			v := env[k]
			if v != "" && v == creds.APIKey {
				v = "<redacted>"
			}
			fmt.Printf("env: %s=%s\n", k, v)
		}
		return 0, nil
	}

	// docker CLI handles raw TTY + signal forwarding into the container.
	// env goes to the docker client process; exec's name-only -e flags
	// propagate the values (see ExecArgs).
	return RunChild("docker", execArgs, env)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
