package launch

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildImageArgs(t *testing.T) {
	if got := BuildImageArgs("claude", false); !reflect.DeepEqual(got,
		[]string{"build", "-t", "orq-launch-claude:v1", "-"}) {
		t.Fatalf("got %v", got)
	}
	got := BuildImageArgs("claude", true)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--no-cache") || !strings.Contains(joined, "--pull") {
		t.Fatalf("rebuild flags missing: %v", got)
	}
}

func TestRunContainerArgs(t *testing.T) {
	got := RunContainerArgs("kimi", "orq-launch-kimi-abc123", "/repo", false)
	want := []string{"run", "-d", "--name", "orq-launch-kimi-abc123",
		"--label", "orq.launch=1", "orq-launch-kimi:v1", "sleep", "infinity"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}

	got = RunContainerArgs("kimi", "c", "/repo", true)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-v /repo:/workspace") || !strings.Contains(joined, "-w /workspace") {
		t.Fatalf("mount args missing: %v", got)
	}
}

func TestExecArgs(t *testing.T) {
	got := ExecArgs("c1", true, map[string]string{"B": "2", "A": "1"}, []string{"claude", "--resume"})
	want := []string{"exec", "-i", "-t", "-e", "A=1", "-e", "B=2", "c1", "claude", "--resume"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}

	got = ExecArgs("c1", false, nil, []string{"kimi"})
	if !reflect.DeepEqual(got, []string{"exec", "-i", "c1", "kimi"}) {
		t.Fatalf("no-tty: %v", got)
	}
}

func TestContainerNameUnique(t *testing.T) {
	a, b := containerName("claude"), containerName("claude")
	if a == b {
		t.Fatal("names must be unique")
	}
	if !strings.HasPrefix(a, "orq-launch-claude-") {
		t.Fatalf("prefix: %s", a)
	}
}

func TestAgentInstallCmd(t *testing.T) {
	claude := claudeAgent()
	if cmd := agentInstallCmd(&claude); !strings.Contains(cmd, "claude.ai/install.sh") ||
		!strings.Contains(cmd, "npm install -g @anthropic-ai/claude-code") {
		t.Fatalf("claude install fallback: %s", cmd)
	}
	kimi := kimiAgent()
	if cmd := agentInstallCmd(&kimi); cmd != "npm install -g @moonshot-ai/kimi-code" {
		t.Fatalf("kimi install: %s", cmd)
	}
}

func TestRedactArgs(t *testing.T) {
	got := redactArgs([]string{"-e", "ORQ_API_KEY=sk-secret", "claude"}, "sk-secret")
	if got[1] != "ORQ_API_KEY=<redacted>" || got[2] != "claude" {
		t.Fatalf("got %v", got)
	}
}
