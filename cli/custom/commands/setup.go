package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// A key living in cleartext in another program's config is the highest-risk
	// class PCI DSS 8.6.2 describes; 8.6.3 wants rotation at least annually and
	// sooner by risk. Renewal starts early enough that the replacement is wired
	// before the old key lapses.
	gatewayKeyLifetime    = 90 * 24 * time.Hour
	gatewayKeyRenewWindow = 30 * 24 * time.Hour

	defaultWebBaseURL = "https://my.orq.ai"
	docsURL           = "https://docs.orq.ai"
	setupSteps        = 3
)

type setupOptions struct {
	interactive bool
	workspace   string
	apiKey      string
	agents      []string
	noGateway   bool
	noInput     bool
	yes         bool
	// persistKey allows --api-key to replace the saved credential; only 'orq setup' sets it.
	persistKey bool
}

// --yes takes the affirmative without asking; --no-input or no TTY takes the default rather than blocking a script.
// A prompt that was drawn and then failed (Ctrl-C, closed terminal) is a decline, never the default: at a true-default
// prompt the default is "yes", and taking an abort as consent mints keys and edits shell profiles the user backed out of.
func (o *setupOptions) confirm(message string, def bool) bool {
	if o.yes {
		return true
	}
	if o.noInput {
		return def
	}
	answer := def
	if err := survey.AskOne(&survey.Confirm{Message: message, Default: def}, &answer, promptStdio()); err != nil {
		return false
	}
	return answer
}

// setupComplete is the run's verdict, and drives both the final screen and the
// setup_complete field. verified is one narrow fact — the API answered — so on
// its own it printed a green "Setup complete" directly above an agent that
// failed to wire, and told a CI job keying on the field that the run succeeded.
//
// Skipped counts with Error: an agent the run declined to wire is one the user
// asked for and did not get, so claiming completion above it is the same lie in
// a quieter voice. A run that never reached the agent step is a different thing
// and stays complete: --no-input stops after the key and reports no agents at
// all, having promised none.
func setupComplete(verified bool, agents []agentResult) bool {
	if !verified {
		return false
	}
	for _, a := range agents {
		if a.Error != "" || a.Skipped != "" {
			return false
		}
	}
	return true
}

type agentResult struct {
	Agent      string `json:"agent"`
	Provider   string `json:"provider,omitempty"`
	ModelCount int    `json:"model_count,omitempty"`
	Error      string `json:"error,omitempty"`
	// Skipped is the wire that was never attempted, as opposed to Error's wire
	// that was attempted and failed. Set only when nothing on disk wires the
	// agent afterwards: an earlier run's provider block surviving is not a skip.
	Skipped string `json:"skipped,omitempty"`
}

func NewSetupCommand() *cobra.Command {
	opts := setupOptions{}

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Authenticate and wire up your coding agents",
		Long: bartolocli.Markdown(`Gets a new machine from zero to working: signs you in, creates a ` +
			`workspace API key, and wires your coding agents to route model calls through the orq AI Gateway.

Run it bare for the short path, with ` + "`-i`" + ` to be asked about every choice, or fully ` +
			`flagged with ` + "`--no-input`" + ` for CI.

Supported agents: ` + strings.Join(agentIDs(), ", ") + `.

Credential order is ` + "`--api-key`" + ` → login session → ` + "`ORQ_API_KEY`" + `. Note this is
deliberately not the order ` + "`orq launch`" + ` uses: launch prefers an explicit
` + "`ORQ_API_KEY`" + ` over the session, because it configures one throwaway process. Setup
writes persistent configuration, so the workspace you picked in ` + "`orq auth login`" + `
wins over a key left exported in your shell.`),
		// A failure here is a runtime problem, not a usage problem.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.persistKey = true
			return runSetup(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Ask about every choice instead of inferring")
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key instead of logging in and creating one")
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked")
	return cmd
}

// applyGlobalFlags folds the viper-bound global flags (--no-input, --workspace)
// into opts once for every entry point, and forces --no-input without a TTY so
// an interactive prompt can never block a pipeline.
func applyGlobalFlags(opts *setupOptions) error {
	opts.noInput = viper.GetBool("no-input")
	if ws := strings.TrimSpace(viper.GetString("workspace")); ws != "" {
		opts.workspace = ws
	}
	if !hasInteractiveTTY() {
		opts.noInput = true
	}
	if opts.noInput {
		opts.interactive = false
	}
	return nil
}

func runSetup(cmd *cobra.Command, opts *setupOptions) error {
	// --no-input and --workspace are global flags (registerGlobalFlags), read from viper in applyGlobalFlags.
	if err := applyGlobalFlags(opts); err != nil {
		return err
	}

	rep := newReporter(opts.noInput)
	printSplash(bartolocli.Stderr, cmd.Root().Version)

	result := map[string]any{}

	// --- Step 1: authenticate ------------------------------------------------
	rep.step(1, setupSteps, "Authenticate")
	authState, err := resolveAuth(cmd.Context(), rep, opts)
	if err != nil {
		return err
	}
	result["profile"] = auth.ActiveProfile()

	client := auth.NewClient(authState.apiBase)

	// No project step: keys are workspace-scoped, and the key API takes a different id format than /v2/projects returns.

	// --- Step 2: API key -----------------------------------------------------
	rep.step(2, setupSteps, "API key")
	keyInfo, mintedToken, err := resolveAPIKey(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["api_key"] = keyInfo
	// Verify with the credential the agents will use, not this run's session token.
	if mintedToken != "" {
		authState.useDurableKey(mintedToken)
	}

	rep.step(3, setupSteps, "Coding agents")
	agentResults, err := setupConnectStep(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["coding_agents"] = agentResults
	if n, counted := authState.enabledModels, authState.enabledModelsCounted; counted {
		result["models_enabled"] = n
	}

	// Reachability only: a model call would spend the user's credits and write a trace into their workspace.
	rep.blank()

	verified := verifySetup(rep, client, authState)
	result["verified"] = verified

	links := buildLinks(authState)
	if len(links) > 0 {
		result["links"] = links
	}
	complete := setupComplete(verified, agentResults)
	result["setup_complete"] = complete

	printFinalScreen(rep, agentResults, links, client.RouterBaseURL(), complete, opts)

	if !wantsHumanView(cmd) {
		if err := emit(result); err != nil {
			return err
		}
	}
	if !complete && verified {
		return errAgentFailed
	}
	if !verified {
		return errors.New("setup finished but the verification call failed")
	}
	return nil
}

var errAgentFailed = errors.New("one or more coding agents could not be configured")

// ============================================================================
// Step 1 — authentication
// ============================================================================

type authState struct {
	apiBase string
	session *auth.Session
	// bearer authenticates the API calls setup makes on the user's behalf.
	bearer string
	// suppliedKey is set when the user brought their own key; we never mint then.
	suppliedKey string
	// durableKey is set whenever an API key becomes the bearer — minted this run or reused from a previous one.
	durableKey           bool
	enabledModels        int
	enabledModelsCounted bool
}

// useDurableKey makes an API key the bearer. Durability and the bearer move
// together: a second assignment site that sets one without the other is how a
// valid key ends up reported as "no durable API key".
func (s *authState) useDurableKey(key string) {
	s.bearer = key
	s.durableKey = true
}

// durableBearer reports whether bearer is an API key rather than the session's
// expiring access token. Only a key may be wired into agent configs: kimi
// embeds the literal value, and a session token 401s within the hour.
func (s *authState) durableBearer() bool {
	// A bearer must exist before it can be durable: session == nil is a proxy for
	// "an API key is the bearer", and on its own it makes the zero value report a
	// credential it does not have, writing api_key = "" into every agent config.
	if s.bearer == "" {
		return false
	}
	return s.suppliedKey != "" || s.durableKey || s.session == nil
}

func resolveAuth(ctx context.Context, rep *reporter, opts *setupOptions) (*authState, error) {
	// An explicit key wins; it carries no provenance, so it is saved with no workspace.
	// persistKey is false on connect: saving there replaces a credential the user
	// did not ask to replace, and blanks its recorded workspace, which disables
	// the mismatch guard permanently.
	if key := strings.TrimSpace(opts.apiKey); key != "" {
		if opts.persistKey {
			if err := saveAPIKeyProfile(key, ""); err != nil {
				return nil, err
			}
			rep.ok("api key (profile: %s)", auth.ActiveProfile())
		} else {
			rep.ok("api key from --api-key (not saved)")
		}
		return &authState{apiBase: apiBaseFromEnv(), bearer: key, suppliedKey: key}, nil
	}

	session, err := auth.ReadSession()
	if err != nil {
		return nil, err
	}

	// Bartolo auto-loads ./.env at startup, so "unset ORQ_API_KEY" is wrong advice when a file re-injects it every run.
	if envKey := UserEnvAPIKey(); envKey != "" && session == nil {
		if file, v := dotEnvAPIKey(); file != "" && v == envKey {
			rep.ok("api key from ./%s", file)
			rep.note("orq loads ./%s automatically — remove its ORQ_API_KEY line to sign in instead; unsetting the shell variable is not enough.", file)
		} else {
			rep.ok("api key from ORQ_API_KEY")
			rep.note("credential order: login session → ORQ_API_KEY (env). No session found, so the environment key is used.")
		}
		return &authState{apiBase: apiBaseFromEnv(), bearer: envKey, suppliedKey: envKey}, nil
	}

	if session != nil {
		envKey := UserEnvAPIKey()
		savedKey, savedWS := savedAPIKey()
		if auth.EnvKeyShadowsWorkspace(envKey, savedKey, savedWS, activeWorkspaceKey(session)) {
			if envKey == savedKey {
				rep.note("the exported ORQ_API_KEY is for workspace %s; this run follows your login instead", savedWS)
			} else {
				rep.note("the exported ORQ_API_KEY is not the key setup saved, so its workspace is unknown; this run follows your login instead")
			}
		}
	}

	if session == nil {
		if opts.noInput {
			return nil, errors.New("no TTY available for browser login\n  Pass --api-key <key> or set ORQ_API_KEY, then re-run")
		}
		session, err = deviceLogin(ctx, rep, opts)
		if err != nil {
			return nil, err
		}

	}
	signedInAs := "current user"
	if session.User != nil && session.User.Email != "" {
		signedInAs = session.User.Email
	}

	client := auth.NewClient(session.APIBaseURL)
	session, err = resolveWorkspace(rep, client, session, opts, signedInAs)
	if err != nil {
		return nil, err
	}

	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil {
		return nil, err
	}
	return &authState{apiBase: session.APIBaseURL, session: session, bearer: active.AccessToken}, nil
}

func apiBaseFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("ORQ_API_BASE_URL")); v != "" {
		return v
	}
	return auth.DefaultAPIBaseURL
}

func deviceLogin(ctx context.Context, rep *reporter, opts *setupOptions) (*auth.Session, error) {
	result, err := runDeviceLogin(ctx, rep, "", opts.workspace, true)
	if err != nil {
		return nil, err
	}
	if result.Session.User != nil {
		rep.ok("signed in as %s", result.Session.User.Email)
	}
	return result.Session, nil
}

// deviceLoginResult carries the session plus the activation details auth login emits in JSON.
type deviceLoginResult struct {
	Session         *auth.Session
	VerificationURI string
	UserCode        string
	BrowserOpened   bool
}

// runDeviceLogin is the shared device-login flow; callers report success their own way.
func runDeviceLogin(ctx context.Context, rep *reporter, apiBase, workspace string, openBrowser bool) (*deviceLoginResult, error) {
	// Context-aware so Ctrl-C cancels the poll instead of waiting out the device-code expiry.
	client := auth.NewClient(apiBase).WithContext(ctx)
	start, err := client.StartDeviceLogin("orq-cli")
	if err != nil {
		return nil, err
	}
	rep.note("Open: %s", start.VerificationURIComplete)
	rep.note("Code: %s", start.UserCode)
	browserOpened := false
	if openBrowser {
		browserOpened = auth.OpenBrowser(start.VerificationURIComplete)
		if !browserOpened {
			rep.note("Could not open the browser automatically. Open the URL manually.")
		}
	}
	rep.note("Waiting for browser approval...")

	approved, err := client.AwaitDeviceApproval(ctx, start.DeviceCode, start.ExpiresIn, start.Interval)
	if err != nil {
		return nil, err
	}
	profile, err := client.FetchProfile(approved.AccessToken)
	if err != nil {
		return nil, err
	}
	if workspace == "" && len(profile.Workspaces) > 1 && hasInteractiveTTY() {
		workspace, err = selectWorkspace(profile.Workspaces, "Choose an active workspace")
		if err != nil {
			return nil, err
		}
	}
	session, err := client.CreateSessionFromDeviceApproval(approved, profile, workspace)
	if err != nil {
		return nil, err
	}
	return &deviceLoginResult{
		Session:         session,
		VerificationURI: start.VerificationURIComplete,
		UserCode:        start.UserCode,
		BrowserOpened:   browserOpened,
	}, nil
}

func resolveWorkspace(rep *reporter, client *auth.Client, session *auth.Session, opts *setupOptions, signedInAs string) (*auth.Session, error) {
	key := opts.workspace
	if key == "" && session.ActiveWorkspaceKey != nil {
		key = *session.ActiveWorkspaceKey
	}
	if key == "" || (opts.interactive && opts.workspace == "") {
		chosen, err := selectWorkspace(session.Workspaces, "Which workspace?")
		if err != nil {
			return nil, err
		}
		key = chosen
	}
	if key == "" {
		return nil, errors.New("no workspace is available for this user")
	}
	updated, err := client.UseWorkspace(key)
	if err != nil {
		return nil, err
	}
	if signedInAs != "" {
		rep.ok("%s · workspace %s", signedInAs, key)
	} else {
		rep.ok("workspace %s", key)
	}
	return updated, nil
}

// displayLine shortens the env file to ~ so the printed command stays paste-able; the written file keeps the absolute path.
func (s shellSetup) displayLine() string {
	return strings.Replace(s.Line, s.EnvFile, tilde(s.EnvFile), 1)
}

// shellSetup describes how to give the user's shell the key.
type shellSetup struct {
	EnvFile string // file orq writes, holding the export
	Profile string // user's profile file, "" when the shell is unrecognised
	Line    string // line to add to that profile
}

// Mirrors install.sh's profile_for_shell, except zsh: agents may start from non-interactive shells, which read .zshenv but not .zshrc. fish cannot parse 'export VAR=value'.
func detectShell(dir string) shellSetup {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	posix := shellSetup{EnvFile: filepath.Join(dir, "env")}
	switch filepath.Base(strings.TrimSpace(os.Getenv("SHELL"))) {
	case "zsh":
		posix.Profile = filepath.Join(home, ".zshenv")
	case "bash":
		// macOS login shells read .bash_profile; Linux ones read .bashrc.
		if runtime.GOOS == "darwin" {
			if _, err := os.Stat(filepath.Join(home, ".bash_profile")); err == nil {
				posix.Profile = filepath.Join(home, ".bash_profile")
				break
			}
		}
		posix.Profile = filepath.Join(home, ".bashrc")
	case "fish":
		return shellSetup{
			EnvFile: filepath.Join(dir, "env.fish"),
			Profile: filepath.Join(home, ".config", "fish", "config.fish"),
			Line:    "source " + filepath.Join(dir, "env.fish"),
		}
	case "sh", "dash", "ksh":
		posix.Profile = filepath.Join(home, ".profile")
	}
	// Set even for an unrecognised shell: there is still a line to run, just no profile to name.
	posix.Line = ". " + posix.EnvFile
	return posix
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return path
	}
	return "~" + path[len(home):]
}

// Agent configs reference ORQ_API_KEY rather than inlining it, and nothing else exported it: agents came up with an empty bearer.
func writeShellEnvFile(token string) (string, error) {
	dir := viper.GetString("config-directory")
	if dir == "" {
		return "", errors.New("no config directory configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sh := detectShell(dir)
	assign := "export ORQ_API_KEY=" + token
	if strings.HasSuffix(sh.EnvFile, ".fish") {
		assign = "set -gx ORQ_API_KEY " + token
	}
	header := "# Written by 'orq setup'.\n"
	if sh.Line != "" {
		header += "# Add to " + sh.Profile + ":\n#     " + sh.Line + "\n"
	}
	if err := os.WriteFile(sh.EnvFile, []byte(header+assign+"\n"), 0o600); err != nil {
		return "", err
	}
	// A pre-existing file may have been created with looser permissions.
	return sh.EnvFile, os.Chmod(sh.EnvFile, 0o600)
}

func offerProfileSourceLine(rep *reporter, opts *setupOptions) {
	if opts.noInput {
		return
	}
	sh := detectShell(viper.GetString("config-directory"))
	if sh.Profile == "" || sh.Line == "" {
		return // unrecognised shell: the final screen prints the manual line
	}
	if profileSourcesEnvFile(sh) {
		return
	}
	if !opts.confirm(fmt.Sprintf("Add '%s' to %s so agents always see ORQ_API_KEY?", sh.displayLine(), tilde(sh.Profile)), true) {
		return
	}
	f, err := os.OpenFile(sh.Profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		rep.warn("could not update %s: %v", sh.Profile, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString("\n# Added by 'orq setup' — exports ORQ_API_KEY for coding agents.\n" + sh.Line + "\n"); err != nil {
		rep.warn("could not update %s: %v", sh.Profile, err)
		return
	}
	rep.ok("updated     %s — new shells only; run '%s' here", tilde(sh.Profile), sh.displayLine())
}

// Matches the home-relative suffix so "$HOME/.orq/env" and "~/.orq/env" count too; re-appending stacks duplicates.
func profileSourcesEnvFile(sh shellSetup) bool {
	data, err := os.ReadFile(sh.Profile)
	if err != nil {
		return false
	}
	needle := sh.EnvFile
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(needle, home+string(os.PathSeparator)) {
		needle = strings.TrimPrefix(needle, home+string(os.PathSeparator))
	}
	return strings.Contains(string(data), needle)
}

// Mirrors bartolo's saveAuthProfile, then chmods: viper writes 0644 and this file holds a live credential.
//
// Clears the gateway triple: savedAPIKey prefers gateway_key, so leaving a
// previously minted key in place meant a key the user passed with --api-key was
// silently ignored by the next command that wired an agent.
func saveAPIKeyProfile(key, workspace string) error {
	profile := auth.ActiveProfile()
	clearGatewayKeyFields(profile)
	return writeAPIKeyProfile(profile, key, workspace)
}

func clearGatewayKeyFields(profile string) {
	for _, field := range []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at"} {
		bartolocli.Creds.Set("profiles."+profile+"."+field, "")
	}
}

// saveGatewayKeyProfile stores the minted key under its own field. Writing it as
// api_key would make apiKeyConfigured true, and every generated command would
// then authenticate with a gateway-scoped credential instead of the session.
//
// Clears api_key for the mirror reason saveAPIKeyProfile clears the gateway
// triple. Versions before the split wrote the minted key to api_key with a
// workspace beside it, so an upgraded machine that switches workspace mints a
// new key while the old one survives — and api_key is the profile field
// apiKeyConfigured accepts, so every command would keep authenticating with a
// stale key for the workspace the user just left.
func saveGatewayKeyProfile(key, keyID string, expiresAt time.Time, workspace string) error {
	profile := auth.ActiveProfile()
	bartolocli.Creds.Set("profiles."+profile+".api_key", "")
	bartolocli.Creds.Set("profiles."+profile+".gateway_key_id", keyID)
	bartolocli.Creds.Set("profiles."+profile+".gateway_key_expires_at", expiresAt.UTC().Format(time.RFC3339))
	return writeGatewayKeyProfile(profile, key, workspace)
}

// gatewayKeyExpiry reports when the saved key expires. Not-ok means no expiry is
// recorded — a key minted before expiry existed, or one the user brought — and
// callers must treat that as "unknown", never as "expired".
func gatewayKeyExpiry() (time.Time, bool) {
	raw := strings.TrimSpace(bartolocli.GetProfile()["gateway_key_expires_at"])
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// gatewayKeyDueForRenewal reports whether the saved key is close enough to
// expiry to replace now, so an agent never meets the 401 itself.
func gatewayKeyDueForRenewal(now time.Time) bool {
	at, ok := gatewayKeyExpiry()
	return ok && at.Sub(now) < gatewayKeyRenewWindow
}

// clearAPIKeyProfile removes the stored keys; without it logout leaves a live key in credentials.json.
func clearAPIKeyProfile() (bool, error) {
	profile := auth.ActiveProfile()
	held := strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) != "" ||
		strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".gateway_key")) != ""
	if !held {
		return false, nil
	}
	clearGatewayKeyFields(profile)
	return true, writeAPIKeyProfile(profile, "", "")
}

func savedAPIKey() (key, workspace string) { return auth.SavedAgentKey() }

func savedGatewayKeyID() string {
	return strings.TrimSpace(bartolocli.GetProfile()["gateway_key_id"])
}

func activeWorkspaceKey(session *auth.Session) string {
	if session != nil && session.ActiveWorkspaceKey != nil {
		return strings.TrimSpace(*session.ActiveWorkspaceKey)
	}
	return ""
}

// Either side unknown means no mismatch: an unrecorded workspace (pre-field keys, --api-key) must not invalidate a working credential.
func keyWorkspaceMismatch(savedWS, active string) bool {
	return savedWS != "" && active != "" && savedWS != active
}

// bartolo's resolveAuthHandler looks a profile type up verbatim and falls back to the sole handler only when empty, so a descriptive type breaks every generated command; the name is read back from the registry.
func BartoloAuthType() string {
	if _, ok := bartolocli.AuthHandlers["apikey"]; ok {
		return "apikey"
	}
	if len(bartolocli.AuthHandlers) == 1 {
		for name := range bartolocli.AuthHandlers {
			return name
		}
	}
	return ""
}

func writeAPIKeyProfile(profile, key, workspace string) error {
	bartolocli.Creds.Set("profiles."+profile+".api_key", key)
	return writeCredsProfile(profile, workspace)
}

func writeGatewayKeyProfile(profile, key, workspace string) error {
	bartolocli.Creds.Set("profiles."+profile+".gateway_key", key)
	return writeCredsProfile(profile, workspace)
}

func writeCredsProfile(profile, workspace string) error {
	bartolocli.Creds.Set("profiles."+profile+".type", BartoloAuthType())
	bartolocli.Creds.Set("profiles."+profile+".workspace", workspace)
	filename := path.Join(viper.GetString("config-directory"), "credentials.json")
	if err := bartolocli.Creds.WriteConfigAs(filename); err != nil {
		return err
	}
	return os.Chmod(filename, 0o600)
}

// clearShellEnvFile removes the exported key from the file `orq setup` wrote,
// leaving the file itself in place: a shell profile may carry `. ~/.orq/env`,
// and deleting the target would make every new shell report a missing file.
// Both spellings are cleared — a user who changed shells since setup has the
// other one still sitting there.
func clearShellEnvFile() []string {
	dir := viper.GetString("config-directory")
	if dir == "" {
		return nil
	}
	var cleared []string
	for _, name := range []string{"env", "env.fish"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "ORQ_API_KEY") {
			continue
		}
		body := "# Cleared by 'orq auth logout'. Run 'orq setup' to create a new key.\n"
		if os.WriteFile(path, []byte(body), 0o600) == nil {
			cleared = append(cleared, path)
		}
	}
	return cleared
}

// storedAPIKeyProfile reports whether the active profile holds a key that
// authenticates commands, so it reads the one profile field apiKeyConfigured
// accepts. Doctor asks about the environment separately, hence no env check here.
// Counting gateway_key here made doctor answer "authenticated: credentials.json"
// for a key no command authenticates with, on the one screen whose job is to
// explain why nothing works. Callers must not log the value.
func storedAPIKeyProfile() bool {
	prefix := "profiles." + auth.ActiveProfile()
	return strings.TrimSpace(bartolocli.Creds.GetString(prefix+".api_key")) != ""
}

// Bartolo loads these files before any command runs, so a key here outlives 'unset' and logout; the parsing mirrors its loadDotEnvFile.
func dotEnvAPIKey() (file, value string) {
	for _, name := range []string{".env", ".env.local"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) != "ORQ_API_KEY" {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
				v = v[1 : len(v)-1]
			}
			// A placeholder (ORQ_API_KEY=) is no credential and must not hide a later file that holds one.
			if v == "" {
				continue
			}
			return name, v
		}
	}
	return "", ""
}

// warnLingeringAPIKeys names the credentials logout cannot clear, or the next command silently authenticates again.
func warnLingeringAPIKeys() {
	file, v := dotEnvAPIKey()
	if file != "" {
		Warn("./%s still sets ORQ_API_KEY and orq loads it automatically — remove that line to fully sign out", file)
	}
	// Independent sources: explicitAPIKey is snapshotted before our PreRun injects a token, and an export matching the dotenv value is indistinguishable from it.
	if explicitAPIKey && envAPIKeySet() && (file == "" || v != strings.TrimSpace(os.Getenv("ORQ_API_KEY"))) {
		Warn("ORQ_API_KEY is still exported in this shell — logout cannot unset it; run: unset ORQ_API_KEY")
	}
	for _, spec := range agentRegistry() {
		if _, prov := wiredPath(spec.providerConfig, spec.providerPresent); prov {
			Warn("coding agents keep their orq config after logout — 'orq disconnect' removes it")
			return
		}
	}
}

// envAPIKeySet reports only that a key is set, never which value.
func envAPIKeySet() bool {
	for _, name := range []string{"ORQ_API_KEY", "ORQ_TOKEN", "ORQ_AUTHORIZATION"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// ============================================================================
// Step 2 — API key
// ============================================================================

// resolveAPIKey returns the payload summary and, when it minted one, the raw token.
func resolveAPIKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (map[string]any, string, error) {
	info := map[string]any{"created": false, "profile": auth.ActiveProfile()}

	if state.suppliedKey != "" {
		rep.ok("using the API key you passed")
		return info, "", nil
	}

	token, created, err := ensureDurableKey(rep, client, state, opts)
	if err != nil {
		return nil, "", err
	}
	info["created"] = created
	if token == "" {
		return info, "", nil
	}
	if path, err := writeShellEnvFile(token); err != nil {
		// Not fatal: the key is saved, and the final screen still shows how to export it.
		rep.warn("could not write the shell env file: %v", err)
	} else {
		// Labelled, not indented under "key saved": that line is skipped when an earlier setup's key is reused.
		rep.ok("env file    %s  → exports ORQ_API_KEY", tilde(path))
		offerProfileSourceLine(rep, opts)
	}

	return info, token, nil
}

// ensureDurableKey reuses the saved workspace key or mints and persists a new
// one. Reuse is workspace-scoped: keys are minted per workspace, and minting
// per run orphaned every copy of the old one. An interactive setup may decline
// the mint, which returns an empty token.
func ensureDurableKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (token string, created bool, err error) {
	token, tokenWS := savedAPIKey()
	active := activeWorkspaceKey(state.session)
	switch {
	case token == "":
		// nothing saved — fall through to mint
	case keyWorkspaceMismatch(tokenWS, active):
		rep.note("saved key belongs to workspace %s — creating one for %s", tokenWS, active)
		token = ""
	case gatewayKeyDueForRenewal(time.Now()):
		// The superseded key is left alive until its own expiry: that overlap is
		// what keeps an agent config working until this run rewrites it.
		rep.note("saved key expires soon — creating its replacement; run 'orq connect' to rewire the agents")
		token = ""
	case tokenWS == "":
		rep.ok("reusing the key from an earlier setup (workspace unrecorded)")
	default:
		rep.ok("reusing the key from an earlier setup")
	}
	if token != "" {
		return token, false, nil
	}
	if opts.interactive && !opts.confirm("Create a workspace API key now?", true) {
		rep.ok("skipped creating an API key")
		return "", false, nil
	}

	// Plain ASCII: the dashboard echoes the name back and we do not know its validation rules.
	hostname, _ := os.Hostname()
	hostname = strings.TrimSuffix(hostname, ".local")
	if hostname == "" {
		hostname = "unknown-host"
	}
	// Purpose before hostname: the dashboard lists every restricted key as
	// "Restricted" without saying restricted to what, and sanitizeKeyName
	// truncates from the right, so a trailing purpose is what a long corporate
	// hostname would eat.
	keyName := sanitizeKeyName("orq-cli " + capGateway + " " + hostname)

	// Without a user id the API mints against a service account, which only admins may create.
	userID := ""
	if state.session != nil && state.session.User != nil {
		userID = state.session.User.ID
	}
	expiresAt := time.Now().Add(gatewayKeyLifetime)
	req, err := auth.NewAPIKeyRequest(keyName, auth.GatewayAccess(), expiresAt, auth.WithUser(userID))
	if err != nil {
		return "", false, err
	}
	minted, keyID, _, err := client.CreateAPIKey(state.bearer, req)
	if err != nil {
		return "", false, err
	}
	// The API returns the raw token and its id once: persist before anything else can fail.
	if err := saveGatewayKeyProfile(minted, keyID, expiresAt, active); err != nil {
		return "", false, fmt.Errorf("created a key but could not save it: %w", err)
	}
	rep.ok("key saved   %s  (gateway-scoped, expires in %d days)",
		tilde(filepath.Join(viper.GetString("config-directory"), "credentials.json")),
		int(gatewayKeyLifetime.Hours()/24))
	suggestKeyBudget(rep, keyID)
	return minted, true, nil
}

// suggestKeyBudget recommends a spend ceiling rather than setting one. Budgets
// are a workspace-scope RPC, so a Developer or Researcher — most of the people
// onboarding — is refused, and a cap applied silently would stop an agent
// mid-task with nothing naming the cause.
func suggestKeyBudget(rep *reporter, keyID string) {
	if keyID == "" {
		return
	}
	rep.note("cap this key's spend: orq budgets create --scope '{\"api_key\":{\"api_key_id\":\"%s\"}}' \\", keyID)
	rep.note("      --limits '{\"period\":\"BUDGET_PERIOD_MONTHLY\",\"amount\":50}'")
}

func sanitizeKeyName(name string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
			lastSpace = false
		case r == ' ' || r == '\t':
			if !lastSpace && b.Len() > 0 {
				b.WriteRune(' ')
				lastSpace = true
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 64 {
		out = strings.TrimSpace(out[:64])
	}
	if out == "" {
		out = "orq-cli"
	}
	return out
}

func maskToken(token string) string {
	if len(token) <= 12 {
		return "sk-orq-…"
	}
	return token[:12] + "…"
}

// ============================================================================
// Gateway readiness helpers
// ============================================================================

const (
	modelsPath    = "/router/models"
	providersPath = "/router/providers"
	creditsPath   = "/admin/credits"
	// gatewayIntroPath needs no workspace key, so it doubles as the keyless answer.
	gatewayIntroPath = "/docs/ai-gateway/get-started/introduction"
)

// sessionWorkspaceToken returns a workspace-scoped session token, preferring the one already in memory.
func sessionWorkspaceToken(client *auth.Client, state *authState) string {
	// Without this, an --api-key run would refresh whatever session is on disk and read the wrong workspace.
	if state == nil || state.session == nil {
		return ""
	}
	if token := storedWorkspaceToken(state.session); token != "" {
		return token
	}
	// A refresh persists a new session: fine mid-setup, wrong from doctor, so it lives at the call site.
	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil || active == nil {
		return ""
	}
	return active.AccessToken
}

// storedWorkspaceToken returns a session's unexpired workspace token. No network, no refresh, no writes.
func storedWorkspaceToken(session *auth.Session) string {
	if session == nil || session.ActiveWorkspaceKey == nil {
		return ""
	}
	tok, ok := session.WorkspaceTokens[*session.ActiveWorkspaceKey]
	if !ok || tok.Token == "" || isTokenExpired(tok.ExpiresAt) {
		return ""
	}
	return tok.Token
}

// Warn rather than note: notes are suppressed under --no-input.
func reportGatewayReadiness(rep *reporter, state *authState, opts *setupOptions, count int) {
	noModels := count == 0
	models := workspaceLink(state, modelsPath)

	if !noModels {
		rep.ok("%d models available to coding agents", count)
	} else {
		rep.warn("no models enabled for this workspace")
		if models != "" {
			rep.warn("    Enable models   %s", models)
		}
	}
	if !noModels {
		return
	}
	rep.warn("    How it works    %s", docsURL+gatewayIntroPath)

	// Offered for zero models only. That state leaves nothing callable, so the
	// page is worth the interruption; a zero balance may block nothing at all,
	// and stopping every run to ask about it was the cost of not knowing.
	if models == "" || opts.noInput || !opts.confirm("Open the models page now?", true) {
		return
	}
	if !auth.OpenBrowser(models) {
		rep.note("  could not open a browser, the URL above is the one to visit")
	}
}

// listEnabledModels returns the enabled-model count, retrying because a freshly created key is not live yet.
func listEnabledModels(client *auth.Client, state *authState) (int, error) {
	var models []auth.RouterModel
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if models, err = client.ListModels(state.bearer); err == nil {
			break
		}
	}
	if err != nil {
		return 0, err
	}
	// m.Enabled, not is_active: is_active covers the whole catalogue and reported hundreds of models on a workspace with no provider connected.
	enabled := 0
	for _, m := range models {
		if auth.UsableForCodingAgent(m) {
			enabled++
		}
	}
	return enabled, nil
}

// Dashboard paths must be prefixed with the workspace key; unprefixed ones 404. Keyless runs get the docs page.
func workspaceURL(state *authState, path string) string {
	if u := workspaceLink(state, path); u != "" {
		return u
	}
	apiBase := ""
	if state != nil {
		apiBase = state.apiBase
	}
	return dashboardURL(apiBase, "", path)
}

// dashboardURL is the one place a settings link is built.
func dashboardURL(apiBase, workspaceKey, path string) string {
	base := webBaseFor(apiBase)
	if base == "" || workspaceKey == "" {
		return docsURL + gatewayIntroPath
	}
	return base + "/" + workspaceKey + path
}

// workspaceLink is workspaceURL without the docs fallback: "" when there is no dashboard URL or key, so callers with several links omit what they cannot build.
func workspaceLink(state *authState, path string) string {
	key := ""
	apiBase := ""
	if state != nil {
		apiBase = state.apiBase
		if state.session != nil && state.session.ActiveWorkspaceKey != nil {
			key = *state.session.ActiveWorkspaceKey
		}
	}
	if webBaseFor(apiBase) == "" || key == "" {
		return ""
	}
	return dashboardURL(apiBase, key, path)
}

// ============================================================================
// Step 3 — coding agents
// ============================================================================

func instrumentAgents(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) ([]agentResult, error) {
	selected := opts.agents
	if len(selected) == 0 || opts.noGateway {
		return nil, nil
	}

	if !opts.noGateway {
		if count, err := listEnabledModels(client, state); err != nil {
			rep.warn("could not list gateway models: %v", err)
		} else {
			state.enabledModels, state.enabledModelsCounted = count, true
			reportGatewayReadiness(rep, state, opts, count)
		}
	}

	results := make([]agentResult, 0, len(selected))

	for _, id := range selected {
		spec, ok := lookupAgent(id)
		if !ok {
			rep.fail("%s is not a supported agent (%s)", id, strings.Join(agentIDs(), ", "))
			results = append(results, agentResult{Agent: id, Error: "unsupported agent"})
			continue
		}
		res := agentResult{Agent: id}

		if spec.writeProvider == nil {
			// claude routes through env only, so saying nothing would read as a
			// successful wire that never happened.
			rep.info("%-8s no gateway provider config for this agent — nothing to configure", id)
		} else {
			writeAgentProvider(rep, client, state, opts, spec, &res)
		}

		reportAgent(rep, spec, res, opts)
		results = append(results, res)
	}
	return results, nil
}

// writeAgentProvider registers orq as a model provider for one agent, recording
// the outcome on res: an error only where the user consented to a wire that
// then failed, so the exit code and the JSON agree with the terminal.
func writeAgentProvider(rep *reporter, client *auth.Client, state *authState, opts *setupOptions, spec agentSpec, res *agentResult) {
	id := spec.ID
	path, perr := spec.providerConfig(false)
	switch {
	case perr != nil:
		rep.fail("%-8s provider  %v", id, perr)
		res.Error = perr.Error()
	case path == "":
		// No resolver returns ("", nil) today; silence here would read as success for a wire that never happened.
		rep.fail("%-8s provider  no config path for this agent", id)
		res.Error = "provider config path resolved empty"
	case !state.durableBearer():
		// Declining the mint is a choice, not an error. Same two states as the
		// empty-catalogue arm below: an earlier run's block still wires the
		// agent, an absent one leaves it unwired.
		rep.warn("%-8s provider  skipped: no durable API key to wire — re-run 'orq setup' to mint one", id)
		if spec.providerPresent == nil || !spec.providerPresent(path) {
			res.Skipped = "no durable API key"
		}
	default:
		models, merr := codingModels(rep, client, state)
		switch {
		case merr != nil:
			// An unreadable catalogue is a failed wire, not an empty workspace.
			rep.fail("%-8s provider  %v", id, merr)
			res.Error = merr.Error()
		case len(models) == 0:
			// Every writer clears its own keys before emitting, so writing an empty catalogue would delete a working provider block from an earlier run.
			// Say which of the two states this is: an earlier run's block still wires the agent, an absent one leaves it unwired.
			if spec.providerPresent != nil && spec.providerPresent(path) {
				rep.warn("%-8s provider  no models to offer; kept the orq provider already in %s", id, tilde(path))
			} else {
				rep.warn("%-8s provider  no models to offer; nothing written to %s", id, tilde(path))
				res.Skipped = "no models to offer"
			}
		default:
			// Must match the [models."<key>"] form the writer emits: the full provider/model ref.
			defaultModel := ""
			if best, ok := defaultCodingModel(rep, client, state); ok {
				defaultModel = best.Ref()
			}
			// An empty defaultModel is fine: codex omits the model key, kimi omits default_model.
			written, werr := spec.writeProvider(path, client.RouterBaseURL(), state.bearer, models, defaultModel)
			if werr != nil {
				// A consented wire that failed must reach the exit code and the JSON.
				rep.fail("%-8s provider  %v", id, werr)
				res.Error = werr.Error()
			} else {
				res.Provider = path
				res.ModelCount = written
			}
		}
	}
}

func reportAgent(rep *reporter, spec agentSpec, res agentResult, opts *setupOptions) {
	if res.Provider == "" {
		return
	}
	wired := "gateway"
	// codex's profile names one default and takes its list from elsewhere, so only claim a count when there is one.
	if res.ModelCount > 0 {
		wired = fmt.Sprintf("gateway (%d models)", res.ModelCount)
	}

	rep.ok("%-8s %-24s %s", spec.ID, wired, tilde(res.Provider))

	// Every other agent reads ORQ_API_KEY from the environment. This one cannot,
	// so the user should know a live credential is now sitting in that file.
	if spec.providerEmbedsKey {
		rep.note("         this config holds the key itself, not a reference to ORQ_API_KEY")
	}
	// orq is registered as an option, not the default, so some agents need a step the user would never find.
	if spec.providerUsage != "" {
		rep.note("         %s", spec.providerUsage)
	}
}

var cachedCodingModels []auth.RouterModel
var codingModelsFetched bool

// codingModels fetches the gateway catalogue once per run: the enabled chat models with function calling, the same pool 'orq launch' writes.
// The error is returned, not swallowed into an empty slice: a fetch that failed and a workspace with nothing enabled are different facts, and
// reporting the first as the second told users to go enable models in a workspace that already had them, then exited 0 having wired nothing.
func codingModels(rep *reporter, client *auth.Client, state *authState) ([]auth.RouterModel, error) {
	if codingModelsFetched {
		return cachedCodingModels, nil
	}

	all, err := client.ListModels(state.bearer)
	if err != nil {
		// Not memoized: one transient failure would otherwise poison every later agent in the loop.
		return nil, fmt.Errorf("could not list gateway models: %w", err)
	}
	codingModelsFetched = true
	for _, m := range all {
		if auth.UsableForCodingAgent(m) {
			cachedCodingModels = append(cachedCodingModels, m)
		}
	}
	return cachedCodingModels, nil
}

// defaultCodingModel picks by preference order, then anything enabled. No probe: it would bill a completion, and a tools-less probe passed models that later failed.
func defaultCodingModel(rep *reporter, client *auth.Client, state *authState) (auth.RouterModel, bool) {
	models, err := codingModels(rep, client, state)
	if err != nil || len(models) == 0 {
		return auth.RouterModel{}, false
	}

	for _, group := range auth.CandidateCodingModels(models, preferredCodingModels) {
		if len(group) > 0 {
			return group[0], true
		}
	}
	// No preferred family, but anything callable beats reporting none: on false
	// the caller leaves defaultModel empty, writers omit the key, and the agent
	// falls back to a bundled id the gateway cannot address.
	return models[0], true
}

func promptForAgents(rep *reporter) ([]string, error) {
	registry := agentRegistry()
	options := make([]string, 0, len(registry))
	byOption := map[string]string{}
	defaults := []string{}

	detected := []string{}
	for _, spec := range registry {
		if spec.writeProvider == nil {
			continue
		}
		label := fmt.Sprintf("%-9s %s", spec.ID, spec.Label)
		options = append(options, label)
		byOption[label] = spec.ID
		if spec.detect != nil && spec.detect() {
			defaults = append(defaults, label)
			detected = append(detected, spec.Label)
		}
	}
	if len(detected) > 0 {
		rep.note("Detected: %s", strings.Join(detected, ", "))
	}

	var chosen []string
	if err := survey.AskOne(&survey.MultiSelect{
		Message: "Instrument which agents?",
		Options: options,
		Default: defaults,
	}, &chosen); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(chosen))
	for _, label := range chosen {
		ids = append(ids, byOption[label])
	}
	return ids, nil
}

// ============================================================================
// Verification and the final screen
// ============================================================================

// verifySetup makes one authenticated call with the new credentials. Not whoami: an --api-key run has no session and whoami would report "not logged in".
func verifySetup(rep *reporter, client *auth.Client, state *authState) bool {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		// A freshly minted key is not accepted for a second or two; without the retry a working setup reports as broken.
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if _, err = client.ListProjects(state.bearer); err == nil {
			return true
		}
	}
	rep.fail("api unreachable  %s: %v", client.URLs.APIBaseURL, err)
	return false
}

// webBaseURL is the dashboard origin, "" when self-hosted without ORQ_WEB_BASE_URL.
// webBaseFor derives the dashboard host, shared with doctor.
func webBaseFor(apiBase string) string {
	webBase := strings.TrimRight(os.Getenv("ORQ_WEB_BASE_URL"), "/")
	if webBase == "" && apiBase == auth.DefaultAPIBaseURL {
		webBase = defaultWebBaseURL
	}
	return webBase
}

func buildLinks(state *authState) map[string]string {
	links := map[string]string{"docs": docsURL}
	apiBase := ""
	if state != nil {
		apiBase = state.apiBase
	}
	webBase := webBaseFor(apiBase)
	if webBase != "" && state.session != nil && state.session.ActiveWorkspaceKey != nil {
		links["workspace"] = webBase + "/" + *state.session.ActiveWorkspaceKey
	}
	// Outside the guard: workspaceURL degrades to the docs page, and keyless runs printed blank URLs when these sat inside it.
	links["models"] = workspaceURL(state, modelsPath)
	links["providers"] = workspaceURL(state, providersPath)
	links["credits"] = workspaceURL(state, creditsPath)
	return links
}

func printFinalScreen(rep *reporter, agents []agentResult, links map[string]string, routerBase string, verified bool, opts *setupOptions) {
	if opts.noInput {
		return
	}
	w := rep.w
	fmt.Fprintln(w)
	if verified {
		fmt.Fprintf(w, "  %s %s\n", paint(ansiOK, "✓"), bold("Setup complete"))
	} else {
		fmt.Fprintf(w, "  %s %s\n", paint(ansiWarn, "!"), bold("Setup finished with failed checks — see above"))
	}
	fmt.Fprintln(w)

	gatewayWired := []string{}
	// starts are the wired agents' own commands, never 'orq launch': launch builds a throwaway home and would not exercise what setup wrote.
	starts := []string{}
	for _, a := range agents {
		if a.Error != "" {
			continue
		}
		spec, ok := lookupAgent(a.Agent)
		if !ok {
			continue
		}
		if a.Provider == "" {
			continue
		}
		gatewayWired = append(gatewayWired, spec.Label)
		starts = append(starts, spec.startCommand())
	}
	switch {
	case len(gatewayWired) > 0:
		fmt.Fprintf(w, "  %s %s model calls through orq.\n",
			strings.Join(gatewayWired, " and "), pluralize(len(gatewayWired), "routes its", "route their"))
	default:
		// 'orq launch' only belongs here: nothing durable was written for it to shadow.
		fmt.Fprintln(w, "  Route an existing OpenAI client through the gateway:")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "      client = OpenAI(api_key=os.environ[\"ORQ_API_KEY\"],\n"+
			"                      base_url=\"%s\")\n", routerBase)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s orq launch %s\n", padLabel("Launch"), detectedAgentID())
		fmt.Fprintf(w, "  %s orq connect\n", padLabel("Wire"))
	}
	keyReferenced := false
	for _, a := range agents {
		if a.Error == "" && a.Provider != "" {
			keyReferenced = true
		}
	}
	// The env var is empty in this shell even on a wired machine, so the branches below consult the profile: re-printing the append line stacks duplicates.
	if keyReferenced && UserEnvAPIKey() == "" {
		sh := detectShell(viper.GetString("config-directory"))
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s %s\n", paint(ansiWarn, "!"),
			"ORQ_API_KEY is not exported here, and agents inherit it from this shell.")
		switch {
		// A declined mint leaves no env file; sourcing it would be the remedy that fails.
		case fileMissing(sh.EnvFile):
			fmt.Fprintf(w, "    %s\n", paint(ansiDim, "run 'orq setup' to create an API key and its env file"))
		case sh.Profile != "" && profileSourcesEnvFile(sh):
			fmt.Fprintf(w, "    %s\n", sh.displayLine())
			fmt.Fprintf(w, "    %s\n", paint(ansiDim, fmt.Sprintf("new shells already get it from %s", tilde(sh.Profile))))
		case sh.Profile == "":
			fmt.Fprintf(w, "    %s\n", sh.displayLine())
			fmt.Fprintf(w, "    %s\n", paint(ansiDim, "add that line to your shell profile so new shells get it too"))
		default:
			fmt.Fprintf(w, "    echo '%s' >> %s && %s\n", sh.displayLine(), tilde(sh.Profile), sh.displayLine())
		}
	}
	// After the env warning on purpose: these commands only authenticate once ORQ_API_KEY is exported.
	if len(starts) > 0 {
		fmt.Fprintln(w)
		for i, cmd := range starts {
			label := "Start"
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(w, "  %s %s\n", padLabel(label), cmd)
		}
	}
	fmt.Fprintln(w)
	if ws := links["workspace"]; ws != "" {
		fmt.Fprintf(w, "  %s %s\n", padLabel("Workspace"), ws)
	}
	fmt.Fprintf(w, "  %s orq doctor  %s  %s\n", padLabel("Stuck?"), paint(ansiDim, "·"), docsURL)
	fmt.Fprintln(w)
}

// labelWidth pads every action and link label; wide enough for "Workspace", the longest one printed.
const labelWidth = 11

func padLabel(s string) string {
	return paint(ansiDim, pad(s, labelWidth))
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// detectedAgentID names an installed agent so the screen never suggests a binary the user does not have.
func detectedAgentID() string {
	for _, spec := range agentRegistry() {
		if spec.detect != nil && spec.detect() {
			return spec.ID
		}
	}
	return "claude"
}
