package custom

import (
	"slices"
	"testing"
)

// The agent name is the dividing line: flags before it are orq's, flags after
// it belong to the agent and must survive verbatim — including the ones that
// collide with orq's own (codex takes its own -p profile).
func TestSplitLaunchGlobals(t *testing.T) {
	root := buildRoot(t)

	cases := []struct {
		name    string
		argv    []string
		globals []string
		rest    []string
	}{
		{
			name:    "flag before the agent is orq's",
			argv:    []string{"launch", "--profile", "acme", "claude"},
			globals: []string{"--profile", "acme"},
			rest:    []string{"launch", "claude"},
		},
		{
			name:    "equals form needs no value argument",
			argv:    []string{"launch", "--server=https://acme.internal", "claude", "--resume"},
			globals: []string{"--server=https://acme.internal"},
			rest:    []string{"launch", "claude", "--resume"},
		},
		{
			name:    "several globals, then the agent's own argv",
			argv:    []string{"launch", "--profile", "acme", "--server", "https://acme.internal", "codex", "exec", "-p", "work"},
			globals: []string{"--profile", "acme", "--server", "https://acme.internal"},
			rest:    []string{"launch", "codex", "exec", "-p", "work"},
		},
		{
			name: "after the agent name everything is the agent's",
			argv: []string{"launch", "claude", "--profile", "foo"},
			rest: []string{"launch", "claude", "--profile", "foo"},
		},
		{
			name: "launcher-owned flags stay where the launcher parses them",
			argv: []string{"launch", "claude", "--dry-run", "--model", "anthropic/claude-sonnet-5"},
			rest: []string{"launch", "claude", "--dry-run", "--model", "anthropic/claude-sonnet-5"},
		},
		{
			name:    "flag before the launch word is orq's too",
			argv:    []string{"--profile", "acme", "launch", "kimi"},
			globals: []string{"--profile", "acme"},
			rest:    []string{"launch", "kimi"},
		},
		{
			name:    "globals on both sides of the launch word",
			argv:    []string{"--server", "https://acme.internal", "launch", "--profile", "acme", "codex", "exec", "-p", "work"},
			globals: []string{"--server", "https://acme.internal", "--profile", "acme"},
			rest:    []string{"launch", "codex", "exec", "-p", "work"},
		},
		{
			name:    "a boolean global does not swallow the next argument",
			argv:    []string{"--no-color", "launch", "claude"},
			globals: []string{"--no-color"},
			rest:    []string{"launch", "claude"},
		},
		{
			name: "a bare -- ends orq's flags",
			argv: []string{"launch", "--", "claude", "--profile", "foo"},
			rest: []string{"launch", "--", "claude", "--profile", "foo"},
		},
		{
			name: "other commands are untouched",
			argv: []string{"--profile", "acme", "prompts", "list"},
			rest: []string{"--profile", "acme", "prompts", "list"},
		},
		{
			name: "a word that is not a flag before launch means another command",
			argv: []string{"agents", "launch", "--profile", "acme"},
			rest: []string{"agents", "launch", "--profile", "acme"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globals, rest := splitLaunchGlobals(root, tc.argv)
			if !slices.Equal(globals, tc.globals) {
				t.Errorf("globals = %q, want %q", globals, tc.globals)
			}
			if !slices.Equal(rest, tc.rest) {
				t.Errorf("rest = %q, want %q", rest, tc.rest)
			}
		})
	}
}
