package commands

import (
	"fmt"
	"sort"
	"strings"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// NewListProfilesCommand lists saved credentials. The generated Bartolo
// command uses Formatter.Format, which is correct for structured output but
// makes the default TOON representation the terminal view too. The custom
// command opts into Bartolo's existing table renderer only for an interactive
// default-format invocation.
func NewListProfilesCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list-profiles",
		Aliases: []string{"ls"},
		Short:   "List available configured authentication profiles",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := listAuthProfiles()
			if len(rows) == 0 {
				if wantsHumanView(cmd) {
					fmt.Fprintf(bartolocli.Stdout, "No profiles configured. Use `%s auth setup` to add one.\n", cmd.Root().CommandPath())
					return nil
				}
				return emit(map[string]any{"profiles": []map[string]any{}})
			}

			payload := map[string]any{"profiles": rows}
			if !wantsHumanView(cmd) {
				return emit(payload)
			}

			return renderProfileTable(payload, profileTableColumns(rows))
		},
	}
}

func listAuthProfiles() []map[string]any {
	profiles := bartolocli.Creds.GetStringMap("profiles")

	// A session login has no bartolo profile at all — its gateway key and
	// workspace live in our own state (see auth.MigrateProfileState) — so the
	// listing is the union, or logging in would leave `auth list-profiles`
	// reporting nothing configured.
	seen := map[string]bool{}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
		seen[name] = true
	}
	for _, name := range auth.StateProfiles() {
		if !seen[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		stored, _ := profiles[name].(map[string]interface{})
		// Copy before decorating: profiles is bartolo's live in-memory
		// credentials map, and merging state into it would mutate what every
		// later reader in this process sees.
		profile := make(map[string]interface{}, len(stored)+len(auth.StateFields))
		for field, value := range stored {
			profile[field] = value
		}
		for field, value := range auth.StateOf(name) {
			if value != "" {
				profile[field] = value
			}
		}

		typeName, _ := profile["type"].(string)
		row := map[string]any{"name": name, "type": typeName}

		keys := []string{"server"}
		if handler := bartolocli.AuthHandlers[typeName]; handler != nil {
			keys = append(keys, handler.ProfileKeys()...)
		} else {
			for key := range profile {
				keys = append(keys, key)
			}
			sort.Strings(keys[1:])
		}

		for _, key := range keys {
			field := strings.ReplaceAll(key, "-", "_")
			if field == "type" {
				continue
			}
			if value, ok := profile[field]; ok {
				row[field] = maskProfileSecret(field, value)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func profileTableColumns(rows []map[string]any) []string {
	// Keep the identity columns stable even when a profile has no explicit
	// server or credential field. The profile type is an internal Bartolo
	// handler name (normally empty) and is not useful to terminal users.
	columns := []string{"name", "server"}
	for _, candidate := range []string{"api_key", "gateway_key", "gateway_key_id", "gateway_key_expires_at"} {
		for _, row := range rows {
			if _, ok := row[candidate]; ok {
				columns = append(columns, candidate)
				break
			}
		}
	}
	return columns
}

func renderProfileTable(payload map[string]any, columns []string) error {
	// Tables render only when the format is `table`, and this CLI serializes to
	// TOON, so ask for one explicitly for this human-only render. Machine-format
	// paths never enter here, and restore puts the command's own format back.
	restore, err := bartolocli.SetOutputFormat("table")
	if err != nil {
		return err
	}
	defer restore()
	return bartolocli.FormatList(payload, columns...)
}

func maskProfileSecret(name string, value any) any {
	lower := strings.ToLower(name)
	for _, hint := range []string{"key", "token", "secret", "password", "passphrase", "credential", "signature"} {
		if strings.Contains(lower, hint) {
			s, ok := value.(string)
			if !ok || s == "" {
				return value
			}
			runes := []rune(s)
			if len(runes) < 12 {
				return "********"
			}
			return string(runes[:4]) + "********" + string(runes[len(runes)-4:])
		}
	}
	return value
}
