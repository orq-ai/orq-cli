package custom

import (
	"fmt"
	"os"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/commands"

	colorable "github.com/mattn/go-colorable"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
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
	"login":       true,
	"logout":      true,
	"setup":       true,
	"add-profile": true,
	"doctor":      true,
	"help":        true,
	"completion":  true,
	"man-pages":   true,
}

// Register wires custom commands and session-aware auth onto the provided root
// command. Must be called after generated.Register so that the
// bartolo `auth` parent command exists for our subcommands to attach onto.
func Register(root *cobra.Command) {
	if root == nil {
		root = bartolocli.Root
	}
	registerGlobalFlags()
	installSessionPreRun()
	registerCommands(root)
	appendHelpFooter(root)
}

func registerGlobalFlags() {
	// AddGlobalFlag binds through viper, and bartolo's env replacer maps
	// `-` to `_`, so these also honor ORQ_NO_INPUT / ORQ_NO_COLOR / ORQ_WORKSPACE.
	bartolocli.AddGlobalFlag("no-input", "", "Never prompt; fail instead of asking questions", false)
	bartolocli.AddGlobalFlag("no-color", "", "Disable colored output (NO_COLOR is also honored)", false)
	bartolocli.AddGlobalFlag("workspace", "", "Workspace key to use for this invocation (overrides the session's active workspace)", "")
}

func appendHelpFooter(root *cobra.Command) {
	if root.Long == "" {
		root.Long = root.Short
	}
	root.Long = strings.TrimRight(root.Long, "\n") + "\n\n" + helpFooter
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
		applyNoColor()
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
			client := auth.NewClient(session.APIBaseURL)
			token, err := client.WorkspaceToken(session, override)
			if err != nil {
				return fmt.Errorf("workspace %q: %w", override, err)
			}
			os.Setenv("ORQ_API_KEY", token)
			return nil
		}
		if token := activeWorkspaceToken(); token != "" {
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
	if f := cmd.Flags().Lookup("profile"); f != nil && f.Changed {
		explicit = true
	}
	if !explicit {
		return nil
	}
	if auth.InspectSession().Status != auth.StatusMissing {
		return nil
	}
	if strings.TrimSpace(bartolocli.GetProfile()["api_key"]) != "" {
		return nil
	}
	profile := auth.ActiveProfile()
	return fmt.Errorf(
		"unknown profile %q: no session at %s and no credentials entry; run `orq auth login --profile %s` first",
		profile, auth.SessionFilePath(), profile,
	)
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

func activeWorkspaceToken() string {
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		return ""
	}
	client := auth.NewClient(session.APIBaseURL)
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
