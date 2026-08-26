package commands

import (
	"fmt"
	"sort"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// NewListProfilesCommand replaces bartolo's `auth list-profiles`, which printed
// every stored profile field verbatim in a table — including the API key — and
// ignored --json/-o entirely (BACK-2113). Secrets are masked here and the
// payload goes through the configured formatter like every other command.
func NewListProfilesCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list-profiles",
		Aliases: []string{"ls"},
		Short:   "List configured authentication profiles (secrets masked)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles := listProfiles()
			if wantsHumanView(cmd) {
				printProfiles(profiles)
				return nil
			}
			return emit(map[string]any{"profiles": profiles})
		},
	}
}

// listProfiles reads the credentials file bartolo owns and returns one entry
// per profile, sorted by name, with every secret value already masked.
func listProfiles() []map[string]string {
	raw := bartolocli.Creds.GetStringMap("profiles")
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	profiles := make([]map[string]string, 0, len(names))
	for _, name := range names {
		fields, ok := raw[name].(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]string{"name": name}
		for key, value := range fields {
			s, ok := value.(string)
			if !ok || s == "" {
				continue
			}
			if secretKey(key) {
				s = maskToken(s)
			}
			entry[key] = s
		}
		profiles = append(profiles, entry)
	}
	return profiles
}

// secretKey reports whether a profile field holds a credential. Matched on the
// name rather than a fixed list so a field added later (by bartolo or by a new
// auth handler) is masked by default instead of leaking until someone notices.
func secretKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range []string{"key", "token", "secret", "password"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func printProfiles(profiles []map[string]string) {
	if len(profiles) == 0 {
		info("No profiles configured. Use `orq auth setup` to add one.")
		return
	}
	for _, profile := range profiles {
		fmt.Fprintln(bartolocli.Stdout, profile["name"])
		keys := make([]string, 0, len(profile))
		width := 0
		for key := range profile {
			if key == "name" {
				continue
			}
			keys = append(keys, key)
			if len(key) > width {
				width = len(key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			kv(width, strings.ReplaceAll(key, "_", "-"), "%s", profile[key])
		}
	}
}
