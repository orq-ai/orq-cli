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
	if flags.Help || flags.Sandbox {
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
	flags, rest, err := ParseArgv([]string{"--sandbox", "--mount-cwd", "--rebuild", "--dry-run", "--no-fetch-models"}, ParseArgvOptions{AllowModels: true})
	if err != nil || len(rest) != 0 {
		t.Fatalf("rest=%v err=%v", rest, err)
	}
	if !flags.Sandbox || !flags.MountCwd || !flags.Rebuild || !flags.DryRun || !flags.NoFetchModels {
		t.Fatalf("flags: %+v", flags)
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
	flags, _, _ = ParseArgv([]string{"--sandbox", "-h"}, ParseArgvOptions{})
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

// --sandbox skipped the safety prompt; local mode had no way to say the same
// thing, so the only escape from the prompt was towards the sandbox or the
// blunt ORQ_LAUNCH_NON_INTERACTIVE.
func TestLocalFlagIsParsedAndExclusiveWithSandbox(t *testing.T) {
	flags, rest, err := ParseArgv([]string{"--local"}, ParseArgvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !flags.Local || flags.Sandbox {
		t.Errorf("--local: Local=%v Sandbox=%v", flags.Local, flags.Sandbox)
	}
	if len(rest) != 0 {
		t.Errorf("--local leaked to the agent: %v", rest)
	}

	// It is ours, not the agent's: everything after -- still passes through.
	_, rest, err = ParseArgv([]string{"--", "--local"}, ParseArgvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "--local" {
		t.Errorf("after --, --local must reach the agent: %v", rest)
	}
}
