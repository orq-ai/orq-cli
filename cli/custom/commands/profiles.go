package commands

import (
	"fmt"
	"sort"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	if len(profiles) == 0 {
		return nil
	}

	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		profile, ok := profiles[name].(map[string]interface{})
		if !ok {
			continue
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
	// Bartolo's table renderer is intentionally tied to JSON as its interactive
	// mode. The CLI's normal default is TOON, so temporarily select the table
	// mode for this one human-only render. Machine-format paths never enter
	// here, and the viper value is restored before the command returns.
	previous := viper.GetString("output-format")
	viper.Set("output-format", "json")
	defer viper.Set("output-format", previous)
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
