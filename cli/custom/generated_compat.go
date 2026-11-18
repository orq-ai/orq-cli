package custom

import (
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// generatedAuthType is the name bartolo >= 0.19 gives the spec's one security
// scheme. Older generators registered it anonymously.
const generatedAuthType = "ApiKey"

// restoreAnonymousAuth re-registers the generated API-key handler without a
// name. A named handler answers only to its own name, so the rename stranded
// every profile saved as "apikey" by an earlier build; the anonymous one
// authenticates any stored type, and `auth profile add` is one command again.
func restoreAnonymousAuth(root *cobra.Command) {
	handler, ok := bartolocli.AuthHandlers[generatedAuthType]
	if !ok {
		return
	}
	// A second named scheme would mean the spec really is multi-auth, where
	// names are needed. The registry outlives Init, so a "" from an earlier
	// build in the same process (tests) is replaced, not counted.
	for name := range bartolocli.AuthHandlers {
		if name != "" && name != generatedAuthType {
			return
		}
	}
	if add := commandAt(root, "auth", "profile", "add"); add != nil {
		add.RemoveCommand(childCommand(add, generatedAuthType)) // nil-safe
	}
	delete(bartolocli.AuthHandlers, generatedAuthType)
	bartolocli.SetDefaultAuthType("")
	bartolocli.UseAuth("", handler)
}

func commandAt(root *cobra.Command, path ...string) *cobra.Command {
	cmd := root
	for _, name := range path {
		if cmd = childCommand(cmd, name); cmd == nil {
			return nil
		}
	}
	return cmd
}
