package launch

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

var opencodePrompt = &PromptMapping{
	Flags:  []string{"-p", "--prompt"},
	ToArgs: func(v string) []string { return []string{"run", v} },
}

func TestParseArgvExtractsFlags(t *testing.T) {
	flags, rest, err := ParseArgv([]string{
		"--model", "openai/gpt-5-mini",
		"--models=anthropic/claude-sonnet-4-6,openai/gpt-5-mini",
		"--base-url=https://api.orq.ai/v3/router",
		"models",
	}, ParseArgvOptions{AllowModels: true})
	if err != nil {
		t.Fatal(err)
	}
	if flags.Model != "openai/gpt-5-mini" ||
		flags.Models != "anthropic/claude-sonnet-4-6,openai/gpt-5-mini" ||
		flags.BaseURL != "https://api.orq.ai/v3/router" {
		t.Fatalf("flags: %+v", flags)
	}
	if !reflect.DeepEqual(rest, []string{"models"}) {
		t.Fatalf("rest: %v", rest)
	}
}

func TestParseArgvPromptMapping(t *testing.T) {
	for _, flag := range []string{"-p", "--prompt"} {
		_, rest, err := ParseArgv([]string{flag, "hello"}, ParseArgvOptions{Prompt: opencodePrompt, AllowModels: true})
		if err != nil || !reflect.DeepEqual(rest, []string{"run", "hello"}) {
			t.Fatalf("%s: rest=%v err=%v", flag, rest, err)
		}
	}
}

func TestParseArgvPassthroughDelimiter(t *testing.T) {
	flags, rest, err := ParseArgv([]string{"--model", "openai/gpt-5-mini", "--", "--help", "--sandbox"}, ParseArgvOptions{AllowModels: true})
	if err != nil || flags.Model != "openai/gpt-5-mini" {
		t.Fatalf("flags=%+v err=%v", flags, err)
	}
	if !reflect.DeepEqual(rest, []string{"--help", "--sandbox"}) {
		t.Fatalf("rest: %v", rest)
	}
	if flags.Help || flags.DryRun {
		t.Fatal("flags after -- must not be parsed")
	}
}

func TestParseArgvMissingValues(t *testing.T) {
	for _, argv := range [][]string{{"--model"}, {"--models"}, {"--base-url"}, {"-p"}} {
		_, _, err := ParseArgv(argv, ParseArgvOptions{Prompt: opencodePrompt, AllowModels: true})
		if err == nil || !strings.Contains(err.Error(), "expects a value") {
			t.Fatalf("%v: want value error, got %v", argv, err)
		}
	}
}

func TestParseArgvModelsDisallowed(t *testing.T) {
	// claude does not accept --models; it must pass through.
	_, rest, err := ParseArgv([]string{"--models", "x"}, ParseArgvOptions{AllowModels: false})
	if err != nil || !reflect.DeepEqual(rest, []string{"--models", "x"}) {
		t.Fatalf("rest=%v err=%v", rest, err)
	}
}

func TestParseArgvLaunchFlags(t *testing.T) {
	flags, rest, err := ParseArgv([]string{"--mcp", "--no-skills", "--dry-run", "--no-fetch-models"}, ParseArgvOptions{AllowModels: true})
	if err != nil || len(rest) != 0 {
		t.Fatalf("rest=%v err=%v", rest, err)
	}
	if !flags.MCP || !flags.NoSkills || !flags.DryRun || !flags.NoFetchModels {
		t.Fatalf("flags: %+v", flags)
	}
}

// Sandbox support is gone, so the flags it owned belong to the agent now. The
// collision this un-shadows is real: codex has its own --sandbox <mode>.
func TestSandboxFlagsBelongToTheAgent(t *testing.T) {
	flags, rest, err := ParseArgv([]string{"--sandbox", "read-only"}, ParseArgvOptions{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(rest, []string{"--sandbox", "read-only"}) {
		t.Errorf("rest = %v, want the agent to receive both", rest)
	}
	// The launcher consumed nothing, so parsing stopped at --sandbox.
	if flags.DryRun {
		t.Error("launcher kept parsing past an agent-owned flag")
	}
	for _, arg := range []string{"--local", "--mount-cwd", "--rebuild"} {
		_, rest, err := ParseArgv([]string{arg}, ParseArgvOptions{})
		if err != nil || !reflect.DeepEqual(rest, []string{arg}) {
			t.Errorf("%s: rest=%v err=%v, want passthrough", arg, rest, err)
		}
	}
}

func TestParseArgvHelp(t *testing.T) {
	flags, _, err := ParseArgv([]string{"-h"}, ParseArgvOptions{})
	if err != nil || !flags.Help {
		t.Fatalf("-h: %+v err=%v", flags, err)
	}
	flags, _, _ = ParseArgv([]string{"--help"}, ParseArgvOptions{})
	if !flags.Help {
		t.Fatal("--help not detected")
	}
	// Leading launcher flags don't disqualify help...
	flags, _, _ = ParseArgv([]string{"--mcp", "-h"}, ParseArgvOptions{})
	if !flags.Help {
		t.Fatal("-h after launcher flag not detected")
	}
	// ...but once an agent arg has passed through, -h belongs to the agent.
	flags, rest, _ := ParseArgv([]string{"exec", "-h"}, ParseArgvOptions{})
	if flags.Help {
		t.Fatal("non-leading -h must pass through to the agent")
	}
	if !reflect.DeepEqual(rest, []string{"exec", "-h"}) {
		t.Fatalf("rest: %v", rest)
	}
}

func TestParseArgvUnknownPassthrough(t *testing.T) {
	_, rest, err := ParseArgv([]string{"--resume", "-c", "config.json"}, ParseArgvOptions{AllowModels: true})
	if err != nil || !reflect.DeepEqual(rest, []string{"--resume", "-c", "config.json"}) {
		t.Fatalf("rest=%v err=%v", rest, err)
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/x", "ANTHROPIC_API_KEY=old"}
	got := MergeEnv(base, map[string]string{"ANTHROPIC_API_KEY": "", "NEW": "v"})

	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "ANTHROPIC_API_KEY=old") {
		t.Fatal("old value not replaced")
	}
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=\n") && !strings.HasSuffix(joined, "ANTHROPIC_API_KEY=") {
		if !slices.Contains(got, "ANTHROPIC_API_KEY=") {
			t.Fatalf("empty override lost: %v", got)
		}
	}
	if !slices.Contains(got, "PATH=/usr/bin") || !slices.Contains(got, "NEW=v") {
		t.Fatalf("merge broken: %v", got)
	}
}
