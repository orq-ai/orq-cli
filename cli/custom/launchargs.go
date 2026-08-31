package custom

import (
	"strings"

	"github.com/spf13/cobra"
)

// passthroughCommands run with cobra's DisableFlagParsing, so cobra parses no
// flags at all for them — not even the root's own. Each needs its globals
// lifted out of argv here instead.
var passthroughCommands = map[string]bool{
	"launch": true,
	"orqi":   true,
}

// splitPassthroughGlobals separates orq's own global flags from the rest of
// argv on an invocation of a passthroughCommand. They may be typed on either
// side of the command word (`orq --profile acme launch claude` and
// `orq launch --profile acme claude` both work); the first argument the
// command owns ends them.
//
// A passthrough command runs with DisableFlagParsing so every one of its own
// flags reaches it verbatim — including ones that collide with ours (codex's
// own `-p profile`). But cobra reads that as "parse no flags at all for this
// invocation": the root's own persistent flags are never parsed either, and
// every argument, wherever it was typed, is handed to the command. So
// `orq launch --profile acme claude` forwarded `--profile` to claude, and
// `orq orqi --profile staging "why"` would forward `--profile` to orqi.
//
// The dividing line is the first argument the command owns: before it the
// flags are orq's, after it they are the command's. The caller parses the
// returned globals into the root's persistent flags before Execute, so the
// PreRun chain resolves the profile and host — and injects the session
// token — the same way it does for every other command.
func splitPassthroughGlobals(root *cobra.Command, args []string) (globals, rest []string) {
	globals, cmdIndex := leadingGlobals(root, args)
	if cmdIndex < 0 {
		return nil, args
	}
	name := args[cmdIndex]
	i := cmdIndex + 1
	for ; i < len(args); i++ {
		flagName, hasValue := globalFlagName(root, args[i])
		if flagName == "" {
			break // the command's own argument, or a flag we do not own: leave it be
		}
		globals = append(globals, args[i])
		if hasValue && i+1 < len(args) {
			i++
			globals = append(globals, args[i])
		}
	}
	if len(globals) == 0 {
		return nil, args
	}
	rest = append(rest, name)
	return globals, append(rest, args[i:]...)
}

// leadingGlobals returns orq's global flags typed before a passthroughCommand
// word and where that word sits, or -1 when this invocation is not one of
// them. Only global flags may precede it; the first other word means cobra is
// resolving some other command.
func leadingGlobals(root *cobra.Command, args []string) ([]string, int) {
	var globals []string
	for i := 0; i < len(args); i++ {
		if passthroughCommands[args[i]] {
			return globals, i
		}
		name, hasValue := globalFlagName(root, args[i])
		if name == "" {
			return nil, -1
		}
		globals = append(globals, args[i])
		if hasValue && i+1 < len(args) {
			i++
			globals = append(globals, args[i])
		}
	}
	return nil, -1
}

// globalFlagName reports the root persistent flag arg names, and whether it
// consumes the following argument as its value. Returns "" for anything that
// is not one of orq's own globals.
func globalFlagName(root *cobra.Command, arg string) (string, bool) {
	flags := root.PersistentFlags()
	switch {
	case strings.HasPrefix(arg, "--"):
		name, _, hasEq := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		f := flags.Lookup(name)
		if f == nil {
			return "", false
		}
		return name, !hasEq && f.NoOptDefVal == ""
	case strings.HasPrefix(arg, "-") && len(arg) > 1:
		f := flags.ShorthandLookup(arg[1:2])
		if f == nil {
			return "", false
		}
		return f.Name, len(arg) == 2 && f.NoOptDefVal == ""
	}
	return "", false
}
