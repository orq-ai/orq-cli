package custom

import (
	"context"
	"errors"
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
	"update":        true, // updating must work without a session; it touches no orq API
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
		resolveServer(cmd)
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
		// The session's host is the last resort, below every explicit source.
		// TODO(orq-ai/bartolo#22): once a profile can carry its own server and
		// the regenerated clients read it (a per-profile resolver, proposed in
		// that PR and absent from the pinned bartolo), this bridge and
		// mirrorServerToViper both go away — the profile becomes the one store.
		if auth.Server() == "" && session.APIBaseURL != "" {
			auth.SetServer(session.APIBaseURL, "session")
			mirrorServerToViper()
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

// resolveServer decides the one host this invocation talks to, and records
// where it came from. Every layer used to read its own env var — auth read
// ORQ_API_BASE_URL, the generated commands read viper's `server`, launch read
// the env directly — so the same run could reach two hosts. One decision here,
// mirrored into viper for the generated commands and into auth for everything
// else, is what makes --server mean the same thing everywhere.
//
// The session's own host is layered on afterwards by the caller: it loses to
// every explicit source, so it cannot be decided until they are ruled out.
func resolveServer(cmd *cobra.Command) {
	envServer, envVar := auth.ServerFromEnv(os.Getenv)
	switch {
	case cmd.Root().PersistentFlags().Changed("server"):
		// Read the flag, not viper's key: an explicitly typed --server has to
		// win over anything else that lands on the same key.
		auth.SetServer(cmd.Root().PersistentFlags().Lookup("server").Value.String(), "flag")
	case cmd.Flags().Changed("api-base-url"):
		// The pre-4.15 flag on the six auth/workspace/doctor commands, kept for
		// one release (commands.DeprecatedAPIBaseFlag).
		// Lookup, not GetString: the latter returns an error this branch would
		// have to swallow, and swallowing it would drop a host the user typed.
		commands.Warn("--api-base-url is deprecated and will be removed in a future release; use --server instead")
		auth.SetServer(cmd.Flags().Lookup("api-base-url").Value.String(), "flag")
	case envServer != "":
		if envVar == auth.DeprecatedServerEnvVar {
			commands.Warn("ORQ_API_BASE_URL is deprecated and will be removed in a future release; use ORQ_SERVER (or --server) instead")
		}
		auth.SetServer(envServer, "env")
	case viper.GetString("server") != "":
		auth.SetServer(viper.GetString("server"), "config") // persisted `orq server set`
	default:
		// Assign in every branch: the resolver decides the host, it does not
		// leave behind whatever a previous call happened to store.
		auth.SetServer("", "default")
	}
	mirrorServerToViper()
}

// mirrorServerToViper hands the resolved host to the generated commands, which
// read viper's `server` key directly (bartolo cli.ResolveServer). They cannot
// see the session host, --api-base-url or ORQ_API_BASE_URL; the mirror is the
// only way those sources reach them. (The plain defaults now agree — the
// OpenAPI server list and auth.DefaultAPIBaseURL are both my.orq.ai.)
func mirrorServerToViper() {
	if s := auth.Server(); s != "" && viper.GetString("server") != s {
		viper.Set("server", s)
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
	root.AddCommand(commands.NewUpdateCommand())
	installSkillsRefreshPreRun()
}

// installSkillsRefreshPreRun keeps installed skills current with the running
// binary, and reclaims links left behind by launches that died without
// cleaning up after themselves.
//
// The two halves are scoped differently, because they cost and risk
// different things.
//
// Refresh is scoped to the commands that actually touch skills (see
// skillsCommand). It used to run on every `orq` invocation, on the reasoning
// that someone who updates the CLI and then opens their agent directly should
// not be left on the old set. That put a lock acquisition and a directory
// walk in front of `orq --help`, and made every bug in this path — a wedged
// lock, a bad prune — reachable from a command that has nothing to do with
// skills. The convergence it bought back is now doctor's job: it reports a
// stale or incomplete install and names the one command that fixes it.
//
// The sweep runs everywhere. It is neither expensive nor destructive: it
// reads the manifest, checks whether any recorded session PID is gone, and
// returns before taking the lock when none is. Scoping it too was collateral
// damage — links leaked by a killed `orq launch` were left for four commands
// to collect, and doctor excludes session links by design, so nothing
// reported them in the meantime.
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
// and a failed Refresh skips the sweep rather than waiting out that same
// timeout twice, so a contended manifest costs this at most one lockTimeout
// (currently 2s), not a hang and not a doubled wait.
func installSkillsRefreshPreRun() {
	prev := bartolocli.PreRun
	bartolocli.PreRun = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		// The sweep runs everywhere, the refresh does not. A dead PID in the
		// manifest is an unambiguous fact and collecting it is one file read
		// plus one liveness check — SweepDeadSessions returns before locking
		// when nothing is dead. Scoping it to the skills commands left links
		// from a killed `orq launch` converged by four commands and reported
		// by none, since doctor excludes session links by design. Refresh is
		// the expensive, destructive half, and it stays behind the gate.
		if err := skills.SweepDeadSessions(); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Warning: could not clean up stale session skills: %v\n", err)
			// The sweep and Refresh each wait out the same lock (lockTimeout
			// in lock.go) before giving up, and a lock held a moment ago is
			// very likely held now: running the refresh anyway would make an
			// already-contended command wait out that timeout a second time
			// for almost no chance of success. Returning here caps the worst
			// case at one timeout instead of two.
			if errors.Is(err, skills.ErrManifestLocked) {
				return nil
			}
		}
		if !skillsCommand(cmd) {
			return nil
		}
		res, err := skills.Refresh()
		if err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Warning: could not refresh orq skills: %v\n", err)
		} else {
			if len(res.Added) > 0 || len(res.Removed) > 0 {
				fmt.Fprintf(bartolocli.Stderr, "orq skills updated to match this CLI version (%d installed, %d removed)\n",
					len(res.Added), len(res.Removed))
			}
			// One line naming the remedy, not one raw Go error per broken
			// link. Refresh leaves the fingerprint stale while any link is
			// failing, so this repeats on every skills command until the user
			// repairs the directory — which is the point: a warning printed
			// once, about a link that stays broken, is a warning the user
			// will miss.
			if len(res.Failed) > 0 {
				fmt.Fprintf(bartolocli.Stderr, "Warning: %d orq skill link(s) could not be updated — run 'orq connect skills' to repair them\n",
					len(res.Failed))
			}
			// A path we stopped tracking without deleting is the one case the
			// user cannot discover afterwards: the record is gone, so
			// `orq disconnect skills` will never mention it either. Said once,
			// here, naming the paths, or not at all.
			if len(res.Disowned) > 0 {
				fmt.Fprintf(bartolocli.Stderr, "orq no longer manages %d path(s) in your skills directory and left them in place — remove them by hand if you no longer want them: %s\n",
					len(res.Disowned), strings.Join(res.Disowned, ", "))
			}
			// A path we skipped but still record is inert: refresh will refuse
			// to touch it on every future update, so the user's skills quietly
			// stop tracking this CLI and nothing says so. Disowned paths are
			// named above and are not repeated here.
			if skipped := stillRecorded(res); len(skipped) > 0 {
				fmt.Fprintf(bartolocli.Stderr, "Warning: orq did not update %d path(s) in your skills directory because something else now occupies them — run 'orq doctor' for the list\n",
					len(skipped))
			}
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

// stillRecorded returns the skipped paths whose manifest record survived, so
// the pre-run never names a path twice: Disowned is the subset that was
// skipped and dropped, and it gets its own, more final, message.
func stillRecorded(res *skills.Result) []string {
	dropped := map[string]bool{}
	for _, p := range res.Disowned {
		dropped[p] = true
	}
	var out []string
	for _, p := range res.Skipped {
		if !dropped[p] {
			out = append(out, p)
		}
	}
	return out
}

// skillsCommand reports whether cmd is one whose job involves the skills on
// this machine, and therefore one that should converge them first.
//
// Matched on the root-level command, so subcommands and aliases ride along
// with their parent: `orq connect skills` and `orq launch claude` are both
// the same answer as their root.
func skillsCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	top := cmd
	for top.Parent() != nil && top.Parent().Parent() != nil {
		top = top.Parent()
	}
	switch top.Name() {
	// launch installs session links; connect and disconnect are the whole
	// point; setup installs skills itself (see instrumentAgents in
	// setup.go). doctor is deliberately absent: it reports
	// what is on disk, and a diagnostic that repairs the thing it is
	// diagnosing can never show you the state you called it about.
	case "launch", "connect", "disconnect", "setup":
		return true
	}
	return false
}
