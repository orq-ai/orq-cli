package commands

import (
	"fmt"
	"sort"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// HumanizeBuiltins replaces bartolo's machine-shaped records for `orq server`
// and `orq auth profile` with a readable view when a person is at a terminal.
// A pipe or an explicit -o keeps bartolo's output: that is the script contract.
//
// Each view renders from the record bartolo itself built, captured in place of
// the formatter, so the readable and the machine output cannot drift and
// bartolo's own validation and errors still run. RES-1640 tracks moving this
// into bartolo, after which this file and widenServerUse's stdout swap go.
func HumanizeBuiltins(root *cobra.Command) {
	if server := subcommand(root, "server"); server != nil {
		humanize(subcommand(server, "list"), printServerList)
		humanize(subcommand(server, "current"), printServerCurrent)
		humanize(subcommand(server, "set"), func(rec record) {
			fmt.Fprintf(bartolocli.Stdout, "Saved %s as the default server.%s\n", rec.str("server"), overrideNote())
		})
		humanize(subcommand(server, "clear"), func(record) {
			fmt.Fprintf(bartolocli.Stdout, "Cleared the saved server.%s\n", overrideNote())
		})
	}
	profile := subcommand(root, "auth", "profile")
	humanize(subcommand(profile, "list"), printProfileList)
	humanize(subcommand(profile, "current"), printProfileCurrent)
	humanize(subcommand(profile, "use"), func(rec record) {
		line := fmt.Sprintf("Now using profile %s", rec.str("active_profile"))
		if host := bartolocli.ProfileServer(); host != "" {
			line += " (" + host + ")"
		}
		fmt.Fprintln(bartolocli.Stdout, line+".")
	})
	humanize(subcommand(profile, "clear"), func(record) {
		if name := bartolocli.ActiveProfileName(); name != "" {
			fmt.Fprintf(bartolocli.Stdout, "Cleared the saved profile, but %q is still in force for this call (--profile or ORQ_PROFILE).\n", name)
			return
		}
		fmt.Fprintln(bartolocli.Stdout, "No profile active. Using your login session.")
	})
}

// subcommand walks names down from parent, returning nil at the first miss.
// A miss skips the wrap silently; the wiring test in package custom keeps a
// bartolo rename from turning the feature off unnoticed.
func subcommand(parent *cobra.Command, names ...string) *cobra.Command {
	for _, name := range names {
		if parent == nil {
			return nil
		}
		var next *cobra.Command
		for _, c := range parent.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		parent = next
	}
	return parent
}

// record is the generic value a bartolo command handed its formatter.
type record map[string]any

func (r record) str(key string) string {
	s, _ := r[key].(string)
	return s
}

func (r record) list(key string) []map[string]any {
	items, _ := r[key].([]map[string]any)
	if items != nil {
		return items
	}
	generic, _ := r[key].([]any)
	for _, g := range generic {
		if m, ok := g.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items
}

// recorder stands in for the formatter and keeps what the command emitted.
type recorder struct{ got any }

func (r *recorder) Format(v any) error { r.got = v; return nil }

// humanize runs the command as before unless a person is at a terminal, in
// which case it runs the original with its record captured and hands that to
// render. A failed command returns its error and renders nothing.
func humanize(cmd *cobra.Command, render func(record)) {
	if cmd == nil || cmd.RunE == nil {
		return
	}
	orig := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !wantsHumanView(c) {
			return orig(c, args)
		}
		rec := &recorder{}
		restore := bartolocli.Formatter
		bartolocli.Formatter = rec
		defer func() { bartolocli.Formatter = restore }()
		if err := orig(c, args); err != nil {
			return err
		}
		bartolocli.Formatter = restore
		m, _ := rec.got.(map[string]any)
		render(record(m))
		return nil
	}
}

func printServerList(rec record) {
	var rows []tableRow
	listed := false
	for _, s := range rec.list("servers") {
		marker := ""
		if s["selected"] == true {
			marker, listed = paint(ansiOK, "✓"), true
		}
		rows = append(rows, tableRow{marker: marker, cells: []string{fmt.Sprint(s["index"]), fmt.Sprint(s["url"]), fmt.Sprint(s["description"])}})
	}
	printTable(bartolocli.Stdout, []string{"#", "URL", "DESCRIPTION"}, rows)
	if !listed {
		fmt.Fprintf(bartolocli.Stdout, "\nCurrent server %s is not in this list (%s).\n", rec.str("selected_server"), auth.ServerSource().Describe())
	}
}

func printServerCurrent(rec record) {
	heading("Server")
	kv(7, "url", "%s", rec.str("server"))
	kv(7, "source", "%s", auth.ServerSource().Describe())
}

// overrideNote tells a person when a higher layer still decides this call's
// host. The host comes from auth, resolved before the write, because bartolo's
// own write clears the key a re-read would use.
func overrideNote() string {
	if src := auth.ServerSource(); src.Overrides() {
		return fmt.Sprintf(" %s still decides this call's host (%s).", src.Describe(), auth.Server())
	}
	return ""
}

func printProfileCurrent(rec record) {
	name := rec.str("active_profile")
	if name == "" {
		fmt.Fprintln(bartolocli.Stdout, "No profile active. Using your login session.")
		return
	}
	heading("Profile")
	kv(7, "name", "%s", name)
	kv(7, "source", "%s", profileSourceLabel(rec.str("source")))
	if host := rec.str("profile_server"); host != "" {
		kv(7, "server", "%s", host)
	}
	if rec["exists"] == false {
		fmt.Fprintf(bartolocli.Stdout, "\nNo profile named %q is configured. See `orq auth profile list`.\n", name)
	}
}

func profileSourceLabel(source string) string {
	switch source {
	case "flag":
		return "--profile flag"
	case "env":
		return "ORQ_PROFILE"
	case "selected":
		return "saved with `orq auth profile use`"
	default:
		return source
	}
}

func printProfileList(rec record) {
	profiles := rec.list("profiles")
	if len(profiles) == 0 {
		fmt.Fprintln(bartolocli.Stdout, "No profiles configured. Add one with `orq auth profile add <name> --api-key-file <file>`.")
		return
	}
	sort.SliceStable(profiles, func(i, j int) bool { return fmt.Sprint(profiles[i]["name"]) < fmt.Sprint(profiles[j]["name"]) })
	rows := make([]tableRow, 0, len(profiles))
	for _, p := range profiles {
		marker := ""
		if p["active"] == true {
			marker = paint(ansiOK, "✓")
		}
		rows = append(rows, tableRow{marker: marker, cells: []string{fmt.Sprint(p["name"]), orDash(p["server"]), orDash(p["api_key"])}})
	}
	printTable(bartolocli.Stdout, []string{"NAME", "SERVER", "KEY"}, rows)
}

func orDash(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "-"
}
