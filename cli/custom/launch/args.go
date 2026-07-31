package launch

import "fmt"

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

// ParseArgv is the one arg parser for all agents (subcommands run with
// cobra DisableFlagParsing): shared --model/--models/--base-url/
// --no-fetch-models plus launch-specific --sandbox/--mount-cwd/--rebuild/
// --dry-run, `--` passthrough, and -h/--help (cobra can't auto-handle help
// with flag parsing disabled). Unrecognized args pass through to the agent.
func ParseArgv(argv []string, opts ParseArgvOptions) (GatewayFlags, []string, error) {
	var flags GatewayFlags
	var passthrough []string

	takeValue := func(arg string, i *int) (string, error) {
		*i++
		if *i >= len(argv) || argv[*i] == "" {
			return "", fmt.Errorf("flag %s expects a value", arg)
		}
		return argv[*i], nil
	}

	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		if arg == "--" {
			passthrough = append(passthrough, argv[i+1:]...)
			break
		}

		switch {
		case arg == "-h" || arg == "--help":
			flags.Help = true
		case arg == "--model":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.Model = v
		case len(arg) > 8 && arg[:8] == "--model=":
			flags.Model = arg[8:]
		case opts.AllowModels && arg == "--models":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.Models = v
		case opts.AllowModels && len(arg) > 9 && arg[:9] == "--models=":
			flags.Models = arg[9:]
		case arg == "--base-url":
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			flags.BaseURL = v
		case len(arg) > 11 && arg[:11] == "--base-url=":
			flags.BaseURL = arg[11:]
		case arg == "--no-fetch-models":
			flags.NoFetchModels = true
		case arg == "--no-mcp":
			flags.NoMCP = true
		case arg == "--no-skills":
			flags.NoSkills = true
		case arg == "--sandbox":
			flags.Sandbox = true
		case arg == "--mount-cwd":
			flags.MountCwd = true
		case arg == "--rebuild":
			flags.Rebuild = true
		case arg == "--dry-run":
			flags.DryRun = true
		case opts.Prompt != nil && containsString(opts.Prompt.Flags, arg):
			v, err := takeValue(arg, &i)
			if err != nil {
				return flags, nil, err
			}
			passthrough = append(passthrough, opts.Prompt.ToArgs(v)...)
		default:
			passthrough = append(passthrough, arg)
		}
	}

	return flags, passthrough, nil
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
