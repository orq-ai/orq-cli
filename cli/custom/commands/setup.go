package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Redirects from orq-ai/orq-skills; this is the name the plugin repo documents.
	skillsRepo        = "orq-ai/assistant-plugins"
	defaultWebBaseURL = "https://my.orq.ai"
	docsURL           = "https://docs.orq.ai"
	setupSteps        = 3
)

type setupOptions struct {
	interactive    bool
	workspace      string
	apiKey         string
	agents         []string
	global         bool
	local          bool
	noCodingAgents bool
	noMCP          bool
	noGateway      bool
	noInput        bool
	yes            bool
	// narrowing records that the branch flags came from 'setup coding-agents', whose spellings are --gateway/--mcp.
	narrowing bool
}

// skipFlag names the flag in the spelling of the command actually run: the subcommand has no --no-gateway to send people looking for.
func (o *setupOptions) skipFlag(branch string) string {
	if o.narrowing {
		// On the subcommand, a branch is off because the *other* one was named.
		if branch == "mcp" {
			return "--gateway"
		}
		return "--mcp"
	}
	return "--no-" + branch
}

// --yes takes the affirmative without asking; --no-input or no TTY takes the default rather than blocking a script.
func (o *setupOptions) confirm(message string, def bool) bool {
	if o.yes {
		return true
	}
	if o.noInput {
		return def
	}
	answer := def
	if err := survey.AskOne(&survey.Confirm{Message: message, Default: def}, &answer, promptStdio()); err != nil {
		return def
	}
	return answer
}

type agentResult struct {
	Agent      string `json:"agent"`
	MCP        string `json:"mcp,omitempty"`
	Provider   string `json:"provider,omitempty"`
	ModelCount int    `json:"model_count,omitempty"`
	Error      string `json:"error,omitempty"`
}

func NewSetupCommand() *cobra.Command {
	opts := setupOptions{}

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Authenticate and wire up your coding agents",
		Long: bartolocli.Markdown(`Gets a new machine from zero to working: signs you in, creates a ` +
			`workspace API key, and wires your coding agents to orq — the AI Gateway as a ` +
			`model provider, and the orq.ai MCP server for workspace tools.

Projects are never asked about here: keys are workspace-scoped, and project ` +
			`scope belongs where resources are created (agents, deployments).

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
			return runSetup(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Ask about every choice instead of inferring")
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key instead of logging in and creating one")
	f.StringSliceVar(&opts.agents, "coding-agent", nil, "Coding agent to wire (repeatable): "+strings.Join(agentIDs(), ", "))
	f.StringSliceVar(&opts.agents, "agent", nil, "Deprecated alias for --coding-agent")
	_ = f.MarkHidden("agent")
	f.BoolVar(&opts.global, "global", false, "Write agent config to the home directory instead of this project")
	f.BoolVar(&opts.noCodingAgents, "no-coding-agents", false, "Skip coding-agent wiring entirely")
	// Hidden, not removed: with no value to disambiguate it, --no-agent reads as "do not create an Orq Agent".
	f.BoolVar(&opts.noCodingAgents, "no-agent", false, "Deprecated alias for --no-coding-agents")
	_ = f.MarkHidden("no-agent")
	f.BoolVar(&opts.noMCP, "no-mcp", false, "Do not register the orq MCP server in agent configs")
	f.BoolVar(&opts.noGateway, "no-gateway", false, "Do not register the orq AI Gateway as a model provider in agent configs")
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked")
	f.BoolVar(&opts.local, "local", false, "Write agent config into this project even when inference would pick $HOME")
	cmd.AddCommand(newSetupCodingAgentsCommand())
	return cmd
}

// newSetupCodingAgentsCommand re-runs just the coding-agent wiring against an existing credential.
func newSetupCodingAgentsCommand() *cobra.Command {
	opts := setupOptions{}
	var gatewayOnly, mcpOnly bool

	cmd := &cobra.Command{
		Use:   "coding-agents",
		Short: "Wire coding agents to orq (gateway provider and MCP server)",
		Long: bartolocli.Markdown(`Registers orq with the coding agents on this machine: the AI Gateway ` +
			`as a model provider, and the orq.ai MCP server for workspace tools. Reuses the ` +
			`credential from a previous ` + "`orq setup`" + ` — it never creates keys or edits your shell.

Not to be confused with ` + "`orq agents`" + `, which manages Orq Agents on your workspace. ` +
			`Coding agents are the CLIs on this machine: ` + strings.Join(agentIDs(), ", ") + `.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if gatewayOnly && mcpOnly {
				return errors.New("--gateway and --mcp each narrow to one half; passing both leaves nothing to wire (omit them to wire both)")
			}
			opts.noMCP = gatewayOnly
			opts.noGateway = mcpOnly
			opts.narrowing = gatewayOnly || mcpOnly
			return runCodingAgents(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&opts.agents, "coding-agent", nil, "Coding agent to wire (repeatable): "+strings.Join(agentIDs(), ", "))
	f.StringSliceVar(&opts.agents, "agent", nil, "Deprecated alias for --coding-agent")
	_ = f.MarkHidden("agent")
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key instead of the one a previous setup saved")
	f.BoolVar(&gatewayOnly, "gateway", false, "Wire only the AI Gateway provider")
	f.BoolVar(&mcpOnly, "mcp", false, "Wire only the MCP server")
	f.BoolVar(&opts.global, "global", false, "Write agent config to the home directory instead of this project")
	f.BoolVar(&opts.local, "local", false, "Write agent config into this project even when inference would pick $HOME")
	return cmd
}

func runCodingAgents(cmd *cobra.Command, opts *setupOptions) error {
	if err := resolveScope(opts); err != nil {
		return err
	}

	rep := newReporter(opts.noInput)

	// Checked before authenticating: wiring needs a durable key (kimi embeds the literal value, session tokens expire within the hour) and this command never mints one.
	saved, savedWS := savedAPIKey()
	if saved == "" && strings.TrimSpace(opts.apiKey) == "" && strings.TrimSpace(os.Getenv("ORQ_API_KEY")) == "" {
		return errors.New("no saved API key — run 'orq setup' once to create one, or pass --api-key")
	}

	authState, err := resolveAuth(cmd.Context(), rep, opts)
	if err != nil {
		return err
	}
	// The saved key is workspace-scoped: refuse rather than wire every agent to the key's workspace. This command never mints.
	if active := activeWorkspaceKey(authState); saved != "" && keyWorkspaceMismatch(savedWS, active) {
		return fmt.Errorf("saved API key belongs to workspace %s, but the active workspace is %s — run 'orq setup --workspace %s' to create one for it", savedWS, active, active)
	}
	client := auth.NewClient(authState.apiBase)
	// Only fall back to the saved key when the user supplied none this run, or agents get the stale credential resolveAuth has already replaced.
	if saved != "" && authState.suppliedKey == "" {
		authState.bearer = saved
	}

	result := map[string]any{}
	agentResults, err := instrumentAgents(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["agents"] = agentResults

	if !wantsHumanView(cmd) {
		if err := emit(result); err != nil {
			return err
		}
	}
	for _, a := range agentResults {
		if a.Error != "" {
			return errAgentFailed
		}
	}
	return nil
}

// resolveScope settles the global/local decision once for every entry point, defaulting to $HOME outside a project.
func resolveScope(opts *setupOptions) error {
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
	if opts.global && opts.local {
		return errors.New("--global and --local are mutually exclusive")
	}
	if !opts.global && !opts.local && !looksLikeProject() {
		opts.global = true
	}
	return nil
}

func runSetup(cmd *cobra.Command, opts *setupOptions) error {
	// --no-input and --workspace are global flags (registerGlobalFlags), read from viper in resolveScope.
	if err := resolveScope(opts); err != nil {
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
		authState.bearer = mintedToken
	}

	rep.step(3, setupSteps, "Coding agents")
	agentResults, err := instrumentAgents(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["agents"] = agentResults
	result["gateway_funded"] = authState.gatewayFunding.String()
	if n, counted := enabledModelCount(); counted {
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
	result["setup_complete"] = verified

	printFinalScreen(rep, agentResults, links, client.RouterBaseURL(), verified, opts)

	if !wantsHumanView(cmd) {
		if err := emit(result); err != nil {
			return err
		}
	}
	for _, a := range agentResults {
		if a.Error != "" {
			return errAgentFailed
		}
	}
	if !verified {
		return errors.New("setup finished but the verification call failed")
	}
	return nil
}

var errAgentFailed = errors.New("one or more coding agents could not be configured")

// looksLikeProject reports whether to write the project-scoped MCP configs (.mcp.json, .kimi-code/mcp.json) here rather than $HOME.
func looksLikeProject() bool {
	for _, marker := range []string{".git", "package.json", "pyproject.toml", "go.mod", "Cargo.toml"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

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
	// gatewayFunding is what this run learned about the workspace's ability to pay for a model call.
	gatewayFunding fundingState
}

// Three-valued on purpose: the question is often never asked, and that is not "cannot pay".
type fundingState int

const (
	fundingUnknown fundingState = iota
	fundingOK
	fundingNone
)

// String is the --json spelling; a bool would make "never checked" read as "cannot pay".
func (f fundingState) String() string {
	switch f {
	case fundingOK:
		return "funded"
	case fundingNone:
		return "unfunded"
	default:
		return "unknown"
	}
}

func resolveAuth(ctx context.Context, rep *reporter, opts *setupOptions) (*authState, error) {
	// An explicit key wins; it carries no provenance, so it is saved with no workspace.
	if key := strings.TrimSpace(opts.apiKey); key != "" {
		if err := saveAPIKeyProfile(key, ""); err != nil {
			return nil, err
		}
		rep.ok("api key (profile: %s)", auth.ActiveProfile())
		return &authState{apiBase: apiBaseFromEnv(), bearer: key, suppliedKey: key}, nil
	}

	session, err := auth.ReadSession()
	if err != nil {
		return nil, err
	}

	// Bartolo auto-loads ./.env at startup, so "unset ORQ_API_KEY" is wrong advice when a file re-injects it every run.
	if envKey := strings.TrimSpace(os.Getenv("ORQ_API_KEY")); envKey != "" && session == nil {
		if file, v := dotEnvAPIKey(); file != "" && v == envKey {
			rep.ok("api key from ./%s", file)
			rep.note("orq loads ./%s automatically — remove its ORQ_API_KEY line to sign in instead; unsetting the shell variable is not enough.", file)
		} else {
			rep.ok("api key from ORQ_API_KEY")
			rep.note("credential order: login session → ORQ_API_KEY (env). No session found, so the environment key is used.")
		}
		return &authState{apiBase: apiBaseFromEnv(), bearer: envKey, suppliedKey: envKey}, nil
	}

	if session != nil && strings.TrimSpace(os.Getenv("ORQ_API_KEY")) != "" {
		// Opposite of 'orq launch' on purpose: setup persists what it resolves, so the workspace picked at login beats a stale exported key.
		rep.note("ignoring the exported ORQ_API_KEY — your login session wins here")
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

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return path
	}
	return "~" + path[len(home):]
}

// Agent configs reference ORQ_API_KEY instead of inlining it (kimi's rule for an mcp.json on disk), and nothing else exported it: agents came up with an empty bearer and every MCP call failed.
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
func saveAPIKeyProfile(key, workspace string) error {
	return writeAPIKeyProfile(auth.ActiveProfile(), key, workspace)
}

// clearAPIKeyProfile removes the stored key; without it logout leaves a live key in credentials.json.
func clearAPIKeyProfile() (bool, error) {
	profile := auth.ActiveProfile()
	if strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) == "" {
		return false, nil
	}
	return true, writeAPIKeyProfile(profile, "", "")
}

func savedAPIKey() (key, workspace string) {
	profile := bartolocli.GetProfile()
	return strings.TrimSpace(profile["api_key"]), strings.TrimSpace(profile["workspace"])
}

func activeWorkspaceKey(state *authState) string {
	if state.session != nil && state.session.ActiveWorkspaceKey != nil {
		return strings.TrimSpace(*state.session.ActiveWorkspaceKey)
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
	bartolocli.Creds.Set("profiles."+profile+".type", BartoloAuthType())
	bartolocli.Creds.Set("profiles."+profile+".api_key", key)
	bartolocli.Creds.Set("profiles."+profile+".workspace", workspace)
	filename := path.Join(viper.GetString("config-directory"), "credentials.json")
	if err := bartolocli.Creds.WriteConfigAs(filename); err != nil {
		return err
	}
	return os.Chmod(filename, 0o600)
}

// storedAPIKeyProfile reports whether the active profile holds a key; callers must not log it.
func storedAPIKeyProfile() bool {
	return strings.TrimSpace(bartolocli.Creds.GetString("profiles."+auth.ActiveProfile()+".api_key")) != ""
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

	// Reuse the saved key, but only for the workspace this run resolved: keys are workspace-scoped at mint time, and minting per run orphaned every copy of the old one.
	token, tokenWS := savedAPIKey()
	active := activeWorkspaceKey(state)
	switch {
	case token == "":
		// nothing saved — fall through to mint
	case keyWorkspaceMismatch(tokenWS, active):
		rep.note("saved key belongs to workspace %s — creating one for %s", tokenWS, active)
		token = ""
	case tokenWS == "":
		rep.ok("reusing the key from an earlier setup (workspace unrecorded)")
	default:
		rep.ok("reusing the key from an earlier setup")
	}
	if token == "" {
		if opts.interactive && !opts.confirm("Create a workspace API key now?", true) {
			rep.ok("skipped creating an API key")
			return info, "", nil
		}

		// Plain ASCII: the dashboard echoes the name back and we do not know its validation rules.
		hostname, _ := os.Hostname()
		hostname = strings.TrimSuffix(hostname, ".local")
		if hostname == "" {
			hostname = "unknown-host"
		}
		keyName := sanitizeKeyName("orq-cli " + hostname)

		// Without a user id the API mints against a service account, which only admins may create.
		userID := ""
		if state.session != nil && state.session.User != nil {
			userID = state.session.User.ID
		}
		minted, _, err := client.CreateAPIKey(state.bearer, keyName, "", userID)
		if err != nil {
			return nil, "", err
		}
		token = minted
		// The API returns the raw token once: persist before anything else can fail.
		if err := saveAPIKeyProfile(token, active); err != nil {
			return nil, "", fmt.Errorf("created a key but could not save it: %w", err)
		}
		info["created"] = true
		rep.ok("key saved   %s  (workspace-scoped)", tilde(filepath.Join(viper.GetString("config-directory"), "credentials.json")))
	}
	if path, err := writeShellEnvFile(token); err != nil {
		// Not fatal: the key is saved, and the final screen still shows how to export it.
		rep.warn("could not write the shell env file: %v", err)
	} else {
		rep.ok("            %s  → exports ORQ_API_KEY", tilde(path))
		offerProfileSourceLine(rep, opts)
	}

	return info, token, nil
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

// BYOK (providersPath) or credits, either one, lets the gateway serve a call; neither is needed for MCP.
const (
	modelsPath    = "/router/models"
	providersPath = "/router/providers"
	creditsPath   = "/admin/credits"
	// gatewayIntroPath needs no workspace key, so it doubles as the keyless answer.
	gatewayIntroPath = "/docs/ai-gateway/get-started/introduction"
)

// Session-only: /v2/credits needs credits.view, which an API key cannot carry (403). known=false means unanswered and must never be reported as "no credits".
func workspaceCanSpend(client *auth.Client, state *authState) (credits float64, known bool) {
	token := sessionWorkspaceToken(client, state)
	if token == "" {
		return 0, false
	}
	balance, err := client.Credits(token)
	if err != nil {
		return 0, false
	}
	return balance.Balance, true
}

// sessionWorkspaceToken returns a workspace-scoped session token, preferring the one already in memory.
func sessionWorkspaceToken(client *auth.Client, state *authState) string {
	// Without this, an --api-key run would refresh whatever session is on disk and read a different workspace's balance.
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

// No credit advice without a known web URL: self-hosted deployments are not metered (isAllowedToUseSystemDefaultKeys returns before credits) and have no dashboard to link to.
func canAdviseOnCredits(state *authState) bool {
	if state == nil {
		return false
	}
	return webBaseFor(state.apiBase) != ""
}

// The balance is a hint, not a verdict: the gateway also serves at zero credits via BYOK, private models, the recently-created flag, or shared keys.
func resolveGatewayFunding(client *auth.Client, state *authState) {
	if state == nil || state.gatewayFunding != fundingUnknown || !canAdviseOnCredits(state) {
		return
	}
	credits, known := workspaceCanSpend(client, state)
	if !known {
		return
	}
	if credits > 0 {
		state.gatewayFunding = fundingOK
		return
	}
	state.gatewayFunding = fundingNone
}

// Zero enabled models is a blocker and warns; a zero balance may block nothing at all, so it states a remedy and asks nothing.
// Both remedies, neither prescribed: enforce_enabled_models defaults to false and is not readable from this host. Warn rather than note, since notes are suppressed under --no-input.
func reportGatewayReadiness(rep *reporter, state *authState, opts *setupOptions, count int) {
	noModels := count == 0
	unfunded := state != nil && state.gatewayFunding == fundingNone

	models := workspaceLink(state, modelsPath)
	credits := workspaceLink(state, creditsPath)

	if !noModels {
		rep.ok("%d chat models available", count)
	} else {
		rep.warn("no models enabled for this workspace")
		if models != "" {
			rep.warn("    Enable models   %s", models)
		}
	}
	if unfunded {
		// Conditional, not a warning. A zero balance only blocks a call when the
		// subscription meters orq's managed keys, and then only for a public model
		// with no provider key connected. A workspace that never bought credits is
		// unmetered and unaffected. None of that is readable from here, and the
		// balance itself is doctor's to report.
		line := "if model calls get refused, add credits or connect a provider key"
		if credits != "" {
			line += " (" + credits + ")"
		}
		rep.info("%s", line)
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

// Derived from the catalogue, not GET /v2/integrations: that endpoint 403s for a workspace API key.
func connectedProviders(models []auth.RouterModel) []string {
	counts := map[string]int{}
	for _, m := range models {
		if m.Enabled && m.Type == "chat" && m.Provider != "" {
			counts[m.Provider]++
		}
	}
	out := make([]string, 0, len(counts))
	for p := range counts {
		out = append(out, p)
	}
	// Busiest first: only the head of this list gets shown.
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// listEnabledModels returns the enabled-model count and their providers, retrying because a freshly created key is not live yet.
func listEnabledModels(client *auth.Client, state *authState) (int, []string, error) {
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
		return 0, nil, err
	}
	// m.Enabled, not is_active: is_active covers the whole catalogue and reported hundreds of models on a workspace with no provider connected.
	enabled := 0
	for _, m := range models {
		if m.Enabled && m.Type == "chat" {
			enabled++
		}
	}
	return enabled, connectedProviders(models), nil
}

// Dashboard paths must be prefixed with the workspace key; unprefixed ones 404. Keyless runs get the docs page.
func workspaceURL(state *authState, path string) string {
	key := ""
	apiBase := ""
	if state != nil {
		apiBase = state.apiBase
		if state.session != nil && state.session.ActiveWorkspaceKey != nil {
			key = *state.session.ActiveWorkspaceKey
		}
	}
	return dashboardURL(apiBase, key, path)
}

// dashboardURL is the one place a settings link is built; doctor builds the same links through it.
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

// scopeNote tells a user who asked for project scope why this agent still reads only from $HOME.
func scopeNote(resolve func(bool) (string, error), askedGlobal bool) string {
	if askedGlobal || resolve == nil {
		return ""
	}
	project, perr := resolve(false)
	global, gerr := resolve(true)
	if perr != nil || gerr != nil || project != global {
		return ""
	}
	return "  (this agent reads it only from your home directory)"
}

func instrumentAgents(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) ([]agentResult, error) {
	if opts.noCodingAgents {
		rep.ok("skipped coding-agent wiring (--no-coding-agents)")
		return nil, nil
	}
	mcpURL := client.MCPServerURL()

	selected := opts.agents
	if len(selected) == 0 {
		if opts.noInput {
			rep.ok("no agent selected — pass --coding-agent to wire one")
			return nil, nil
		}
		var err error
		selected, err = promptForAgents(rep)
		if err != nil || len(selected) == 0 {
			return nil, nil
		}
	}

	// One consent question for both writes: they edit the user's own config files, and the MCP half grants agents workspace read/write.
	if !opts.noMCP && !opts.noGateway && !opts.yes && !opts.noInput {
		const (
			wireBoth    = "Gateway + MCP tools (recommended)"
			wireGateway = "Gateway only — route model calls through orq"
			wireMCP     = "MCP tools only — agents read/write your workspace"
			wireNothing = "Skip"
		)
		choice := wireBoth
		if err := survey.AskOne(&survey.Select{
			Message: "Wire your coding agents to orq?",
			Options: []string{wireBoth, wireGateway, wireMCP, wireNothing},
			Default: wireBoth,
		}, &choice, promptStdio()); err != nil {
			// An abort is "no": failing open here wired exactly what the user backed out of.
			return nil, fmt.Errorf("setup cancelled at the wiring question: %w", err)
		}
		switch choice {
		case wireGateway:
			opts.noMCP = true
		case wireMCP:
			opts.noGateway = true
		case wireNothing:
			opts.noMCP, opts.noGateway = true, true
		}
	}
	if opts.noMCP && opts.noGateway {
		rep.ok("skipped coding-agent wiring (nothing selected)")
		return nil, nil
	}

	if !opts.noGateway {
		if count, _, err := listEnabledModels(client, state); err != nil {
			rep.warn("could not list gateway models: %v", err)
		} else {
			rememberEnabledModelCount(count)
			resolveGatewayFunding(client, state)
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

		// A gateway-only agent leaves mcpConfig nil (pi has no native MCP) and
		// falls through to the "nothing to configure" branch below.
		var configPath string
		var err error
		if spec.mcpConfig != nil {
			configPath, err = spec.mcpConfig(opts.global)
		}
		switch {
		case opts.noMCP:
		case err != nil:
			rep.fail("%-8s %v", id, err)
			res.Error = err.Error()
		case configPath == "":
			rep.note("%-8s no MCP support in this agent — nothing to configure", id)
		default:
			if err := spec.writeMCP(configPath, mcpURL); err != nil {
				rep.fail("%-8s %v", id, err)
				rep.blank()
				rep.note("Add this manually to %s:", tilde(configPath))
				for _, line := range strings.Split(spec.manualSnippet(mcpURL), "\n") {
					rep.note("    %s", line)
				}
				res.Error = err.Error()
			} else {
				res.MCP = configPath
			}
		}

		switch {
		case opts.noGateway && spec.writeProvider != nil:
		case spec.writeProvider != nil:
			if path, perr := spec.providerConfig(opts.global); perr == nil && path == "" {
				// No resolver returns ("", nil) today; silence here would read as success for a wire that never happened.
				rep.fail("%-8s provider  no config path for this scope", id)
				res.Error = "provider config path resolved empty"
			} else if perr == nil && path != "" {
				models := codingModels(rep, client, state)
				// Must match the [models."<key>"] form the writer emits: the full provider/model ref.
				defaultModel := ""
				if best, ok := defaultCodingModel(rep, client, state); ok {
					defaultModel = best.Ref()
				}
				// Every writer clears its own keys before emitting, so writing an empty catalogue would delete a working provider block from an earlier run.
				if len(models) == 0 {
					// Say which of the two states this is: an earlier run's block
					// still wires the agent, an absent one leaves it unwired.
					if spec.providerPresent != nil && spec.providerPresent(path) {
						rep.warn("%-8s provider  no models to offer; kept the orq provider already in %s", id, tilde(path))
					} else {
						rep.warn("%-8s provider  no models to offer; nothing written to %s", id, tilde(path))
					}
					results = append(results, res)
					continue
				}
				// An empty defaultModel is fine: codex omits the model key, kimi omits default_model.
				written, werr := spec.writeProvider(path, client.RouterBaseURL(), state.bearer, models, defaultModel)
				switch {
				case werr != nil:
					// A wire the user consented to and did not get must reach the exit code and the JSON, not evaporate as a warning.
					rep.fail("%-8s provider  %v", id, werr)
					res.Error = werr.Error()
				default:
					res.Provider = path
					res.ModelCount = written
				}
			} else if perr != nil {
				rep.fail("%-8s provider  %v", id, perr)
				res.Error = perr.Error()
			}
		}

		reportAgent(rep, spec, res, opts)
		results = append(results, res)
	}
	return results, nil
}

func reportAgent(rep *reporter, spec agentSpec, res agentResult, opts *setupOptions) {
	wired := []string{}
	if res.MCP != "" {
		wired = append(wired, "MCP")
	}
	if res.Provider != "" {
		gateway := "gateway"
		// codex's profile names one default and takes its list from elsewhere, so only claim a count when there is one.
		if res.ModelCount > 0 {
			gateway = fmt.Sprintf("gateway (%d models)", res.ModelCount)
		}
		wired = append(wired, gateway)
	}
	if len(wired) == 0 {
		return
	}

	where := tilde(res.MCP)
	switch {
	case res.MCP == "":
		where = tilde(res.Provider)
	case res.Provider != "" && filepath.Dir(res.MCP) == filepath.Dir(res.Provider):
		where = tilde(filepath.Dir(res.MCP)) + string(filepath.Separator)
	case res.Provider != "":
		where = tilde(res.MCP) + " · " + tilde(res.Provider)
	}

	scope := ""
	if res.MCP != "" {
		scope = scopeNote(spec.mcpConfig, opts.global)
	} else if res.Provider != "" {
		scope = scopeNote(spec.providerConfig, opts.global)
	}
	rep.ok("%-8s %-24s %s%s", spec.ID, strings.Join(wired, " + "), where, scope)

	// orq is registered as an option, not the default, so some agents need a step the user would never find.
	if res.Provider != "" && spec.providerUsage != "" {
		rep.note("         %s", spec.providerUsage)
	}
}

// codingModels fetches the gateway catalogue once per run: the enabled chat models with function calling, the same pool 'orq launch' writes.
var cachedCodingModels []auth.RouterModel
var codingModelsFetched bool

// enabledModels is the chat-model count and whether anyone counted: only the gateway branch counts, so unknown is not zero.
var enabledModels int
var enabledModelsCounted bool

func rememberEnabledModelCount(n int) { enabledModels, enabledModelsCounted = n, true }
func enabledModelCount() (int, bool)  { return enabledModels, enabledModelsCounted }

func codingModels(rep *reporter, client *auth.Client, state *authState) []auth.RouterModel {
	if codingModelsFetched {
		return cachedCodingModels
	}
	codingModelsFetched = true

	all, err := client.ListModels(state.bearer)
	if err != nil {
		rep.warn("could not list gateway models: %v", err)
		return nil
	}
	for _, m := range all {
		if m.Enabled && m.Type == "chat" && m.Functions {
			cachedCodingModels = append(cachedCodingModels, m)
		}
	}
	return cachedCodingModels
}

// defaultCodingModel picks by preference order, then anything enabled. No probe: it would bill a completion, and a tools-less probe passed models that later failed.
func defaultCodingModel(rep *reporter, client *auth.Client, state *authState) (auth.RouterModel, bool) {
	models := codingModels(rep, client, state)
	if len(models) == 0 {
		return auth.RouterModel{}, false
	}

	for _, group := range auth.CandidateCodingModels(models, preferredCodingModels) {
		if len(group) > 0 {
			return group[0], true
		}
	}
	// No preferred family: writers omit the key and the agent falls back to a bundled id the gateway cannot address.
	return models[0], true
}

func promptForAgents(rep *reporter) ([]string, error) {
	registry := agentRegistry()
	options := make([]string, 0, len(registry))
	byOption := map[string]string{}
	defaults := []string{}

	detected := []string{}
	for _, spec := range registry {
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
func webBaseURL(state *authState) string {
	if state == nil {
		return webBaseFor("")
	}
	return webBaseFor(state.apiBase)
}

// webBaseFor is webBaseURL without an authState, so doctor can build the same links.
func webBaseFor(apiBase string) string {
	webBase := strings.TrimRight(os.Getenv("ORQ_WEB_BASE_URL"), "/")
	if webBase == "" && apiBase == auth.DefaultAPIBaseURL {
		webBase = defaultWebBaseURL
	}
	return webBase
}

func buildLinks(state *authState) map[string]string {
	links := map[string]string{"docs": docsURL}
	webBase := webBaseURL(state)
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

	// Classified per agent: only MCP-wired agents may be told they can read and write the workspace.
	mcpWired := []string{}
	gatewayOnly := []string{}
	// starts are the agents' own commands, never 'orq launch': launch builds a throwaway home and would not exercise what setup wrote.
	starts := []string{}
	for _, a := range agents {
		if a.Error != "" {
			continue
		}
		spec, ok := lookupAgent(a.Agent)
		if !ok {
			continue
		}
		switch {
		case a.MCP != "":
			mcpWired = append(mcpWired, spec.Label)
		case a.Provider != "":
			gatewayOnly = append(gatewayOnly, spec.Label)
		default:
			continue
		}
		starts = append(starts, spec.startCommand())
	}
	switch {
	case len(mcpWired)+len(gatewayOnly) > 0:
		if len(mcpWired) > 0 {
			fmt.Fprintf(w, "  %s can now read and write your orq.ai workspace.\n", strings.Join(mcpWired, " and "))
		}
		if len(gatewayOnly) > 0 {
			fmt.Fprintf(w, "  %s %s model calls through orq.\n",
				strings.Join(gatewayOnly, " and "), pluralize(len(gatewayOnly), "routes its", "route their"))
		}
	default:
		fmt.Fprintln(w, "  Route an existing OpenAI client through the gateway:")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "      client = OpenAI(api_key=os.environ[\"ORQ_API_KEY\"],\n"+
			"                      base_url=\"%s\")\n", routerBase)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Or start a coding agent:      orq launch %s\n", detectedAgentID())
		fmt.Fprintln(w, "  Or wire one durably:          orq setup coding-agents")
	}
	// MCP entries reference ORQ_API_KEY too; checking only Provider left kimi with a dead MCP server and no warning.
	keyReferenced := false
	for _, a := range agents {
		if a.Error == "" && (a.Provider != "" || a.MCP != "") {
			keyReferenced = true
		}
	}
	// The env var is empty in this shell even on a wired machine, so the branches below consult the profile: re-printing the append line stacks duplicates.
	if keyReferenced && strings.TrimSpace(os.Getenv("ORQ_API_KEY")) == "" {
		sh := detectShell(viper.GetString("config-directory"))
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s %s\n", paint(ansiWarn, "!"),
			"ORQ_API_KEY is not exported here, and agents inherit it from this shell.")
		switch {
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
		label := pluralize(len(starts), "Start", "Start")
		for i, cmd := range starts {
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(w, "  %s %s\n", padLabel(label), cmd)
		}
		if len(mcpWired) > 0 {
			fmt.Fprintf(w, "  %s %s\n", padLabel("Try"), `"list my orq.ai agents"`)
			// Suggested, not run: the installer detects the agent and writes
			// into the user's own config, which is their call to make.
			fmt.Fprintf(w, "  %s %s\n", padLabel("Skills"), "npx skills add "+skillsRepo)
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
	pad := labelWidth - len([]rune(s))
	if pad < 0 {
		pad = 0
	}
	if s == "" {
		return strings.Repeat(" ", labelWidth)
	}
	return paint(ansiDim, s) + strings.Repeat(" ", pad)
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
