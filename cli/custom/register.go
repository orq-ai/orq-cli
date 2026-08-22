package custom

import (
	"context"
	"fmt"
	"os"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/commands"
	"orq/cli/custom/skills"

	colorable "github.com/mattn/go-colorable"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// apiKeyEnvVars mirrors the env vars bartolo's apikey handler looks up for the
// `Authorization` bearer flow (see apikey.InitBearer in the generated client).
var apiKeyEnvVars = []string{"ORQ_API_KEY", "ORQ_TOKEN", "ORQ_AUTHORIZATION"}

// helpFooter is appended to the root help output (clig.dev: help should say
// where the docs live and where to report problems).
const helpFooter = "Docs:   https://docs.orq.ai\nIssues: https://github.com/orq-ai/orq-cli/issues"

// profileExemptCommands are commands that must work with a profile that has no
// session yet (creating one, or diagnosing why it is missing), so the
// unknown-profile guard skips them.
var profileExemptCommands = map[string]bool{
	"login":         true,
	"logout":        true,
	"setup":         true,
	"add-profile":   true,
	"list-profiles": true, // listing profiles is how you diagnose an unknown one
	"doctor":        true,
	"help":          true,
	"completion":    true,
	"man-pages":     true,
}

// interactiveWizardCommands are bartolo-owned commands whose prompts run
// through bartolo's own TTY check, which knows nothing about --no-input.
// Refusing them up front keeps the "--no-input never prompts" promise honest.
//
// Keyed by command PATH, not name: orq's own `setup` is a different command
// from bartolo's `auth setup`, honors --no-input itself, and is meant to run
// headless in CI. Matching on the bare name refused it.
var interactiveWizardCommands = map[string]bool{
	"auth setup":       true,
	"auth add-profile": true,
}

// commandPath is the command's path with the root binary name removed, so the
// maps above read as the user types them ("auth setup", not "orq auth setup").
func commandPath(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

// Register wires custom commands and session-aware auth onto the provided root
// command. Must be called after generated.Register so that the
// bartolo `auth` parent command exists for our subcommands to attach onto.
func Register(root *cobra.Command) {
	if root == nil {
		root = bartolocli.Root
	}
	// Bartolo routes everything to stdout; errors belong on stderr so
	// `orq ... | jq` never gets Go error text piped into it (clig.dev).
	root.SetErr(bartolocli.Stderr)
	// On error, print the error and point at --help instead of dumping the
	// full usage block after every runtime failure.
	root.SilenceUsage = true
	registerGlobalFlags()
	installSessionPreRun()
	registerCommands(root)
	// Help presentation: runs last so it sees the complete tree.
	applyCommandGroups(root)
	annotateGlobalFlagEnvVars(root)
	appendHelpFooter(root)
}

func registerGlobalFlags() {
	// AddGlobalFlag binds through viper, and bartolo's env replacer maps
	// `-` to `_`, so these also honor ORQ_NO_INPUT / ORQ_NO_COLOR / ORQ_WORKSPACE.
	bartolocli.AddGlobalFlag("no-input", "", "Never prompt; fail instead of asking questions", false)
	bartolocli.AddGlobalFlag("no-color", "", "Disable colored output (NO_COLOR is also honored)", false)
	bartolocli.AddGlobalFlag("workspace", "", "Workspace key to use for this invocation (overrides the session's active workspace)", "")
}

// annotateGlobalFlagEnvVars only labels the ORQ_* binding registerGlobalFlags already describes; nothing is bound here.
func annotateGlobalFlagEnvVars(root *cobra.Command) {
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		f.Usage += fmt.Sprintf(" [env: ORQ_%s]", strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_")))
	})
}

func appendHelpFooter(root *cobra.Command) {
	// Extend the help TEMPLATE, not root.Long: Long renders above `Usage:`,
	// which turns the footer into a header. clig.dev wants Docs/Issues as a
	// trailer after the flag list.
	root.SetHelpTemplate(root.HelpTemplate() + "\n" + helpFooter + "\n")
}

// installSessionPreRun runs once per command invocation, after cobra parses
// flags and before the command handler fires. When the active profile's
// session has an apiBaseUrl set and the user did NOT pass --server explicitly,
// we point bartolo's generated commands at the same host the session was
// authenticated against. This keeps "login against local → query against
// local" working without a separate --server flag on every call.
//
// It also bridges the session into bartolo's API-key auth: bartolo's apikey
// handler aborts the request with "missing API key" before our request
// middleware runs, so a logged-in user with no explicit key would never
// authenticate. When no key is configured, we feed the active workspace token
// via ORQ_API_KEY (bartolo's InitBearer adds the "Bearer " prefix) so generated
// commands authenticate as the session user.
func installSessionPreRun() {
	prev := bartolocli.PreRun
	bartolocli.PreRun = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		repairAuthProfileType()
		applyNoColor()
		// Snapshot whether the USER configured an API key before this PreRun
		// injects the session token into ORQ_API_KEY below - commands that
		// read the env afterwards would see our own injection and cry wolf
		// on every invocation.
		commands.SetExplicitAPIKey(apiKeyConfigured())
		commands.SetUserEnvAPIKey(os.Getenv("ORQ_API_KEY"))
		if viper.GetBool("no-input") && interactiveWizardCommands[commandPath(cmd)] {
			return fmt.Errorf(
				"`%s` is an interactive wizard and --no-input/ORQ_NO_INPUT is set; "+
					"use `orq auth login` or set ORQ_API_KEY instead",
				commandPath(cmd),
			)
		}
		if err := rejectUnknownProfile(cmd); err != nil {
			return err
		}
		override := strings.TrimSpace(viper.GetString("workspace"))
		// Warn about a shadowed --workspace before anything else, so the no-op
		// is surfaced even when there is no session at all (API-key-only use).
		if override != "" && apiKeyConfigured() {
			commands.Warn("--workspace has no effect because an explicit API key (ORQ_API_KEY or a credentials profile) is configured and takes precedence")
		}
		session, err := auth.ReadSession()
		if err != nil || session == nil {
			return nil
		}
		if viper.GetString("server") == "" && session.APIBaseURL != "" {
			viper.Set("server", session.APIBaseURL)
		}
		if apiKeyConfigured() {
			return nil
		}
		if override != "" {
			client := auth.NewClient(session.APIBaseURL).WithContext(cmd.Context())
			token, err := client.WorkspaceToken(session, override)
			if err != nil {
				return fmt.Errorf("workspace %q: %w", override, err)
			}
			os.Setenv("ORQ_API_KEY", token)
			return nil
		}
		if token := activeWorkspaceToken(cmd.Context()); token != "" {
			os.Setenv("ORQ_API_KEY", token)
		}
		return nil
	}
}

// applyNoColor is the orq-cli-side stopgap for --no-color and the NO_COLOR
// convention (https://no-color.org). Bartolo decides color support in Init(),
// before flags are parsed, so a flag cannot influence that decision upstream;
// here we swap the writers for ANSI-stripping ones and rebuild the formatter
// without a TTY. The root-cause fix (honoring NO_COLOR/TERM in bartolo's
// color gate) is tracked in the sibling bartolo ticket.
func applyNoColor() {
	if !viper.GetBool("no-color") && os.Getenv("NO_COLOR") == "" {
		return
	}
	bartolocli.Stdout = colorable.NewNonColorable(os.Stdout)
	bartolocli.Stderr = colorable.NewNonColorable(os.Stderr)
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
}

// rejectUnknownProfile errors when the user explicitly selected a profile that
// has neither a session file nor a credentials entry. Without this the CLI
// silently falls through to ORQ_API_KEY and returns real data from the wrong
// context — the worst kind of success. Commands that create or diagnose
// profiles are exempt.
func rejectUnknownProfile(cmd *cobra.Command) error {
	if profileExemptCommands[cmd.Name()] {
		return nil
	}
	explicit := os.Getenv("ORQ_PROFILE") != ""
	// Only the ROOT persistent flag selects a credentials profile. A generated
	// command may define a LOCAL --profile request field (e.g. `models
	// create-autorouter --profile balanced`) that shadows the global flag in
	// cmd.Flags(); that is request data, not a credentials selection.
	if f := cmd.Root().PersistentFlags().Lookup("profile"); f != nil && f.Changed {
		explicit = true
	}
	if !explicit {
		return nil
	}
	if auth.InspectSession().Status != auth.StatusMissing {
		return nil
	}
	// An explicit API key (env var or credentials entry) is a complete,
	// working credential config - the standard CI shape is ORQ_API_KEY with
	// no session file, and blocking it would reject legitimate calls.
	if apiKeyConfigured() {
		return nil
	}
	profile := auth.ActiveProfile()
	return fmt.Errorf(
		"unknown profile %q: no session at %s and no credentials entry; run `orq auth login --profile %s` first",
		profile, auth.SessionFilePath(), profile,
	)
}

// repairAuthProfileType rewrites, in memory only, a stored profile whose "type"
// no auth handler answers to.
//
// Builds before this fix wrote type "apikey" while the generated client
// registers its handler anonymously, so bartolo resolved no handler and every
// generated command aborted with "no authentication handler configured".
// Without this, those users stay broken until they happen to re-run orq setup.
//
// Only the in-memory value is corrected: rewriting credentials.json from a
// PreRun would mean every command silently mutating the user's credential file.
func repairAuthProfileType() {
	profile := auth.ActiveProfile()
	if strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) == "" {
		return
	}
	stored := bartolocli.Creds.GetString("profiles." + profile + ".type")
	if _, ok := bartolocli.AuthHandlers[stored]; ok {
		return
	}
	bartolocli.Creds.Set("profiles."+profile+".type", commands.BartoloAuthType())
}

// apiKeyConfigured reports whether bartolo would already find an API key from
// the environment or the active credentials profile. When true we leave auth
// untouched so an explicit key always wins over the session token.
func apiKeyConfigured() bool {
	for _, envVar := range apiKeyEnvVars {
		if strings.TrimSpace(os.Getenv(envVar)) != "" {
			return true
		}
	}
	return strings.TrimSpace(bartolocli.GetProfile()["api_key"]) != ""
}

func activeWorkspaceToken(ctx context.Context) string {
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		return ""
	}
	client := auth.NewClient(session.APIBaseURL).WithContext(ctx)
	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil {
		return ""
	}
	return active.AccessToken
}

func registerCommands(root *cobra.Command) {
	replaceDoctor(root)
	attachAuthSubcommands(root)
	addHiddenAuthAliases(root)
	root.AddCommand(commands.NewWorkspaceCommand())
	root.AddCommand(commands.NewManPagesCommand())
	root.AddCommand(commands.NewLaunchCommand())
	root.AddCommand(commands.NewSetupCommand())
	root.AddCommand(commands.NewConnectCommand())
	root.AddCommand(commands.NewDisconnectCommand())
	installSkillsRefreshPreRun()
}

// installSkillsRefreshPreRun keeps installed skills current with the running
// binary, and reclaims links left behind by launches that died without
// cleaning up after themselves, on every `orq` invocation — not just
// `orq connect`, so someone who updates the CLI and then opens their agent
// directly is not left on the old set.
//
// root has no PersistentPreRun of its own to chain onto: bartolo's Init sets
// root.PersistentPreRunE to a function that, after its own housekeeping,
// calls the single package-level bartolocli.PreRun hook if one is set. That
// is the same seam installSessionPreRun above already uses, so this chains
// onto it the same way rather than assigning root.PersistentPreRun directly
// — which cobra would never even look at, since PersistentPreRunE (bartolo's)
// takes priority over PersistentPreRun on the same command.
//
// Both calls only ever touch what the manifest already records: a machine
// that never ran `orq connect` has no manifest, and Refresh and
// SweepDeadSessions both return before touching the filesystem in that case.
// Neither call may fail a command — an update or a dead-session sweep that
// cannot proceed (most likely because a concurrent `orq launch` or `orq
// connect` holds the manifest lock) is worth a warning, not a broken
// `orq --help`. The lock wait itself is capped (see lock.go's lockTimeout),
// so a contended manifest costs this at most a couple of seconds, not a hang.
func installSkillsRefreshPreRun() {
	prev := bartolocli.PreRun
	bartolocli.PreRun = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		if res, err := skills.Refresh(); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Warning: could not refresh orq skills: %v\n", err)
		} else if len(res.Added) > 0 || len(res.Removed) > 0 {
			fmt.Fprintf(bartolocli.Stderr, "orq skills updated to match this CLI version (%d installed, %d removed)\n",
				len(res.Added), len(res.Removed))
		}
		if err := skills.SweepDeadSessions(); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Warning: could not clean up stale session skills: %v\n", err)
		}
		return nil
	}
}

func replaceDoctor(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == "doctor" {
			root.RemoveCommand(c)
			break
		}
	}
	root.AddCommand(commands.NewDoctorCommand())
}

func attachAuthSubcommands(root *cobra.Command) {
	var authParent *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "auth" {
			authParent = c
			break
		}
	}
	if authParent == nil {
		authParent = &cobra.Command{
			Use:   "auth",
			Short: "Authentication settings",
		}
		root.AddCommand(authParent)
	}
	// Bartolo's `auth setup` command ships with a `login` alias for the
	// API-key wizard. Strip it so our OAuth `auth login` subcommand is the
	// one cobra resolves.
	for _, c := range authParent.Commands() {
		if c.Name() == "setup" {
			c.Aliases = removeString(c.Aliases, "login")
		}
	}
	authParent.AddCommand(commands.NewLoginCommand())
	authParent.AddCommand(commands.NewLogoutCommand())
	authParent.AddCommand(commands.NewWhoAmICommand())
}

func removeString(slice []string, target string) []string {
	out := slice[:0]
	for _, s := range slice {
		if s != target {
			out = append(out, s)
		}
	}
	return out
}

func addHiddenAuthAliases(root *cobra.Command) {
	for _, factory := range []func() *cobra.Command{
		commands.NewLoginCommand,
		commands.NewLogoutCommand,
		commands.NewWhoAmICommand,
	} {
		alias := factory()
		alias.Hidden = true
		root.AddCommand(alias)
	}
}
