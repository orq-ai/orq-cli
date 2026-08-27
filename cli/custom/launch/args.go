package launch

import (
	"fmt"
	"slices"
	"strings"
)

// PromptMapping maps an agent-agnostic prompt flag (-p/--prompt) to the
// agent's own CLI syntax (e.g. `run "..."` for opencode, `exec ...` for
// codex). Agents without a mapping (claude) pass -p straight through.
type PromptMapping struct {
	Flags  []string
	ToArgs func(value string) []string
}

// ParseArgvOptions controls per-agent parsing behavior.
type ParseArgvOptions struct {
	Prompt      *PromptMapping
	AllowModels bool
}

// CompletionFlags returns the launcher-owned flags matching toComplete for
// shell completion (cobra flag parsing is disabled, so cobra can't enumerate
// them itself). The list mirrors ParseArgv's cases; TestCompletionFlagsMatchParser
// asserts every entry is actually consumed by ParseArgv. Returns nil unless
// toComplete looks like a flag — anything else belongs to the agent's own CLI.
func CompletionFlags(def *AgentDef, toComplete string) []string {
	if !strings.HasPrefix(toComplete, "-") {
		return nil
	}
	flags := []string{"--model", "--base-url", "--no-fetch-models", "--mcp", "--no-mcp",
		"--no-skills", "--dry-run", "--help"}
	if def.AllowModels {
		flags = append(flags, "--models")
	}
	if def.Prompt != nil {
		flags = append(flags, def.Prompt.Flags...)
	}
	var out []string
	for _, f := range flags {
		if strings.HasPrefix(f, toComplete) {
			out = append(out, f)
		}
	}
	return out
}

// ParseArgv is the one arg parser for all agents (subcommands run with
// cobra DisableFlagParsing). Launcher-owned flags — --model/--models/
// --base-url/--no-fetch-models/--mcp/--no-mcp/--no-skills/--dry-run/-h — are recognized
// only at the FRONT of argv: the first arg the launcher doesn't own ends
// launcher parsing and everything from there on belongs to the agent verbatim.
// This keeps agent flags that collide with ours (codex's -p profile) reachable:
// `orq launch codex exec -p work` passes both to codex. A
// leading `--` ends launcher parsing explicitly; a later `--` is the agent's.
// Prompt-mapped flags (-p/--prompt) expand to the agent's own syntax (e.g.
// `run <text>`) and land at the front of the agent argv by construction.
func ParseArgv(argv []string, opts ParseArgvOptions) (GatewayFlags, []string, error) {
	flags := GatewayFlags{MCP: true}
	var prompt []string

	takeValue := func(arg string, i *int) (string, error) {
		*i++
		if *i >= len(argv) || argv[*i] == "" || strings.HasPrefix(argv[*i], "-") {
			return "", fmt.Errorf("flag %s expects a value", arg)
		}
		return argv[*i], nil
	}
	// eqValue handles the --flag=value form; empty values are an error, not
	// passthrough.
	eqValue := func(arg, prefix string) (string, bool, error) {
		v, ok := strings.CutPrefix(arg, prefix)
		if !ok {
			return "", false, nil
		}
		if v == "" {
			return "", true, fmt.Errorf("flag %s expects a value", strings.TrimSuffix(prefix, "="))
		}
		return v, true, nil
	}

	i := 0
scan:
	for ; i < len(argv); i++ {
		arg := argv[i]

		if v, ok, err := eqValue(arg, "--model="); ok {
			if err != nil {
				return flags, nil, err
			}
			flags.Model = v
			continue
		}
		if opts.AllowModels {
			if v, ok, err := eqValue(arg, "--models="); ok {
				if err != nil {
					return flags, nil, err
				}
				flags.Models = v
				continue
			}
		}
		if v, ok, err := eqValue(arg, "--base-url="); ok {
			if err != nil {
				return flags, nil, err
			}
			flags.BaseURL = v
			continue
		}

		switch {
		case arg == "--":
			i++ // explicit end of launcher flags; rest is the agent's
			break scan
		case arg == "-h" || arg == "--help":
			flags.Help = true
		case arg == "--model":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.Model = v
		case opts.AllowModels && arg == "--models":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.Models = v
		case arg == "--base-url":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.BaseURL = v
		case arg == "--no-fetch-models":
			flags.NoFetchModels = true
		case arg == "--mcp":
			flags.MCP = true
		case arg == "--no-mcp":
			flags.MCP = false
		case arg == "--no-skills":
			flags.NoSkills = true
		case arg == "--dry-run":
			flags.DryRun = true
		case opts.Prompt != nil && slices.Contains(opts.Prompt.Flags, arg):
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			prompt = opts.Prompt.ToArgs(v)
		default:
			break scan // first agent-owned arg; stop consuming
		}
	}

	passthrough := append(prompt, argv[i:]...)
	if len(passthrough) == 0 {
		passthrough = nil
	}
	return flags, passthrough, nil
}
