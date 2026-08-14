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
	defaultWebBaseURL = "https://my.orq.ai"
	docsURL           = "https://docs.orq.ai"
	setupSteps        = 4
)

type setupOptions struct {
	interactive bool
	workspace   string
	apiKey      string
	agents      []string
	global      bool
	local       bool
	noAgent     bool
	noMCP       bool
	noGateway   bool
	noEnv       bool
	noInput     bool
	yes         bool
}

// confirm asks a yes/no question, honouring the two ways a user can answer it
// up front. clig.dev: "Confirm before doing anything dangerous … prompt for the
// user to type y or yes if running interactively", and "if --no-input is
// passed, don't prompt or do anything interactive".
//
// --yes takes the affirmative without asking; --no-input (or no TTY) declines
// to guess and takes the default, because a prompt that cannot be shown must
// not block a script.
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
		// A failure here is a runtime problem, not a usage problem; dumping the
		// flag list on top of the error just buries it.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Ask about every choice instead of inferring")
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key instead of logging in and creating one")
	f.StringSliceVar(&opts.agents, "agent", nil, "Coding agent to instrument (repeatable): "+strings.Join(agentIDs(), ", "))
	f.BoolVar(&opts.global, "global", false, "Write agent config to the home directory instead of this project")
	f.BoolVar(&opts.noAgent, "no-agent", false, "Skip coding-agent instrumentation")
	f.BoolVar(&opts.noMCP, "no-mcp", false, "Do not register the orq MCP server in agent configs")
	f.BoolVar(&opts.noGateway, "no-gateway", false, "Do not register the orq AI Gateway as a model provider in agent configs")
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked")
	f.BoolVar(&opts.noEnv, "no-env", false, "Do not write ORQ_API_KEY to ./.env")
	f.BoolVar(&opts.local, "local", false, "Write agent config into this project even when inference would pick $HOME")
	cmd.AddCommand(newSetupCodingAgentsCommand())
	return cmd
}

// newSetupCodingAgentsCommand re-runs just the coding-agent wiring against an
// existing credential — the thing you want after installing a new agent,
// without re-walking auth and key creation.
//
// Named coding-agents, never agents: `orq agents` is the platform Agents
// product, and one word with two meanings in the same surface was the
// confusion to avoid (decided 2026-08-14, RES-1270).
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
			// The narrowing flags pre-answer the wiring question; neither means
			// both, which is also what the question defaults to.
			opts.noMCP = gatewayOnly
			opts.noGateway = mcpOnly
			return runCodingAgents(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&opts.agents, "agent", nil, "Coding agent to wire (repeatable): "+strings.Join(agentIDs(), ", "))
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

	authState, err := resolveAuth(cmd.Context(), rep, opts)
	if err != nil {
		return err
	}
	client := auth.NewClient(authState.apiBase)

	// The wiring needs a durable credential: agent configs reference
	// ORQ_API_KEY, and kimi embeds the literal key. A login session alone is
	// not enough — its workspace tokens expire within the hour.
	if key := strings.TrimSpace(bartolocli.GetProfile()["api_key"]); key != "" {
		authState.bearer = key
	} else if authState.suppliedKey == "" {
		return errors.New("no saved API key — run 'orq setup' once to create one")
	}

	result := map[string]any{}
	agentResults := instrumentAgents(rep, client, authState, opts)
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

// resolveScope settles the global/local decision once for every entry point:
// explicit flags win (and conflict loudly), inference falls back to $HOME so a
// home-directory run does not scatter project files there.
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
	// --no-input and --workspace are global flags (see registerGlobalFlags),
	// read from viper inside resolveScope rather than re-declared here.
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

	// No project step. Keys are workspace-scoped (the key API accepts a
	// different id format than /v2/projects returns, so project scoping never
	// actually happened), and free-tier accounts cannot create projects at
	// all. Project is asked where scope genuinely matters — creating agents
	// and deployments — never here. Decided 2026-08-14; see RES-1270.

	// --- Step 2: API key -----------------------------------------------------
	rep.step(2, setupSteps, "API key")
	keyInfo, mintedToken, err := resolveAPIKey(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["api_key"] = keyInfo
	// Verify with the credential the agents will actually use, not the session
	// token that happened to authenticate this run.
	if mintedToken != "" {
		authState.bearer = mintedToken
	}

	// --- Step 3: providers ---------------------------------------------------
	// The gateway routes to whatever the workspace has connected (BYOK). With
	// nothing connected there are no models, and every step after this one
	// degrades into a confusing "no models" instead of "connect a provider".
	rep.step(3, setupSteps, "Providers")
	result["models_enabled"] = resolveProviders(rep, client, authState, opts)

	// --- Step 4: coding agents ----------------------------------------------
	rep.step(4, setupSteps, "Coding agent")
	agentResults := instrumentAgents(rep, client, authState, opts)
	result["agents"] = agentResults

	// --- Verify --------------------------------------------------------------
	rep.note("")
	rep.note("Verifying…")
	verified := verifySetup(rep, client, authState)
	result["verified"] = verified
	// A failed gateway call is reported but does not fail setup: everything else
	// (MCP, API key) still works without a connected provider.
	result["gateway_verified"] = verifyGateway(rep, client, authState)

	links := buildLinks(authState)
	if len(links) > 0 {
		result["links"] = links
	}
	result["setup_complete"] = verified

	printFinalScreen(rep, agentResults, links, client.RouterBaseURL(), verified, opts)

	// Same human/machine split as login and whoami: a person at a terminal
	// gets the final screen only — dumping the structured payload after it
	// buries the summary they just read. Scripts (non-TTY) and --json/-o
	// still get the payload.
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

// looksLikeProject reports whether the working directory is somewhere it makes
// sense to drop .mcp.json, .env and .agents/.
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
	// suppliedKey is set when the user brought their own key, in which case we
	// never mint one.
	suppliedKey string
}

func resolveAuth(ctx context.Context, rep *reporter, opts *setupOptions) (*authState, error) {
	// An explicit key wins over everything.
	if key := strings.TrimSpace(opts.apiKey); key != "" {
		if err := saveAPIKeyProfile(key); err != nil {
			return nil, err
		}
		rep.ok("api key (profile: %s)", auth.ActiveProfile())
		return &authState{apiBase: apiBaseFromEnv(), bearer: key, suppliedKey: key}, nil
	}

	session, err := auth.ReadSession()
	if err != nil {
		return nil, err
	}

	// An environment key is usable as-is; do not persist or replace it. Name
	// the real source: bartolo auto-loads ./.env at startup, so "unset it" is
	// wrong (and provably followed-then-failed) advice when the key comes from
	// a file that re-injects it on every run.
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
		// Deliberately the opposite of `orq launch`, which lets an explicit
		// ORQ_API_KEY win. Setup persists what it resolves, and it is the
		// command a user runs right after choosing a workspace in `orq auth
		// login`; letting a stale exported key silently overwrite that choice
		// wires the agent to the wrong workspace and leaves it that way.
		rep.note("credential order: login session → ORQ_API_KEY (env). Using your login session; ORQ_API_KEY is ignored here (orq launch prefers it).")
	}

	if session == nil {
		if opts.noInput {
			return nil, errors.New("no TTY available for browser login\n  Pass --api-key <key> or set ORQ_API_KEY, then re-run")
		}
		session, err = deviceLogin(ctx, rep, opts)
		if err != nil {
			return nil, err
		}
	} else {
		email := "current user"
		if session.User != nil && session.User.Email != "" {
			email = session.User.Email
		}
		rep.ok("already signed in as %s  (use 'orq auth logout' to switch)", email)
	}

	client := auth.NewClient(session.APIBaseURL)
	session, err = resolveWorkspace(rep, client, session, opts)
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

// deviceLoginResult carries the session plus the activation details a machine
// consumer may need to relay (auth login emits them in its JSON payload).
type deviceLoginResult struct {
	Session         *auth.Session
	VerificationURI string
	UserCode        string
	BrowserOpened   bool
}

// runDeviceLogin is the one device-login flow behind `orq auth login`,
// `orq setup`, and launch's inline login offer. Success reporting is left to
// the callers — each has its own idea of what "signed in" looks like.
func runDeviceLogin(ctx context.Context, rep *reporter, apiBase, workspace string, openBrowser bool) (*deviceLoginResult, error) {
	// Context-aware so Ctrl-C during the approval poll cancels instead of
	// waiting out the device-code expiry.
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

func resolveWorkspace(rep *reporter, client *auth.Client, session *auth.Session, opts *setupOptions) (*auth.Session, error) {
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
	rep.ok("workspace %s", key)
	return updated, nil
}

// shellSetup describes how to give the user's shell the key: which file to
// source, which profile to source it from, and how that profile is worded.
type shellSetup struct {
	EnvFile string // file orq writes, holding the export
	Profile string // user's profile file, "" when the shell is unrecognised
	Line    string // line to add to that profile
}

// detectShell resolves the above from $SHELL, mirroring install.sh's
// profile_for_shell so the installer and the CLI agree on where things go.
//
// zsh differs from install.sh on purpose: that only needs PATH, which
// interactive shells set up, so .zshrc is enough. A key read by an agent the
// user may start from a launcher, an IDE or a login shell has to be in
// .zshenv — zsh reads .zshrc only for interactive shells.
//
// fish gets its own file because it cannot parse `export VAR=value`.
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
	// Set regardless of whether the shell was recognised: an unknown shell
	// still needs something to run, it just has no profile file to name.
	posix.Line = ". " + posix.EnvFile
	return posix
}

// writeShellEnvFile writes a sourceable snippet exporting ORQ_API_KEY next to
// credentials.json, in the syntax of the user's shell, and returns its path.
//
// Agent configs reference the key by env var rather than inlining it — kimi's
// own guidance is that an mcp.json is "a plain config file on disk", so http
// servers should use bearerTokenEnvVar. That only works if something actually
// puts the key in the environment, which nothing did: agents came up with an
// empty bearer token and every MCP call failed to authenticate.
//
// A file the user sources beats printing the key on screen (it stays out of
// scrollback and shell history) and beats editing their shell profile for them.
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

// offerProfileSourceLine asks the user whether setup may add the source line
// for the env file to their shell profile, so agents launched from any future
// shell see ORQ_API_KEY without manual steps. Editing a profile is the user's
// call: it only happens on an explicit yes, and never under --no-input.
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
	if !opts.confirm(fmt.Sprintf("Add '%s' to %s so agents always see ORQ_API_KEY?", sh.Line, sh.Profile), true) {
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
	rep.ok("updated     %s  → sources %s", sh.Profile, sh.EnvFile)
	rep.note("• takes effect in new shells; run '%s' for this one", sh.Line)
}

// profileSourcesEnvFile reports whether the profile already references the env
// file, however the user phrased it — re-appending would stack duplicates.
// Matching on the home-relative suffix also catches "$HOME/.orq/env" and
// "~/.orq/env" spellings, which an absolute-path comparison missed.
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

// saveAPIKeyProfile mirrors bartolo's own saveAuthProfile, then tightens the
// permissions: viper writes 0644 and this file holds a live credential.
func saveAPIKeyProfile(key string) error {
	return writeAPIKeyProfile(auth.ActiveProfile(), key)
}

// clearAPIKeyProfile removes the stored key so logout actually logs the user
// out. Without this the session file goes but credentials.json keeps a live
// key, and every generated command stays authenticated.
func clearAPIKeyProfile() (bool, error) {
	profile := auth.ActiveProfile()
	if strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) == "" {
		return false, nil
	}
	return true, writeAPIKeyProfile(profile, "")
}

// BartoloAuthType returns the profile "type" that bartolo can resolve back to
// an auth handler.
//
// The generated client registers its bearer handler anonymously
// (apikey.InitBearer -> cli.UseAuth("", handler)), and resolveAuthHandler looks
// a profile's type up verbatim, falling back to the sole handler only when the
// type is empty. Writing a descriptive "apikey" therefore produced a profile no
// handler could serve, and every generated command — orq agents list, orq
// deployments, all of them — failed with "no authentication handler
// configured".
//
// The name is read back from the registry rather than hardcoded so that a
// future bartolo registering its handler under a real type name keeps working.
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

func writeAPIKeyProfile(profile, key string) error {
	bartolocli.Creds.Set("profiles."+profile+".type", BartoloAuthType())
	bartolocli.Creds.Set("profiles."+profile+".api_key", key)
	filename := path.Join(viper.GetString("config-directory"), "credentials.json")
	if err := bartolocli.Creds.WriteConfigAs(filename); err != nil {
		return err
	}
	return os.Chmod(filename, 0o600)
}

// storedAPIKeyProfile reports whether credentials.json holds a key for the
// active profile. Callers must not log the key itself.
func storedAPIKeyProfile() bool {
	return strings.TrimSpace(bartolocli.Creds.GetString("profiles."+auth.ActiveProfile()+".api_key")) != ""
}

// dotEnvAPIKey returns the first local dotenv file that sets ORQ_API_KEY and
// the value it carries, or "" when none does. Bartolo loads these files into
// the environment at startup (before any command runs), so a key here outlives
// both `unset ORQ_API_KEY` and `orq auth logout` — the parsing mirrors
// bartolo's loadDotEnvFile so the answer matches what actually got loaded.
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
			// A placeholder line (ORQ_API_KEY=) carries no credential: bartolo
			// loads the empty value, which authenticates nothing and does not
			// block a later file from supplying the real key. Reporting it
			// would warn about a key that does not exist and hide the file
			// that actually holds one.
			if v == "" {
				continue
			}
			return name, v
		}
	}
	return "", ""
}

// warnLingeringAPIKeys points out credentials logout cannot clear: a dotenv
// file the CLI auto-loads on every run, or a key exported by the shell.
// Without this the user sees "signed out" while the very next command silently
// authenticates again.
func warnLingeringAPIKeys() {
	file, v := dotEnvAPIKey()
	if file != "" {
		Warn("./%s still sets ORQ_API_KEY and orq loads it automatically — remove that line to fully sign out", file)
	}
	// The two sources are independent: a shell export survives removing the
	// .env line (bartolo's loader skips vars the shell already set), so an
	// early return here would hide the export behind the file warning.
	//
	// explicitAPIKey guards against crying wolf on the session token our own
	// PreRun injects into ORQ_API_KEY; it is snapshotted before the injection.
	// The value comparison keeps a dotenv-loaded key from also reading as a
	// shell export — when the shell exports the very value the file carries,
	// the sources are indistinguishable and the file warning has to do.
	if explicitAPIKey && envAPIKeySet() && (file == "" || v != strings.TrimSpace(os.Getenv("ORQ_API_KEY"))) {
		Warn("ORQ_API_KEY is still exported in this shell — logout cannot unset it; run: unset ORQ_API_KEY")
	}
}

// envAPIKeySet reports whether an API key is present in the environment. Never
// report which value — only that one is set.
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

// resolveAPIKey returns the summary for the emitted payload and, when it minted
// one, the raw token so the caller can verify with it.
func resolveAPIKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (map[string]any, string, error) {
	info := map[string]any{"created": false, "profile": auth.ActiveProfile()}

	if state.suppliedKey != "" {
		rep.ok("key already configured (profile: %s) — skipping key creation", auth.ActiveProfile())
		return info, "", nil
	}

	// A key from an earlier run is reused, not replaced. Minting per run left
	// a trail of live keys in the dashboard and made every re-run disagree
	// with the ./.env written by the previous one. The env-file tail below
	// still runs on reuse: a fresh project directory needs its ./.env even
	// when the key itself is old.
	token := strings.TrimSpace(bartolocli.GetProfile()["api_key"])
	if token != "" {
		rep.ok("key already saved (profile: %s) — reusing it", auth.ActiveProfile())
	} else {
		if opts.interactive && !opts.confirm("Create a workspace API key now?", true) {
			rep.ok("skipped creating an API key")
			return info, "", nil
		}

		// Plain ASCII and no punctuation beyond spaces and hyphens: the name is
		// echoed back by the dashboard and we do not know its validation rules.
		hostname, _ := os.Hostname()
		hostname = strings.TrimSuffix(hostname, ".local")
		if hostname == "" {
			hostname = "unknown-host"
		}
		keyName := sanitizeKeyName("orq-cli " + hostname)

		minted, _, err := client.CreateAPIKey(state.bearer, keyName, "")
		if err != nil {
			return nil, "", err
		}
		token = minted
		// The raw token is returned once. Persist before doing anything else so
		// a later failure cannot leave a live key with no local record of it.
		if err := saveAPIKeyProfile(token); err != nil {
			return nil, "", fmt.Errorf("created a key but could not save it: %w", err)
		}
		info["created"] = true
		// Stated as fact, not apology: workspace scope is what the key API mints.
		rep.ok("created key  %s  (workspace-scoped)", maskToken(token))
		rep.ok("saved       %s", filepath.Join(viper.GetString("config-directory"), "credentials.json"))
	}
	if path, err := writeShellEnvFile(token); err != nil {
		// Not fatal: the key is already saved, and the final screen still tells
		// the user how to export it.
		rep.warn("could not write the shell env file: %v", err)
	} else {
		rep.ok("saved       %s  → source it to export ORQ_API_KEY", path)
		offerProfileSourceLine(rep, opts)
	}

	if opts.noEnv || opts.global {
		return info, token, nil
	}
	if opts.interactive && !opts.confirm("Write ORQ_API_KEY to ./.env?", true) {
		return info, token, nil
	}
	if err := appendEnvKey(rep, token); err != nil {
		rep.warn("could not write ./.env: %v", err)
		return info, token, nil
	}
	info["env_file"] = "./.env"
	return info, token, nil
}

// sanitizeKeyName keeps letters, digits, spaces and hyphens, collapses runs of
// whitespace, and caps the length. Project names are user-supplied and can
// contain anything.
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

// appendEnvKey adds ORQ_API_KEY to ./.env, leaving an existing entry alone, and
// warns when the file is not ignored by git.
func appendEnvKey(rep *reporter, token string) error {
	const envFile = ".env"
	existing, err := os.ReadFile(envFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "ORQ_API_KEY=") {
			continue
		}
		rep.ok("./.env already sets ORQ_API_KEY — left as is")
		// The key there may be one we minted on an earlier run and have since
		// replaced; anything reading .env would then authenticate with a dead
		// credential. We do not overwrite it — it may equally be the user's own.
		if strings.TrimSpace(line) != "ORQ_API_KEY="+token {
			rep.warn("  it differs from the key just created — update it by hand if agents get 401s")
		}
		return nil
	}
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(prefix + "ORQ_API_KEY=" + token + "\n"); err != nil {
		return err
	}
	rep.ok("wrote       ./.env  → ORQ_API_KEY")
	// The write has a side effect worth stating: orq itself auto-loads ./.env,
	// and that key takes precedence over a login session in this directory.
	rep.note("• orq loads ./.env automatically — commands run here authenticate with this key, even after 'orq auth logout'")
	if envIsGitIgnored() {
		rep.note("• .env is covered by .gitignore")
	} else {
		rep.warn("./.env is NOT covered by .gitignore — do not commit it")
	}
	return nil
}

func envIsGitIgnored() bool {
	data, err := os.ReadFile(".gitignore")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case ".env", "/.env", "*.env", ".env*":
			return true
		}
	}
	return false
}

// ============================================================================
// Step 4 — providers
// ============================================================================

// modelsSettingsPath is where a workspace connects providers (BYOK).
const modelsSettingsPath = "/settings/models"

// resolveProviders reports how many models the gateway can route to, and walks
// the user through connecting a provider when the answer is none. Connecting is
// a browser flow (provider secrets never touch the CLI), so all this can do is
// detect, deep-link, and re-check.
func resolveProviders(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) int {
	count, providers, err := listEnabledModels(client, state)
	if err != nil {
		rep.warn("could not list gateway models: %v", err)
		return 0
	}
	if count > 0 {
		reportProviders(rep, count, providers)
		return count
	}

	// The link goes in the warning, not a note: notes are suppressed in quiet
	// mode, which would leave a --no-input user with a problem and no next step.
	link := modelsSettingsURL(state)
	rep.warn("no models enabled — connect a provider (BYOK) at %s", link)
	if opts.noInput {
		rep.warn("  then re-run 'orq setup'")
		return 0
	}

	var retry bool
	prompt := &survey.Confirm{Message: "Connected a provider? Check again", Default: true}
	if err := survey.AskOne(prompt, &retry); err != nil || !retry {
		return 0
	}
	count, providers, err = listEnabledModels(client, state)
	if err != nil {
		rep.warn("could not list gateway models: %v", err)
		return 0
	}
	if count == 0 {
		rep.warn("still no models enabled — continuing, but agents will have none to use")
		return 0
	}
	reportProviders(rep, count, providers)
	return count
}

// reportProviders says what the step actually verified — that BYOK providers
// are connected and reachable. The bare catalogue count was a number nobody
// could act on: it is the whole workspace, while setup goes on to write a
// handful of models into one agent's config.
func reportProviders(rep *reporter, count int, providers []string) {
	const show = 6
	listed := providers
	suffix := ""
	if len(listed) > show {
		listed, suffix = listed[:show], fmt.Sprintf(" +%d more", len(providers)-show)
	}
	rep.ok("providers connected: %s%s", strings.Join(listed, ", "), suffix)
	rep.note("  %d chat model(s) enabled in this workspace", count)
}

// connectedProviders summarises which BYOK providers actually have usable
// models, derived from the catalogue rather than GET /v2/integrations: that
// endpoint is behind a role permission a workspace API key does not carry (403),
// and setup may be running on `--api-key` with no session at all. What matters
// here is "can an agent call something", which enabled models answer directly.
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
	// Busiest first: the head of this list is what gets shown, and an
	// alphabetical head buries the providers the user actually routes through.
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// listEnabledModels returns the enabled-model count and the providers behind
// them, retrying because a freshly created key is not always live yet.
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
	// Enabled chat models: is_active covers the entire catalogue, so counting it
	// reported hundreds of models on a workspace with no provider connected and
	// the "connect a provider" branch below could never fire.
	enabled := 0
	for _, m := range models {
		if m.Enabled && m.Type == "chat" {
			enabled++
		}
	}
	return enabled, connectedProviders(models), nil
}

func modelsSettingsURL(state *authState) string {
	if base := webBaseURL(state); base != "" {
		return base + modelsSettingsPath
	}
	return docsURL + "/docs/ai-gateway/get-started/introduction"
}

// ============================================================================
// Step 5 — coding agents
// ============================================================================

// scopeNote marks a path the agent will only ever read from the home directory,
// so a user who asked for project scope is told why they did not get it.
//
// Derived by asking the resolver both ways rather than listing agent ids: the
// resolver is what decides, three of the five agents are now home-only, and a
// hardcoded list is exactly the kind of thing that goes stale the next time one
// is added. Silent when the user asked for global anyway — then there is no
// discrepancy to explain, only noise.
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

func instrumentAgents(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) []agentResult {
	if opts.noAgent {
		rep.ok("skipped coding-agent setup (--no-agent)")
		return nil
	}
	mcpURL := client.MCPServerURL()

	selected := opts.agents
	if len(selected) == 0 {
		if opts.noInput {
			rep.ok("no agent selected — pass --agent to instrument one")
			return nil
		}
		var err error
		selected, err = promptForAgents(rep)
		if err != nil || len(selected) == 0 {
			return nil
		}
	}

	// One question covers both kinds of write, asked on the default path and
	// always — even with a single detected agent — because this edits config
	// files the user owns and the MCP half grants agents read/write access to
	// their workspace. Asked once rather than per agent: the grant is the same
	// for all of them. Decided 2026-08-14 (RES-1270); it also closes the old
	// asymmetry where MCP was gated and provider writes were not.
	//
	// Flags pre-answer it, so scripts never see a prompt: --no-mcp → gateway
	// only, --no-gateway → MCP only, both → nothing to wire. --yes and
	// --no-input take the default, which is both — unchanged from what a
	// non-interactive run already did.
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
		}, &choice, promptStdio()); err == nil {
			switch choice {
			case wireGateway:
				opts.noMCP = true
			case wireMCP:
				opts.noGateway = true
			case wireNothing:
				opts.noMCP, opts.noGateway = true, true
			}
		}
	}
	if opts.noMCP && opts.noGateway {
		rep.ok("skipped coding-agent wiring (nothing selected)")
		return nil
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

		// MCP registration. Skipping it still leaves the provider config, which
		// is the coherent "route my calls through orq, but do not give the
		// agent workspace read/write" case.
		configPath, err := spec.mcpConfig(opts.global)
		switch {
		case opts.noMCP:
			rep.note("%-8s MCP       skipped (--no-mcp)", id)
		case err != nil:
			rep.fail("%-8s %v", id, err)
			res.Error = err.Error()
		case configPath == "":
			rep.note("%-8s no MCP support in this agent — nothing to configure", id)
		default:
			if err := spec.writeMCP(configPath, mcpURL); err != nil {
				rep.fail("%-8s %v", id, err)
				rep.note("")
				rep.note("Add this manually to %s:", configPath)
				for _, line := range strings.Split(spec.manualSnippet(mcpURL), "\n") {
					rep.note("    %s", line)
				}
				res.Error = err.Error()
			} else {
				rep.ok("%-8s MCP     %s → %s%s", id, configPath, mcpServerName,
					scopeNote(spec.mcpConfig, opts.global))
				res.MCP = configPath
			}
		}

		// Provider registration, so the agent's own model calls go through orq.
		// Consent came from the single wiring question above (or the flags that
		// pre-answer it); the two branches are independent — gateway-only is the
		// coherent "route my calls through orq, but do not give the agent
		// workspace read/write" configuration, and MCP-only the reverse.
		switch {
		case opts.noGateway && spec.writeProvider != nil:
			rep.note("%-8s provider  skipped (--no-gateway)", id)
		case spec.writeProvider != nil:
			if path, perr := spec.providerConfig(opts.global); perr == nil && path != "" {
				models := codingModels(rep, client, state)
				// Must match the [models."<key>"] form the writer emits, which is
				// the full provider/model ref.
				defaultModel := ""
				if best, ok := defaultCodingModel(rep, client, state); ok {
					defaultModel = best.Ref()
				}
				// An empty catalogue is not a config to write, it is nothing to
				// offer — ListModels failed, or the workspace has no chat model
				// with function calling. Every writer clears its own keys before
				// emitting new ones, so calling through with no models deletes a
				// working provider block from an earlier run and reports success.
				// Leaving the file untouched is the only outcome that cannot make
				// the agent worse off than it already was.
				//
				// Not recorded as an agent error: step 4 already reported the
				// cause with a link and a retry, and MCP wiring above succeeded.
				// Failing the run here would report a workspace state twice and
				// call a configured agent broken.
				if len(models) == 0 {
					rep.warn("%-8s provider  skipped: no models to offer, %s left unchanged", id, path)
					results = append(results, res)
					continue
				}
				written, werr := spec.writeProvider(path, client.RouterBaseURL(), state.bearer, models, defaultModel)
				switch {
				case werr != nil:
					rep.warn("%-8s provider  %v", id, werr)
				default:
					// Only claim a model count when the format actually carries
					// one: codex's profile names a single default and takes its
					// list from elsewhere.
					listed := ""
					if written > 0 {
						listed = fmt.Sprintf(" (%d models)", written)
					}
					// No scope note here: when MCP was also written it named the
					// same file and already carried one, and repeating it per
					// line would say the same thing twice for one agent.
					scope := ""
					if res.MCP == "" {
						scope = scopeNote(spec.providerConfig, opts.global)
					}
					rep.ok("%-8s provider  %s → orq gateway%s%s", id, path, listed, scope)
					// Setup registers orq as an option rather than making it the
					// default, so for some agents there is a step the user would
					// otherwise never find.
					if spec.providerUsage != "" {
						rep.note("           %s", spec.providerUsage)
					}
					res.Provider = path
					res.ModelCount = written
				}
			}
		}

		results = append(results, res)
	}
	return results
}

// codingModels fetches the gateway catalogue once per run and returns every
// chat model the workspace has enabled.
//
// It used to return four hand-picked families and probe each one. The families
// were arbitrary — nothing about kimi or the gateway implies four — and the
// probing existed to compensate for selecting from is_active, which included
// models the workspace had never enabled and which therefore often failed.
// With the enabled set as the pool, `orq launch` writes the whole list with no
// probing at all and works; setup now matches it, so the two produce the same
// config by the same rule, and the user gets their models rather than our four.
var cachedCodingModels []auth.RouterModel
var codingModelsFetched bool

// provenModel is the model that answered the default-model probe, with how long
// it took. The verify step reports these instead of paying for a second
// completion to learn the same thing.
var provenModel string
var provenTook time.Duration

// defaultModelResolved memoizes the whole probe outcome, success or failure.
// Every agent with a provider writer asks for the default model, and each
// probe is a billed completion — without this, wiring four agents billed four
// completions for the same answer (and a broken workspace would have billed
// up to three failures per agent).
var defaultModelResolved bool
var provenCandidate auth.RouterModel

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

// defaultCodingModel picks the model an agent should open with and proves it
// answers. Preference order first (a coding agent wants a coding model), then
// anything else the workspace enabled.
//
// Exactly one billed completion: this is the only model that MUST work, since
// it is what the agent uses before the user chooses anything. The rest of the
// list is the user's to pick from, and a bad entry there costs them a switch
// rather than a broken first prompt.
func defaultCodingModel(rep *reporter, client *auth.Client, state *authState) (auth.RouterModel, bool) {
	if defaultModelResolved {
		return provenCandidate, provenModel != ""
	}
	models := codingModels(rep, client, state)
	if len(models) == 0 {
		return auth.RouterModel{}, false
	}
	defaultModelResolved = true

	ordered := []auth.RouterModel{}
	for _, group := range auth.CandidateCodingModels(models, preferredCodingModels) {
		ordered = append(ordered, group...)
	}
	seen := map[string]bool{}
	for _, m := range ordered {
		seen[m.Ref()] = true
	}
	for _, m := range models {
		if !seen[m.Ref()] {
			ordered = append(ordered, m)
		}
	}

	stopSpin := rep.busy("checking the default model answers…")
	defer stopSpin()
	tried := 0
	for _, candidate := range ordered {
		took, err := client.TimeModel(state.bearer, candidate.Ref())
		if err == nil {
			provenModel, provenTook, provenCandidate = candidate.Ref(), took, candidate
			return candidate, true
		}
		tried++
		// Stop walking the whole catalogue on a broken workspace: each attempt
		// is billed, and after a handful of failures the problem is the gateway
		// or the credential, not the model.
		if tried >= 3 {
			break
		}
	}
	return auth.RouterModel{}, false
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

// verifySetup makes one authenticated call with the credentials setup just
// configured. It deliberately avoids whoami: an API-key-only run has no session
// and whoami would report "not logged in" even though everything works.
func verifySetup(rep *reporter, client *auth.Client, state *authState) bool {
	if state.session != nil && state.session.User != nil {
		workspace := ""
		if state.session.ActiveWorkspaceKey != nil {
			workspace = *state.session.ActiveWorkspaceKey
		}
		rep.ok("whoami          %s / %s", state.session.User.Email, workspace)
	}
	// A freshly minted key is not accepted for a second or two, so a single
	// immediate call would report a working setup as broken.
	var projects []auth.Project
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		projects, err = client.ListProjects(state.bearer)
		if err == nil {
			rep.ok("api reachable   %s  (%d projects)", client.URLs.APIBaseURL, len(projects))
			return true
		}
	}
	rep.fail("api unreachable  %s: %v", client.URLs.APIBaseURL, err)
	return false
}

// verifyGateway sends one real request through the AI Gateway with the
// credentials setup just configured. Reaching the API only proves the key is
// valid; this proves the thing the user actually came for — that a model
// answers through the router.
func verifyGateway(rep *reporter, client *auth.Client, state *authState) bool {
	models := codingModels(rep, client, state)
	if len(models) == 0 {
		rep.warn("gateway        no model answered — connect a provider, then re-run 'orq setup'")
		return false
	}
	// Reuse the probe from model selection rather than repeating it. It already
	// proved the exact thing this step reports — a real completion through the
	// router on this credential — and repeating it billed the user twice.
	if provenModel != "" {
		rep.ok("gateway        %s answered in %dms via %s", provenModel, provenTook.Milliseconds(), client.RouterBaseURL())
		return true
	}
	ref := models[0].Ref()
	took, err := client.TimeModel(state.bearer, ref)
	if err != nil {
		rep.fail("gateway        %s did not answer: %v", ref, err)
		return false
	}
	rep.ok("gateway        %s answered in %dms via %s", ref, took.Milliseconds(), client.RouterBaseURL())
	return true
}

// webBaseURL is the dashboard origin for this install, or "" when it cannot be
// known (self-hosted without ORQ_WEB_BASE_URL).
func webBaseURL(state *authState) string {
	webBase := strings.TrimRight(os.Getenv("ORQ_WEB_BASE_URL"), "/")
	// Without an explicit override, only the hosted product has a known web URL.
	if webBase == "" && state.apiBase == auth.DefaultAPIBaseURL {
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
	if webBase != "" {
		links["models"] = webBase + modelsSettingsPath
	}
	return links
}

func printFinalScreen(rep *reporter, agents []agentResult, links map[string]string, routerBase string, verified bool, opts *setupOptions) {
	if opts.noInput {
		return
	}
	w := bartolocli.Stderr
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", 64))
	fmt.Fprintln(w)
	// Do not claim success the verification step just disproved: the checks
	// above already printed what failed, so this line is the only thing left
	// telling the user whether to trust the result.
	if verified {
		fmt.Fprintln(w, "  ✓ Setup complete")
	} else {
		fmt.Fprintln(w, "  ! Setup finished with failed checks — see above")
	}
	fmt.Fprintln(w)

	wired := []string{}
	for _, a := range agents {
		if a.Error == "" && a.MCP != "" {
			if spec, ok := lookupAgent(a.Agent); ok {
				wired = append(wired, spec.Label)
			}
		}
	}
	if len(wired) > 0 {
		fmt.Fprintf(w, "  %s can now read and write your orq.ai workspace.\n", strings.Join(wired, " and "))
		fmt.Fprintln(w, "  Ask one of them:")
		fmt.Fprintln(w)
		fmt.Fprintln(w, `      "list my orq.ai agents"`)
	} else {
		// No agent wired — the other way in is the gateway itself: point an
		// existing OpenAI client at the router and change nothing else.
		fmt.Fprintln(w, "  Route an existing OpenAI client through the gateway:")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "      client = OpenAI(api_key=os.environ[\"ORQ_API_KEY\"],\n"+
			"                      base_url=\"%s\")\n", routerBase)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Or start a coding agent:  orq launch claude")
	}
	// Both the provider block and the MCP entry reference ORQ_API_KEY, so an
	// MCP-only agent needs the export just as much as a provider-wired one.
	// Checking only the provider is what let kimi come up with a dead MCP
	// server and no warning.
	keyReferenced := false
	for _, a := range agents {
		if a.Error == "" && (a.Provider != "" || a.MCP != "") {
			keyReferenced = true
		}
	}
	if keyReferenced && strings.TrimSpace(os.Getenv("ORQ_API_KEY")) == "" {
		sh := detectShell(viper.GetString("config-directory"))
		fmt.Fprintln(w, "  ! your agents read ORQ_API_KEY from the environment; it is not set in this shell.")
		fmt.Fprintln(w, "    Export it once, then restart the agent:")
		fmt.Fprintln(w)
		if sh.Profile == "" {
			// Unrecognised shell: naming a profile file would be a guess, so
			// give the command that works right now and let the user place it.
			fmt.Fprintf(w, "      %s\n", sh.Line)
			fmt.Fprintf(w, "      # add that to your shell profile to make it stick\n")
		} else {
			fmt.Fprintf(w, "      echo '%s' >> %s && %s\n", sh.Line, sh.Profile, sh.Line)
			if strings.HasSuffix(sh.Profile, ".zshenv") {
				fmt.Fprintln(w)
				fmt.Fprintln(w, "    (~/.zshenv, not ~/.zshrc — .zshrc applies only to interactive shells.)")
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	if ws := links["workspace"]; ws != "" {
		fmt.Fprintf(w, "  Workspace   %s\n", ws)
	}
	if m := links["models"]; m != "" {
		fmt.Fprintf(w, "  Models      %s\n", m)
	}
	fmt.Fprintf(w, "  Docs        %s\n", links["docs"])
	fmt.Fprintln(w, "  Stuck?      orq doctor")
	fmt.Fprintln(w)
}
