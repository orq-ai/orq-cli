package commands

import (
	"errors"
	"fmt"

	"strings"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
)

func NewLoginCommand() *cobra.Command {
	var apiBase string
	var workspace string
	var noOpen bool
	var apiKey string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with orq via OAuth device login or an API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Choose the method: an explicit --api-key skips the question, and
			// without a TTY there is nobody to ask, so browser login proceeds
			// directly (it will fail with its own clear error headless).
			method := "OAuth (browser)"
			if strings.TrimSpace(apiKey) != "" {
				method = "API key"
			} else if hasInteractiveTTY() {
				if err := survey.AskOne(&survey.Select{
					Message: "Select login method",
					Options: []string{"OAuth (browser)", "API key"},
				}, &method, promptStdio()); err != nil {
					return err
				}
			}

			if method == "API key" {
				return apiKeyLogin(cmd, apiBase, apiKey)
			}

			result, err := runDeviceLogin(cmd.Context(), newReporter(false), apiBase, workspace, !noOpen)
			if err != nil {
				return err
			}

			report := BuildIdentityReport(result.Session, &auth.NewClient(apiBase).URLs)
			if wantsHumanView(cmd) {
				printIdentity(report, "Signed in as")
				return nil
			}
			return emit(map[string]any{
				"identity":         report,
				"browser_opened":   result.BrowserOpened,
				"verification_uri": result.VerificationURI,
				"user_code":        result.UserCode,
			})
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Preselect a workspace key")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not try to open the browser automatically")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Sign in with this API key instead of the browser")
	return cmd
}

// apiKeyLogin verifies a pasted or flag-supplied key with one real API call,
// then persists it to the credentials profile — the same store `orq setup
// --api-key` writes, so every command resolves it afterwards.
func apiKeyLogin(cmd *cobra.Command, apiBase, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		if err := survey.AskOne(&survey.Password{
			Message: "API key",
		}, &key, survey.WithValidator(survey.Required), promptStdio()); err != nil {
			return err
		}
		key = strings.TrimSpace(key)
	}

	// Verify before saving: persisting a bad key would leave every later
	// command failing with a 401 the user has to trace back here.
	client := auth.NewClient(apiBase).WithContext(cmd.Context())
	projects, err := client.ListProjects(key)
	if err != nil {
		return fmt.Errorf("the key was not accepted by %s: %w", client.URLs.APIBaseURL, err)
	}
	// A user-supplied key carries no workspace provenance — saved as unknown,
	// so setup's reuse check treats it as such rather than as a mismatch.
	if err := saveAPIKeyProfile(key, ""); err != nil {
		return err
	}

	if wantsHumanView(cmd) {
		success("Signed in with an API key (profile: %s, %d projects visible)", auth.ActiveProfile(), len(projects))
		return nil
	}
	return emit(map[string]any{
		"method":   "api_key",
		"profile":  auth.ActiveProfile(),
		"verified": true,
	})
}

func NewLogoutCommand() *cobra.Command {
	var apiBase string
	var yes bool
	var force bool
	var disconnect bool

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Revoke the refresh token and clear local credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			// A stored API key authenticates on its own, so logging out has to
			// clear it too — otherwise "logged out" still runs every command.
			if session == nil {
				keyCleared, err := clearAPIKeyProfile()
				if err != nil {
					return err
				}
				envCleared := clearShellEnvFile()
				removed := disconnectOnLogout(&setupOptions{noInput: !hasInteractiveTTY(), yes: yes || force}, disconnect)
				warnLingeringAPIKeys()
				if wantsHumanView(cmd) {
					if keyCleared {
						success("Cleared the stored API key")
					} else {
						info("Not logged in - nothing to clear.")
					}
					reportClearedEnvFiles(envCleared)
					reportSurvivingGatewayKey()
					return nil
				}
				return emit(map[string]any{
					"authenticated":           false,
					"cleared":                 keyCleared,
					"api_key_profile_cleared": keyCleared,
					"env_files_cleared":       envCleared,
					"coding_agents_removed":   removed,
					"gateway_key_id":          savedGatewayKeyID(),
					"session_file":            auth.SessionFilePath(),
				})
			}

			// --force clears local credentials no matter what, so it implies
			// consent; asking "are you sure?" after the user said "force" is noise.
			confirmed := yes || force
			if !confirmed {
				if !hasInteractiveTTY() {
					return errors.New("refusing to log out without confirmation in non-interactive mode; pass --yes")
				}
				userLabel := "current user"
				if session.User != nil && session.User.Email != "" {
					userLabel = session.User.Email
				}
				confirm := false
				if err := survey.AskOne(&survey.Confirm{
					Message: fmt.Sprintf("Sign out %s?", userLabel),
					Default: true,
				}, &confirm, promptStdio()); err != nil {
					return err
				}
				if !confirm {
					// Declining a confirmation is a choice, not a failure:
					// exit 0. In machine mode emit an explicit cancelled payload
					// so a script can tell "user said no" from "logged out" —
					// both would otherwise be exit 0 with empty stdout.
					if wantsHumanView(cmd) {
						info("Logout cancelled.")
						return nil
					}
					return emit(map[string]any{
						"authenticated": true,
						"cleared":       false,
						"cancelled":     true,
						"session_file":  auth.SessionFilePath(),
					})
				}
			}

			client := auth.NewClient(sessionAPIBase(apiBase, session)).WithContext(cmd.Context())
			revokeErr := client.Logout(session.RefreshToken)
			if revokeErr != nil && !force {
				// Deleting local credentials while the refresh token is still
				// valid server-side would orphan the session. Keep it so the
				// user can retry, unless they explicitly force the clear.
				return fmt.Errorf(
					"token revoke failed, local session kept (retry, or pass --force to clear local credentials anyway): %w",
					revokeErr,
				)
			}
			if err := client.ClearLocalSession(); err != nil {
				return err
			}
			keyCleared, err := clearAPIKeyProfile()
			if err != nil {
				return err
			}
			envCleared := clearShellEnvFile()
			removed := disconnectOnLogout(&setupOptions{noInput: !hasInteractiveTTY(), yes: yes || force}, disconnect)
			warnLingeringAPIKeys()

			// Same human/machine split as login and whoami: the human view
			// returns early so a terminal never sees the structured payload,
			// and --json/-o never sees the check line. A kept-but-unrevoked
			// token is a warning, not a green success.
			if wantsHumanView(cmd) {
				if revokeErr == nil {
					success("Signed out")
				} else {
					Warn("local credentials cleared, but the server-side token was not revoked")
				}
				reportClearedEnvFiles(envCleared)
				reportSurvivingGatewayKey()
				return nil
			}
			return emit(map[string]any{
				"authenticated":           false,
				"cleared":                 true,
				"revoked":                 revokeErr == nil,
				"api_key_profile_cleared": keyCleared,
				"env_files_cleared":       envCleared,
				"coding_agents_removed":   removed,
				"gateway_key_id":          savedGatewayKeyID(),
				"session_file":            auth.SessionFilePath(),
			})
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&force, "force", false, "Clear local credentials even if the server-side token revoke fails (implies --yes)")
	cmd.Flags().BoolVar(&disconnect, "disconnect", false, "Also remove orq from this machine's coding agents, without asking")
	return cmd
}

// reportSurvivingGatewayKey names the one thing logout cannot undo. The key is
// still Active in the workspace until its own expiry, and the id is the only
// handle for killing it, so saying nothing here strands a live credential.
func reportSurvivingGatewayKey() {
	if id := savedGatewayKeyID(); id != "" {
		info("the gateway key is still active — revoke it with: orq api-keys delete %s", id)
	}
}

func NewWhoAmICommand() *cobra.Command {
	var apiBase string

	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the current authenticated user and workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			client := auth.NewClient(sessionAPIBase(apiBase, session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			report := BuildIdentityReport(session, &client.URLs)
			if wantsHumanView(cmd) {
				printIdentity(report, "Signed in as")
				return nil
			}
			return emit(report)
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	return cmd
}

// printIdentity renders the friendly "who am I" block: a green headline plus an
// aligned key/value list. The structured report is reserved for scripts and
// --json/-o, so this is the primary output at a terminal.
func printIdentity(report IdentityReport, verb string) {
	email := "current user"
	name := ""
	if report.User != nil {
		if report.User.Email != "" {
			email = report.User.Email
		}
		name = report.User.DisplayName
	}
	success("%s %s", verb, email)

	activeName := ""
	if report.ActiveWorkspaceKey != nil {
		for _, w := range report.Workspaces {
			if w.Key == *report.ActiveWorkspaceKey {
				activeName = w.Name
				break
			}
		}
	}
	const w = 9
	if name != "" {
		kv(w, "name", "%s", name)
	}
	if activeName != "" && report.ActiveWorkspaceKey != nil {
		kv(w, "workspace", "%s (%s)", activeName, *report.ActiveWorkspaceKey)
	}
	if len(report.Workspaces) > 1 {
		kv(w, "access", "%d workspaces", len(report.Workspaces))
	}
	kv(w, "session", "%s", report.SessionFile)
}

// reportClearedEnvFiles names the files logout emptied. A credential leaving
// the machine is a state change the user has to be able to model: without this
// line, "Signed out" is printed while the shell profile still sources a file
// that exported a live key a moment ago.
func reportClearedEnvFiles(paths []string) {
	for _, path := range paths {
		info("Removed the exported key from %s", tilde(path))
	}
}
