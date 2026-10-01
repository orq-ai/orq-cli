package commands

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// profileInForceError is what login and logout say when a profile is selected:
// the session they act on is not what a profile selects. The message names
// where the selection came from, because it is as often ORQ_PROFILE or a
// persisted `auth profile use` as it is the flag.
func profileInForceError(verb string) error {
	name, source, drop := ProfileSelection()
	return fmt.Errorf(
		"profile %q is an API key, not a login (selected by %s); to %s, %s, or pass --profile \"\" for this call",
		name, source, verb, drop,
	)
}

func NewLoginCommand() *cobra.Command {
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

			if method != "API key" && profileSelected() {
				return profileInForceError("log in to another server with --server")
			}

			if method == "API key" {
				return apiKeyLogin(cmd, apiKey)
			}

			result, err := runDeviceLogin(cmd.Context(), newReporter(false), serverURL(), workspace, !noOpen)
			if err != nil {
				return err
			}
			// The latest login selects the credential for this host. Keep the new
			// browser session and drop an older API-key login that would shadow it.
			if err := auth.ClearAPIKeyLogin(); err != nil {
				return err
			}

			report := BuildIdentityReport(result.Session, &auth.NewClient(serverURL()).URLs)
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
	DeprecatedAPIBaseFlag(cmd)
	cmd.Flags().StringVar(&workspace, "workspace", "", "Preselect a workspace key")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not try to open the browser automatically")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Sign in with this API key instead of the browser")
	return cmd
}

// apiKeyLogin verifies a pasted or flag-supplied key with an API call,
// then stores it under a selected profile, or as a host-keyed api-key login
// when no profile is selected. PreRun injects the host-keyed key into
// ORQ_API_KEY on later commands. An unselected bartolo profile is unreachable.
func apiKeyLogin(cmd *cobra.Command, key string) error {
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
	client := auth.NewClient(serverURL()).WithContext(cmd.Context())
	projects, err := client.ListProjects(key)
	if err != nil {
		return fmt.Errorf("the key was not accepted by %s: %w", client.URLs.APIBaseURL, err)
	}

	profile := bartoloProfileName()
	if profile != "" {
		if err := saveAPIKeyProfile(key); err != nil {
			return err
		}
	} else {
		login := &auth.APIKeyLogin{
			APIBaseURL: client.URLs.APIBaseURL,
			Source:     auth.APIKeyLoginSource,
			APIKey:     key,
		}
		// Best-effort workspace provenance lets `orq status` name the workspace.
		// The key is already verified, so a failed lookup does not fail login.
		if ws, err := client.KeyWorkspace(key); err == nil && ws != "" {
			login.Workspaces = []map[string]any{{"key": ws}}
		}
		if err := auth.SaveAPIKeyLogin(login); err != nil {
			return err
		}
	}

	if wantsHumanView(cmd) {
		if profile != "" {
			success("Signed in with an API key (profile: %s, %d projects visible)", profile, len(projects))
			return nil
		}
		success("Signed in with an API key (%d projects visible)", len(projects))
		return nil
	}
	return emit(map[string]any{
		"method":   "api_key",
		"profile":  profile,
		"verified": true,
	})
}

func NewLogoutCommand() *cobra.Command {
	var yes bool
	var force bool
	var disconnect bool

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Revoke the refresh token and clear local credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if profileSelected() {
				return profileInForceError("log out")
			}
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				// No browser session, but an `orq auth login --api-key`
				// credential can still be the login on this host. Clearing it is
				// the whole point of logout for an api-key user; leaving it would
				// re-authenticate the next command against a "logged out" host.
				//
				// The read only decides the wording ("Signed out" vs "nothing to
				// clear"); a corrupt file that will not decode must still be
				// removed, or logout can never clear the very file that is
				// breaking every command. So on a read error, take it as "a login
				// was present" and clear it anyway.
				apiKeyLogin, readErr := auth.ReadAPIKeyLogin()
				if err := auth.ClearAPIKeyLogin(); err != nil {
					return err
				}
				apiKeyCleared := apiKeyLogin != nil || readErr != nil
				envCleared, err := clearShellEnvFile()
				if err != nil {
					return err
				}
				removed, removeFailed := disconnectOnLogout(&setupOptions{noInput: !hasInteractiveTTY(), yes: yes || force}, disconnect)
				warnLingeringAPIKeys()
				if wantsHumanView(cmd) {
					if apiKeyCleared {
						success("Signed out of the API-key login")
					} else {
						info("Not logged in - nothing to clear.")
					}
					reportClearedEnvFiles(envCleared)
					reportSurvivingGatewayKey("")
					return removalError(removeFailed)
				}
				if err := emit(map[string]any{
					"authenticated":               false,
					"cleared":                     apiKeyCleared,
					"env_files_cleared":           envCleared,
					"coding_agents_removed":       removed,
					"coding_agents_remove_failed": removeFailed,
					"gateway_key_id":              "",
					"session_file":                auth.SessionFilePath(),
				}); err != nil {
					return err
				}
				return removalError(removeFailed)
			}
			// The gateway key is deliberately not revoked by logout. Capture its
			// handle before deleting the session that owns the metadata so both
			// human and machine output can still tell the user what survives.
			gatewayKeyID := session.GatewayKeyID

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

			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
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
			// Clear any api-key login on the same host too: logout signs out of
			// this server, and leaving a stored api-key credential would keep the
			// next command authenticated after a logout that reported success.
			if err := auth.ClearAPIKeyLogin(); err != nil {
				return postLogoutError(gatewayKeyID, err)
			}
			envCleared, err := clearShellEnvFile()
			if err != nil {
				return postLogoutError(gatewayKeyID, err)
			}
			removed, removeFailed := disconnectOnLogout(&setupOptions{noInput: !hasInteractiveTTY(), yes: yes || force}, disconnect)
			warnLingeringAPIKeys()

			// Same human/machine split as login and whoami: the human view
			// returns early so a terminal never sees the structured payload,
			// and `-o json` never sees the check line. A kept-but-unrevoked
			// token is a warning, not a green success.
			if wantsHumanView(cmd) {
				if revokeErr == nil {
					success("Signed out")
				} else {
					Warn("local credentials cleared, but the server-side token was not revoked")
				}
				reportClearedEnvFiles(envCleared)
				reportSurvivingGatewayKey(gatewayKeyID)
				return postLogoutError(gatewayKeyID, removalError(removeFailed))
			}
			if err := emit(map[string]any{
				"authenticated":               false,
				"cleared":                     true,
				"revoked":                     revokeErr == nil,
				"env_files_cleared":           envCleared,
				"coding_agents_removed":       removed,
				"coding_agents_remove_failed": removeFailed,
				"gateway_key_id":              gatewayKeyID,
				"session_file":                auth.SessionFilePath(),
			}); err != nil {
				return postLogoutError(gatewayKeyID, err)
			}
			return postLogoutError(gatewayKeyID, removalError(removeFailed))
		},
	}
	DeprecatedAPIBaseFlag(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&force, "force", false, "Clear local credentials even if the server-side token revoke fails (implies --yes)")
	cmd.Flags().BoolVar(&disconnect, "disconnect", false, "Also remove orq from this machine's coding agents, without asking")
	return cmd
}

var errRemovalFailed = errors.New("orq could not be removed from one or more coding agents")

func removalError(failed bool) error {
	if failed {
		return errRemovalFailed
	}
	return nil
}

// postLogoutError preserves the only handle for a gateway key after its
// owning session has been deleted. Keep wrapping the original error so callers
// can still classify it with errors.Is/errors.As.
func postLogoutError(gatewayKeyID string, err error) error {
	if err == nil {
		return nil
	}
	if gatewayKeyID == "" {
		return fmt.Errorf("local session cleared: %w", err)
	}
	return fmt.Errorf("local session cleared; gateway key %s remains active and must be revoked separately: %w", gatewayKeyID, err)
}

// reportSurvivingGatewayKey names the one thing logout cannot undo. The key is
// still Active in the workspace until its own expiry, and the id is the only
// handle for killing it, so saying nothing here strands a live credential.
func reportSurvivingGatewayKey(id string) {
	if id != "" {
		info("the gateway key is still active — revoke it with: orq api-keys delete %s", id)
	}
}

// reportAPIKeyLogin renders `orq status` for a host authenticated by an
// `orq auth login --api-key` credential: the masked key, the server, and the
// workspace the key resolved to when it was stored, if any. The key is never
// printed in full, matching the profile path above.
func reportAPIKeyLogin(cmd *cobra.Command, login *auth.APIKeyLogin) error {
	server := strings.TrimSpace(login.APIBaseURL)
	if server == "" {
		server = auth.ResolveURLs(serverURL()).APIBaseURL
	}
	check := checkCredential(auth.NewClient(server).WithContext(cmd.Context()).ProbeToken(login.APIKey))
	workspace := ""
	for _, w := range login.Workspaces {
		if k, ok := w["key"].(string); ok && strings.TrimSpace(k) != "" {
			workspace = strings.TrimSpace(k)
			break
		}
	}
	if wantsHumanView(cmd) {
		success("Signed in with an API key")
		kv(9, "server", "%s", server)
		if workspace != "" {
			kv(9, "workspace", "%s", workspace)
		}
		kv(9, "api_key", "%s", maskToken(login.APIKey))
		check.warn()
		return nil
	}
	// orqi reads session_file even from API-key status. It remains the browser
	// session location; api_key_login_file identifies the credential reported here.
	out := map[string]any{
		"method":             "api_key",
		"server":             server,
		"workspace":          workspace,
		"api_key":            maskToken(login.APIKey),
		"session_file":       auth.SessionFilePath(),
		"api_key_login_file": auth.APIKeyLoginFilePath(),
		"identity":           nil,
		"authenticated":      check.Authenticated,
	}
	if check.AuthError != "" {
		out["auth_error"] = check.AuthError
	}
	if check.CheckError != "" {
		out["auth_check_error"] = check.CheckError
	}
	return emit(out)
}

func reportEnvironmentAPIKey(cmd *cobra.Command, key, source string) error {
	server := auth.ResolveURLs(serverURL()).APIBaseURL
	check := checkCredential(auth.NewClient(server).WithContext(cmd.Context()).ProbeToken(key))
	if wantsHumanView(cmd) {
		success("Using API key from %s", source)
		kv(9, "server", "%s", server)
		kv(9, "api_key", "%s", maskToken(key))
		check.warn()
		return nil
	}
	out := map[string]any{
		"method":        "api_key",
		"source":        source,
		"server":        server,
		"api_key":       maskToken(key),
		"session_file":  auth.SessionFilePath(),
		"identity":      nil,
		"authenticated": check.Authenticated,
	}
	if check.AuthError != "" {
		out["auth_error"] = check.AuthError
	}
	if check.CheckError != "" {
		out["auth_check_error"] = check.CheckError
	}
	return emit(out)
}

func activeStoredAPIKeyLogin() *auth.APIKeyLogin {
	if profileInForce() {
		return nil
	}
	login, err := auth.ReadAPIKeyLogin()
	if err != nil || login == nil || strings.TrimSpace(os.Getenv("ORQ_API_KEY")) != strings.TrimSpace(login.APIKey) {
		return nil
	}
	return login
}

// NewStatusCommand is whoami under the name people reach for first, and the
// one the help lists. Same report: who you are, where you are, and which
// credential the next command will use.
func NewStatusCommand() *cobra.Command {
	cmd := NewWhoAmICommand()
	cmd.Use = "status"
	cmd.Aliases = append(cmd.Aliases, "whoami")
	cmd.Short = "Show the active user, workspace, project and credential"
	return cmd
}

func NewWhoAmICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the current authenticated user and workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			if profileInForce() {
				rawKey := strings.TrimSpace(bartolocli.GetProfile()["api_key"])
				key := maskToken(rawKey)
				apiBase := auth.ResolveURLs(serverURL()).APIBaseURL
				check := credentialCheck{Authenticated: rawKey != ""}
				if rawKey != "" {
					check = checkCredential(auth.NewClient(apiBase).WithContext(cmd.Context()).ProbeToken(rawKey))
				}
				if wantsHumanView(cmd) {
					success("Using API-key profile %s", bartolocli.ActiveProfileName())
					kv(9, "server", "%s", apiBase)
					if key == "" {
						// The profile exists but carries no key, so every request
						// will fail; say that rather than print a blank field.
						kv(9, "api_key", "%s", "(not set — run `orq auth profile add "+bartolocli.ActiveProfileName()+" --api-key-file <file>`)")
						return nil
					}
					kv(9, "api_key", "%s", key)
					check.warn()
					return nil
				}
				out := map[string]any{
					"profile":       bartolocli.ActiveProfileName(),
					"server":        apiBase,
					"api_key":       key,
					"session_file":  auth.SessionFilePath(),
					"identity":      nil,
					"authenticated": check.Authenticated,
				}
				if check.AuthError != "" {
					out["auth_error"] = check.AuthError
				}
				if check.CheckError != "" {
					out["auth_check_error"] = check.CheckError
				}
				return emit(out)
			}
			// PreRun exports the active stored login into ORQ_API_KEY. When it
			// outranks a browser session on the same host, report that login.
			if login := activeStoredAPIKeyLogin(); login != nil {
				return reportAPIKeyLogin(cmd, login)
			}
			if explicitAPIKey {
				if key, source := ConfiguredCredential(); key != "" && slices.Contains(APIKeyEnvVars, source) {
					return reportEnvironmentAPIKey(cmd, key, source)
				}
			}
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				// A user-provided environment key outranks the stored login. Report
				// the key the next request will use before looking at the store.
				if key, source := ConfiguredCredential(); key != "" && slices.Contains(APIKeyEnvVars, source) {
					return reportEnvironmentAPIKey(cmd, key, source)
				}
				// No browser session, but an `orq auth login --api-key`
				// credential is still a login on this host. Report it rather than
				// claiming the user is logged out. A corrupt credential file is
				// surfaced, not read as "not logged in": whoami/status is exactly
				// where a user debugging a broken login looks, so the error has to
				// reach them instead of a misleading "you are not logged in".
				login, lerr := auth.ReadAPIKeyLogin()
				if lerr != nil {
					return lerr
				}
				if login != nil {
					return reportAPIKeyLogin(cmd, login)
				}
				return auth.ErrNotLoggedIn
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			report := BuildIdentityReport(session, &client.URLs)
			check := checkCredential(probeCredentialInForce(cmd, session))
			report.Authenticated, report.AuthError, report.AuthCheckError = check.Authenticated, check.AuthError, check.CheckError
			if wantsHumanView(cmd) {
				printIdentity(report, "Signed in as")
				check.warn()
				noteOtherLogins(cmd)
				return nil
			}
			return emit(report)
		},
	}
	DeprecatedAPIBaseFlag(cmd)
	return cmd
}

// probeCredentialInForce sends one workspace-scoped request with the key the
// next command will send: the explicit key when one outranks the session,
// otherwise the session token the root pre-run put in ORQ_API_KEY (minted here
// when the pre-run did not run). The profile fetch WhoAmI already made uses the
// bootstrap token, which the server's authz check does not cover. An
// authz_stale answer is refreshed by the client's transport before it counts.
func probeCredentialInForce(cmd *cobra.Command, session *auth.Session) error {
	client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
	bearer := strings.TrimSpace(os.Getenv("ORQ_API_KEY"))
	if explicitAPIKey {
		bearer, _ = ConfiguredCredential()
	}
	if bearer == "" {
		if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey == "" {
			return nil
		}
		var err error
		bearer, err = client.WithProject(session.ActiveProjectID).WorkspaceToken(session, *session.ActiveWorkspaceKey)
		if err != nil {
			return err
		}
	}
	return client.ProbeToken(bearer)
}

// credentialCheck is what a probe says about the credential. Only a 401 means
// the server rejected it; a timeout, 5xx or 403 says nothing about the key, so
// it leaves Authenticated alone and is reported as a failed check instead.
type credentialCheck struct {
	Authenticated bool
	AuthError     string
	CheckError    string
}

func checkCredential(err error) credentialCheck {
	switch {
	case err == nil:
		return credentialCheck{Authenticated: true}
	case auth.Unauthorized(err):
		return credentialCheck{AuthError: err.Error()}
	default:
		return credentialCheck{Authenticated: true, CheckError: err.Error()}
	}
}

func (c credentialCheck) warn() {
	switch {
	case c.AuthError != "":
		Warn("the server rejected this credential: %s", c.AuthError)
	case c.CheckError != "":
		Warn("could not verify the credential with the server: %s", c.CheckError)
	}
}

// printIdentity renders the friendly "who am I" block: a green headline plus an
// aligned key/value list. The structured report is reserved for scripts and
// `-o json`, so this is the primary output at a terminal.
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
	if report.ActiveProjectName != "" {
		// Marked, not hidden: the session really does record this project, but
		// under a credential that outranks the session nothing narrows a token
		// to it, so printing it bare names a project no command will use.
		if credentialOutranksSession(report) {
			kv(w, "project", "%s (inactive: the %s credential decides the scope)", report.ActiveProjectName, report.Credential.Source)
		} else {
			kv(w, "project", "%s", report.ActiveProjectName)
		}
	}
	if len(report.Workspaces) > 1 {
		kv(w, "access", "%d workspaces", len(report.Workspaces))
	}
	if report.Server != "" {
		kv(w, "server", "%s", report.Server)
	}
	// The credential the next command will authenticate with, named because
	// "signed in as X" alone can describe a state no command actually runs in
	// — a configured key outranks the session for every API call.
	if c := report.Credential; c != nil {
		kv(w, "key", "%s (%s)", c.Source, describeScope(*c))
	}
	kv(w, "session", "%s", report.SessionFile)
}

// describeScope renders a credential's reach. "scope not recorded" rather than
// "all projects" for the opaque key shape: that token carries no scope claims
// at all, and printing the silence as workspace-wide access told users
// something no local check could know.
func describeScope(c IdentityCredential) string {
	switch c.Scope {
	case scopeAllProjects:
		return "all projects"
	case scopeProject:
		if c.ProjectID != "" {
			return "one project"
		}
		return "specific projects"
	default:
		return "scope not recorded in the key"
	}
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

// noteOtherLogins points users with multiple saved logins to the listing.
func noteOtherLogins(cmd *cobra.Command) {
	sessions, err := auth.ListSessions()
	count := usableSessionCount(sessions)
	if err != nil || count < 2 {
		return
	}
	Notice("%d saved logins — see `%s auth sessions`", count, cmd.Root().Name())
}

func usableSessionCount(rows []auth.SessionListEntry) int {
	count := 0
	for _, row := range rows {
		if usableSessionStatus(row.Status) {
			count++
		}
	}
	return count
}
