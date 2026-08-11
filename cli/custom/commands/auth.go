package commands

import (
	"errors"
	"fmt"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func NewLoginCommand() *cobra.Command {
	var apiBase string
	var workspace string
	var noOpen bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with orq via OAuth device login",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := auth.NewClient(apiBase).WithContext(cmd.Context())

			start, err := client.StartDeviceLogin("orq-cli")
			if err != nil {
				return err
			}

			fmt.Fprintf(bartolocli.Stderr, "Open: %s\n", start.VerificationURIComplete)
			fmt.Fprintf(bartolocli.Stderr, "Code: %s\n", start.UserCode)

			browserOpened := false
			if !noOpen {
				browserOpened = auth.OpenBrowser(start.VerificationURIComplete)
				if !browserOpened {
					fmt.Fprintln(bartolocli.Stderr, "Could not open the browser automatically. Open the URL manually.")
				}
			}

			fmt.Fprintln(bartolocli.Stderr, "Waiting for browser approval...")
			approved, err := client.AwaitDeviceApproval(cmd.Context(), start.DeviceCode, start.ExpiresIn, start.Interval)
			if err != nil {
				return err
			}

			profile, err := client.FetchProfile(approved.AccessToken)
			if err != nil {
				return err
			}

			workspaceKey := workspace
			if workspaceKey == "" && len(profile.Workspaces) > 1 && hasInteractiveTTY() {
				workspaceKey, err = selectWorkspace(profile.Workspaces, "Choose an active workspace")
				if err != nil {
					return err
				}
			}

			session, err := client.CreateSessionFromDeviceApproval(approved, profile, workspaceKey)
			if err != nil {
				return err
			}

			report := BuildIdentityReport(session, &client.URLs)
			if wantsHumanView(cmd) {
				printIdentity(report, "Signed in as")
				return nil
			}
			return emit(map[string]any{
				"identity":         report,
				"browser_opened":   browserOpened,
				"verification_uri": start.VerificationURIComplete,
				"user_code":        start.UserCode,
			})
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Preselect a workspace key")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not try to open the browser automatically")
	return cmd
}

func NewLogoutCommand() *cobra.Command {
	var apiBase string
	var yes bool
	var force bool

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
				warnLingeringAPIKeys()
				if wantsHumanView(cmd) {
					if keyCleared {
						success("Cleared the stored API key")
					} else {
						info("Not logged in - nothing to clear.")
					}
					return nil
				}
				return emit(map[string]any{
					"authenticated":           false,
					"cleared":                 keyCleared,
					"api_key_profile_cleared": keyCleared,
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
				return nil
			}
			return emit(map[string]any{
				"authenticated":           false,
				"cleared":                 true,
				"revoked":                 revokeErr == nil,
				"api_key_profile_cleared": keyCleared,
				"session_file":            auth.SessionFilePath(),
			})
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&force, "force", false, "Clear local credentials even if the server-side token revoke fails (implies --yes)")
	return cmd
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
