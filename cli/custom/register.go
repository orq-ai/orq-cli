package custom

import (
	"os"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/commands"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// apiKeyEnvVars mirrors the env vars bartolo's apikey handler looks up for the
// `Authorization` bearer flow (see apikey.InitBearer in the generated client).
var apiKeyEnvVars = []string{"ORQ_API_KEY", "ORQ_TOKEN", "ORQ_AUTHORIZATION"}

// Register wires custom commands and session-aware auth onto the provided root
// command. Must be called after generated.Register so that the
// bartolo `auth` parent command exists for our subcommands to attach onto.
func Register(root *cobra.Command) {
	if root == nil {
		root = bartolocli.Root
	}
	installSessionPreRun()
	registerCommands(root)
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
		session, err := auth.ReadSession()
		if err != nil || session == nil {
			return nil
		}
		if viper.GetString("server") == "" && session.APIBaseURL != "" {
			viper.Set("server", session.APIBaseURL)
		}
		if !apiKeyConfigured() {
			if token := activeWorkspaceToken(); token != "" {
				os.Setenv("ORQ_API_KEY", token)
			}
		}
		return nil
	}
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
