package commands

import (
	"fmt"
	"strings"
)

// orqiFlags are the flags this command owns on `orq orqi`. Everything else is
// orqi's, except orq's own global --profile, which splitPassthroughGlobals
// (cli/custom/launchargs.go) parses onto the root before cobra dispatches.
type orqiFlags struct {
	Help    bool
	Install bool
}

// orqiFlagNames mirrors what parseOrqiArgv consumes;
// TestOrqiCompletionFlagsMatchParser asserts the two agree.
var orqiFlagNames = []string{"-h", "--help", "--install"}

// orqiGlobalFlagNames are orq's own root flags, which work on an orqi line
// because splitPassthroughGlobals lifts them out of argv before cobra
// dispatches. Offered for completion; never seen by parseOrqiArgv.
var orqiGlobalFlagNames = []string{"--no-input", "--profile"}

// parseOrqiArgv recognizes orq's own flags only at the FRONT of argv: the
// first argument orq does not own ends parsing and everything from there
// belongs to orqi verbatim, so a flag orqi grows later can never collide with
// one of ours. A leading `--` ends parsing explicitly. Same convention as
// launch.ParseArgv (cli/custom/launch/args.go), whose flag set and
// GatewayFlags return are gateway-specific and so not reusable here.
//
// cobra parses none of this: `orq orqi` runs with DisableFlagParsing, which
// leaves even the root's persistent --profile unparsed. It arrives at the
// front of argv, which is where this scanner reads it.
func parseOrqiArgv(argv []string) (orqiFlags, []string, error) {
	var flags orqiFlags
	i := 0
scan:
	for ; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			i++
			break scan
		case arg == "-h" || arg == "--help":
			flags.Help = true
		case arg == "--install":
			flags.Install = true
		default:
			break scan
		}
	}
	rest := argv[i:]
	// --install starts no session, so there is no child for trailing
	// arguments to belong to. Refusing beats dropping them silently.
	if flags.Install && len(rest) > 0 {
		return flags, nil, fmt.Errorf("--install takes no arguments, got %q", strings.Join(rest, " "))
	}
	return flags, rest, nil
}

// orqiCompletionFlags returns orq's own flags matching toComplete. Cobra
// cannot enumerate them itself with flag parsing disabled. Anything that does
// not look like a flag belongs to orqi's own CLI. orq's globals are offered
// too, even though this file does not parse them: on an orqi line they work.
func orqiCompletionFlags(toComplete string) []string {
	if !strings.HasPrefix(toComplete, "-") {
		return nil
	}
	var out []string
	for _, f := range append(append([]string{}, orqiFlagNames...), orqiGlobalFlagNames...) {
		if strings.HasPrefix(f, toComplete) {
			out = append(out, f)
		}
	}
	return out
}
