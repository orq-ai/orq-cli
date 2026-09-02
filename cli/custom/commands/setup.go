package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
	"orq/cli/custom/skills"

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
	caps        []string
	noGateway   bool
	noInput     bool
	yes         bool
	// persistKey allows --api-key to replace the saved credential; only 'orq setup' sets it.
	persistKey bool
	// finalScreen marks a run that ends in printFinalScreen, which reports every
	// wire per agent. The per-agent progress lines are that same report, so the
	// two together printed each wire twice; only 'orq setup' sets it.
	finalScreen bool
	// scope is --global / --local: where a write lands for the capabilities
	// that have two places to put it (mcp today, skills next). Three-valued,
	// not two bools: "neither named" is its own answer — global for a write,
	// both scopes for a removal — and a pair of bools can also represent
	// "both named", which is not an answer at all.
	scope configScope
	// confirmFn overrides the prompt, for tests. Every branch guarded by a
	// confirm is otherwise reachable only through noInput, which takes the
	// default — so the answer the user is most likely to regret (declining a
	// replacement, accepting a workspace switch) had no way to be exercised.
	confirmFn func(message string, def bool) bool
}

// configScope is where a scope-capable capability writes. scopeUnset is the
// third state the flags can leave behind, and it is not the same as scopeGlobal:
// an unset removal covers both scopes so a project entry cannot outlive every
// `orq disconnect` run by whoever forgot which scope it landed in.
type configScope int

const (
	scopeUnset configScope = iota
	scopeGlobal
	scopeLocal
)

// scopeFlag binds --global and --local to one configScope, so naming both is a
// parse error at the flag layer rather than a check every entry point has to
// remember, and no downstream reader can see the two set at once.
type scopeFlag struct {
	target *configScope
	sets   configScope
}

func (s *scopeFlag) String() string { return strconv.FormatBool(*s.target == s.sets) }
func (s *scopeFlag) Type() string   { return "bool" }

func (s *scopeFlag) Set(v string) error {
	on, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	if !on {
		return nil
	}
	if *s.target != scopeUnset && *s.target != s.sets {
		return errors.New("--global and --local ask for opposite things — name one")
	}
	*s.target = s.sets
	return nil
}

// --yes takes the affirmative without asking; --no-input or no TTY takes the default rather than blocking a script.
// A prompt that was drawn and then failed (Ctrl-C, closed terminal) is a decline, never the default: at a true-default
// prompt the default is "yes", and taking an abort as consent mints keys and edits shell profiles the user backed out of.
func (o *setupOptions) confirm(message string, def bool) bool {
	if o.confirmFn != nil {
		return o.confirmFn(message, def)
	}
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

// confirmPersistent is confirm for a change that outlives the run. --yes does
// not grant it: answering yes to every prompt is a statement about this
// invocation, and moving the active workspace redirects every later command.
// An unattended run declines, exactly as --no-input does.
func (o *setupOptions) confirmPersistent(message string) bool {
	// Checked ahead of confirmFn, not after: a seam that answers a prompt
	// production never draws reports a branch no user can reach.
	if o.yes || o.noInput {
		return false
	}
	return o.confirm(message, false)
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
		if a.Error != "" || a.Skipped != "" || a.MCPError != "" {
			return false
		}
	}
	return true
}

type agentResult struct {
	Agent      string `json:"agent"`
	Provider   string `json:"provider,omitempty"`
	ModelCount int    `json:"model_count,omitempty"`
	// Skills is the directory this agent's skills landed in, empty when the
	// capability was not requested. The final screen reports per agent, so a
	// skills-only run has something to show even with no gateway wire.
	Skills string `json:"skills,omitempty"`
	// MCP is the config file the orq-workspace entry landed in, empty when the
	// capability was not requested. One field per capability, like Skills: the
	// final screen labels each row with the capability that produced it, so a
	// shared field would make it name the wrong one.
	MCP   string `json:"mcp,omitempty"`
	Error string `json:"error,omitempty"`
	// MCPError is an MCP write that was attempted and failed. Separate from
	// Error because Error is the gateway's — the final screen renders it under
	// that label — and one agent can lose both wires in the same run. Folding
	// them together printed the MCP failure as a gateway one, or, when the
	// gateway had already failed, dropped it from the screen and the JSON
	// entirely.
	MCPError string `json:"mcp_error,omitempty"`
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
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked (except switching workspace, which always needs a person)")
	f.StringSliceVar(&opts.caps, "capability", nil,
		"Capabilities to connect ("+strings.Join(availableCapabilities(), ", ")+"); repeatable")
	// The same pair connect and disconnect carry, for the same capabilities.
	// On setup they pre-answer the wizard's scope question, which is how a
	// non-interactive run — install.sh, CI — names a scope at all.
	addScopeFlags(f, &opts)
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
	// Before anything else: a mistyped capability must not cost a full
	// authentication round trip before it is noticed, and it must not be
	// noticed only as a wizard that quietly connects nothing.
	if len(opts.caps) > 0 {
		caps, err := validateCapabilities(opts.caps)
		if err != nil {
			return err
		}
		opts.caps = caps
	}

	// A skills-only run unpacks files out of this binary and touches nothing
	// else: no key to mint, no workspace to pick, nothing to verify against the
	// API. Sending it through steps 1 and 2 made `orq setup --capability skills`
	// die at "no TTY available for browser login". connect already is that run,
	// credential gate and all, so hand it over rather than growing a second
	// credential-free path here.
	if len(opts.caps) > 0 && !capsNeedCredential(opts.caps) {
		if !opts.noInput {
			return runCredentialFreeSetup(cmd, opts)
		}
		return runConnect(cmd, opts, opts.caps, false)
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
	keyInfo, mintedToken, mintedThisRun, err := resolveAPIKey(rep, client, authState, opts)
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

	// Only a key minted this run needs the retry window; anything else that
	// fails is failing for good and the wait is pure dead time. resolveAPIKey
	// returns the fact itself: mintedToken is also set for a REUSED key, so
	// testing it kept the six seconds on exactly the runs that do not need them.
	verified := verifySetup(rep, client, authState, opts, mintedThisRun)
	result["verified"] = verified

	links := buildLinks(authState)
	if len(links) > 0 {
		result["links"] = links
	}
	complete := setupComplete(verified, agentResults)
	result["setup_complete"] = complete

	printFinalScreen(rep, agentResults, links, complete, opts)

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
	durableKey bool
	// skipDurableKey is set when the user declines the replacement prompt after
	// a saved key was rejected. It prevents the generic mint confirmation below
	// from silently creating the key anyway.
	skipDurableKey bool
	// sessionBearer keeps the workspace access token after a key takes over as bearer.
	sessionBearer        string
	enabledModels        int
	enabledModelsCounted bool
}

// useDurableKey makes an API key the bearer. Durability and the bearer move
// together: a second assignment site that sets one without the other is how a
// valid key ends up reported as "no durable API key".
func (s *authState) useDurableKey(key string) {
	// The session token is the only credential that can read an api-key record,
	// which is what turns a rejected key into a diagnosis.
	if s.session != nil {
		s.sessionBearer = s.bearer
	}
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
			rep.ok("using the key you passed")
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
			// Naming the file is the action: unsetting the shell variable will not help.
			rep.ok("using the API key from ./%s", file)
		} else {
			rep.ok("using the API key from ORQ_API_KEY")
		}
		return &authState{apiBase: apiBaseFromEnv(), bearer: envKey, suppliedKey: envKey}, nil
	}

	if session != nil {
		envKey := UserEnvAPIKey()
		savedKey, savedWS := savedAPIKey()
		if auth.EnvKeyShadowsWorkspace(envKey, savedKey, savedWS, activeWorkspaceKey(session)) {
			// Which one won is the fact; why lives in 'orq doctor'.
			rep.note("following your login, not the exported ORQ_API_KEY")
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

	// The resolved server outranks the host the session was authenticated
	// against, so `orq setup --server <url>` diverts this run instead of
	// silently writing the session's host into every agent config.
	apiBase := sessionAPIBase(session)
	client := auth.NewClient(apiBase)
	session, err = resolveWorkspace(rep, client, session, opts, signedInAs)
	if err != nil {
		return nil, err
	}

	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil {
		return nil, err
	}
	return &authState{apiBase: apiBase, session: session, bearer: active.AccessToken}, nil
}

// apiBaseFromEnv is the no-session fallback. It goes through ResolveURLs rather
// than reading an env var here, so the resolved --server, the env spellings and
// the default are decided in exactly one place.
func apiBaseFromEnv() string {
	return auth.ResolveURLs(serverURL()).APIBaseURL
}

func deviceLogin(ctx context.Context, rep *reporter, opts *setupOptions) (*auth.Session, error) {
	result, err := runDeviceLogin(ctx, rep, serverURL(), opts.workspace, true)
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
	// Bind the host to the profile this login belongs to, so an OAuth profile
	// routes without a flag the same way an API-key one does.
	if err := BindProfileServer(auth.ActiveProfile(), auth.Server()); err != nil {
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
func writeShellEnvFile(rep *reporter, token string) (string, error) {
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
	warnIfEnvFileWasExposed(rep, sh.EnvFile)
	f, err := os.OpenFile(sh.EnvFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// OpenFile's mode is only applied when creating a file. Tighten the
	// descriptor before writing the new token so an existing loose file never
	// briefly exposes the freshly minted key.
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := io.WriteString(f, header+assign+"\n"); err != nil {
		return "", err
	}
	return sh.EnvFile, nil
}

// warnIfEnvFileWasExposed says so before the chmod below tightens a
// pre-existing env file. Silently repairing it destroys the only evidence
// doctor's permissions check would ever have had, and the key inside was
// already readable by every other account on the machine by then.
//
// Unix only: Windows ACLs do not map onto these bits, so there is nothing to
// judge there — the same reason doctor's check is absent on Windows.
func warnIfEnvFileWasExposed(rep *reporter, path string) {
	if rep == nil || runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 == 0 {
		return
	}
	rep.warn("%s was mode %04o — readable by other accounts on this machine. Tightening it to 0600, but %s",
		tilde(path), info.Mode().Perm(), exposedAPIKeyAdvice(""))
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
//
// Deliberately keeps gateway_key_id and gateway_key_expires_at. Neither can
// authenticate anything — a key id is not a secret, it is on the dashboard —
// and logout does not revoke the key server-side, so discarding them destroys
// the only record of what is still out there. They are what lets logout print
// the revoke command, and doctor keep counting down to the expiry.
func clearAPIKeyProfile() (bool, error) {
	profile := auth.ActiveProfile()
	held := strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) != "" ||
		strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".gateway_key")) != ""
	if !held {
		return false, nil
	}
	bartolocli.Creds.Set("profiles."+profile+".gateway_key", "")
	return true, writeAPIKeyProfile(profile, "", "")
}

func savedAPIKey() (key, workspace string) { return auth.SavedAgentKey() }

func savedGatewayKeyID() string {
	// bartolo's GetProfile panics on a nil Creds; guard once for every caller.
	if bartolocli.Creds == nil {
		return ""
	}
	return strings.TrimSpace(bartolocli.GetProfile()["gateway_key_id"])
}

// recordAgentWiring notes which workspace an agent was wired against. Stored at
// agents.<id>, not per profile: the config connect writes is machine-global.
// Holds no key material.
func recordAgentWiring(id, workspace, keyID string) error {
	if bartolocli.Creds == nil {
		return nil
	}
	bartolocli.Creds.Set("agents."+id+".workspace", workspace)
	bartolocli.Creds.Set("agents."+id+".gateway_key_id", keyID)
	bartolocli.Creds.Set("agents."+id+".wired_at", time.Now().UTC().Format(time.RFC3339))
	return saveCreds()
}

// agentWiring reads back what recordAgentWiring stored. Empty means unrecorded,
// which callers must treat as unknown rather than as a mismatch.
func agentWiring(id string) (workspace, keyID string) {
	if bartolocli.Creds == nil {
		return "", ""
	}
	return strings.TrimSpace(bartolocli.Creds.GetString("agents." + id + ".workspace")),
		strings.TrimSpace(bartolocli.Creds.GetString("agents." + id + ".gateway_key_id"))
}

// clearAgentWiring drops the record when disconnect removes a provider config.
// Blanks the fields rather than deleting the block, matching how
// clearAPIKeyProfile treats "present but empty" as not-configured.
func clearAgentWiring(id string) error {
	if bartolocli.Creds == nil {
		return nil
	}
	bartolocli.Creds.Set("agents."+id+".workspace", "")
	bartolocli.Creds.Set("agents."+id+".gateway_key_id", "")
	bartolocli.Creds.Set("agents."+id+".wired_at", "")
	return saveCreds()
}

func activeWorkspaceKey(session *auth.Session) string {
	if session != nil && session.ActiveWorkspaceKey != nil {
		return strings.TrimSpace(*session.ActiveWorkspaceKey)
	}
	return ""
}

// wiredWorkspace names the workspace the bearer authenticates against: the
// saved key's own workspace when that key is the bearer, the session's active
// workspace otherwise. An exported ORQ_API_KEY returns "" — its workspace is
// unknowable, and naming the login's would be a confident wrong answer.
func wiredWorkspace(state *authState) string {
	if saved, savedWS := savedAPIKey(); saved != "" && state.bearer == saved {
		return savedWS
	}
	if envKey := UserEnvAPIKey(); envKey != "" && state.bearer == envKey {
		return ""
	}
	return activeWorkspaceKey(state.session)
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
	// An explicit host travels with the credentials it was used against, so a
	// later `orq --profile <name> ...` needs no flag.
	if server := auth.Server(); server != "" {
		bartolocli.Creds.Set("profiles."+profile+".server", server)
	}
	return saveCreds()
}

// shellEnvFileNames are the shell-integration files `orq setup` writes under
// the config directory: "env" for POSIX shells, "env.fish" for fish. Anything
// that enumerates them (clearShellEnvFile, doctor's permission check) ranges
// over this one slice, so a third shell variant can't be added to one caller
// and forgotten in the other.
var shellEnvFileNames = []string{"env", "env.fish"}

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
	for _, name := range shellEnvFileNames {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "ORQ_API_KEY") {
			continue
		}
		body := "# Cleared by 'orq auth logout'. Run 'orq setup' to create a new key.\n"
		if writeSecretFile(path, []byte(body)) == nil {
			cleared = append(cleared, path)
		}
	}
	return cleared
}

// writeSecretFile replaces the contents of a secret-bearing file only after
// tightening its held descriptor. The permission argument to os.WriteFile is
// ignored for an existing file, which would otherwise expose newly written
// secret data until a later chmod.
func writeSecretFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
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

// apiKeyEnvVars are the variables bartolo's apikey handler reads, in the order
// it reads them. One list: every place that has to answer "is a key set" or
// "keep the key away from here" gets the same answer.
var apiKeyEnvVars = []string{"ORQ_API_KEY", "ORQ_TOKEN", "ORQ_AUTHORIZATION"}

// envAPIKeySet reports only that a key is set, never which value.
func envAPIKeySet() bool {
	for _, name := range apiKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// ============================================================================
// Step 2 — API key
// ============================================================================

// resolveAPIKey returns the payload summary, the raw token the agents will use
// (minted or reused), and whether this run minted it. The last is separate
// because only a fresh key needs the verification retry window.
func resolveAPIKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (map[string]any, string, bool, error) {
	info := map[string]any{"created": false, "profile": auth.ActiveProfile()}

	if state.suppliedKey != "" {
		rep.ok("using the API key you passed")
		return info, "", false, nil
	}

	token, created, err := ensureDurableKey(rep, client, state, opts)
	if err != nil {
		return nil, "", false, err
	}
	info["created"] = created
	if token == "" {
		return info, "", false, nil
	}
	if _, err := writeShellEnvFile(rep, token); err != nil {
		// Not fatal: the key is saved, and the final screen still shows how to export it.
		rep.warn("could not write the shell env file: %v", err)
	} else {
		// The prompt is the point; naming the file we wrote is not.
		offerProfileSourceLine(rep, opts)
	}

	return info, token, created, nil
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
		rep.info("creating a key for workspace %s", active)
		token = ""
	case gatewayKeyDueForRenewal(time.Now()):
		// The superseded key is left alive until its own expiry: that overlap is
		// what keeps an agent config working until this run rewrites it.
		rep.note("saved key expires soon — creating its replacement; run 'orq connect' to rewire the agents")
		token = ""
	default:
		rep.ok("using your saved key")
		if !checkSavedKey(rep, client, state, opts, token) {
			token = ""
		}
	}
	if token != "" {
		return token, false, nil
	}
	if state.skipDurableKey {
		return "", false, nil
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
	rep.ok("gateway key created — expires in %d days", int(gatewayKeyLifetime.Hours()/24))
	return minted, true, nil
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
	if count > 0 {
		// The per-agent line already carries the count; saying it twice is noise.
		return
	}
	models := workspaceLink(state, modelsPath)
	rep.warn("no models enabled for this workspace")
	if models != "" {
		rep.warn("    Enable models   %s", models)
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
	skillDirs := map[string]string{}

	if len(selected) > 0 && hasCap(opts.caps, capSkills) {
		scope := skillsWriteScope(opts)
		res, err := skills.Install(selected, scope)
		if err != nil {
			// Connect's only job for this capability is the thing that just
			// failed, so it is fatal here. launch degrades instead.
			return nil, fmt.Errorf("installing skills: %w", err)
		}
		targets := skillTargetsFor(selected, scope)
		if !opts.finalScreen {
			for _, target := range targets {
				rep.ok("%-8s %-9s %s", target.Agent, capSkills, tilde(target.Dir))
			}
		}
		for _, path := range res.Skipped {
			rep.warn("%-8s %-9s %s already exists and is not ours — left alone", "", capSkills, tilde(path))
		}
		if scope == skills.ScopeLocal {
			reportLocalSkillsNotes(rep, selected, targets)
		}
		// Resolved one agent at a time on purpose: a shared-directory reader
		// has no Target.Agent of its own, so the combined list cannot say
		// which agent a shared directory belongs to.
		for _, id := range selected {
			for _, t := range skillTargetsFor([]string{id}, scope) {
				skillDirs[id] = t.Dir
			}
		}
	}

	if len(selected) == 0 || opts.noGateway {
		// A skills-only run still wired something. Returning nil here is what
		// left the final screen with nothing to report but a gateway snippet.
		results := make([]agentResult, 0, len(skillDirs))
		for _, id := range selected {
			if dir, ok := skillDirs[id]; ok {
				results = append(results, agentResult{Agent: id, Skills: dir})
			}
		}
		return results, nil
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
		res := agentResult{Agent: id, Skills: skillDirs[id]}

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

// skillTargetsFor is the reporting view of skills.Targets: resolution failures
// are already fatal in Install, so a second failure here is not worth a second
// error path.
func skillTargetsFor(agents []string, scope skills.Scope) []skills.Target {
	targets, err := skills.Targets(agents, scope)
	if err != nil {
		return nil
	}
	return targets
}

// reportLocalSkillsNotes is what a local install has to say and the global
// one does not: the directories are untracked files in the repo, kimi reads
// project skills from the repository root only, and pi loads them only for a
// trusted project. Each line prints only when it applies.
func reportLocalSkillsNotes(rep *reporter, agents []string, targets []skills.Target) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root, inRepo := skills.RepoRoot(cwd)
	if inRepo && !skills.SameDir(root, cwd) && slices.Contains(agents, "kimi") {
		rep.warn("--local writes into %s, but the repository root is %s; kimi reads project skills from the root only", tilde(cwd), tilde(root))
	}
	if inRepo {
		var rels []string
		for _, t := range targets {
			if rel, err := filepath.Rel(cwd, t.Dir); err == nil {
				rels = append(rels, filepath.ToSlash(rel)+"/")
			}
		}
		if runtime.GOOS == "windows" {
			// Copy mode: real directories, and a committed one pins a
			// generation the CLI later expects to own.
			rep.info("add %s to .gitignore — orq manages these copies and replaces them on update", strings.Join(rels, " and "))
		} else {
			rep.info("add %s to .gitignore — the links point into ~/.orq and mean nothing to anyone else", strings.Join(rels, " and "))
		}
	}
	if slices.Contains(agents, "pi") {
		rep.info("%-8s %-9s pi loads project skills only for a trusted project — approve it in pi once", "pi", capSkills)
	}
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
				rep.warn("%-8s no models to offer; kept the configuration already on this machine", id)
			} else {
				rep.warn("%-8s no models to offer; nothing written", id)
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
				// Record keeping, not the wire itself: the config is already on
				// disk, so a failure here is a warning, never res.Error.
				if err := recordAgentWiring(id, wiredWorkspace(state), savedGatewayKeyID()); err != nil {
					rep.warn("%-8s provider  wired, but could not record the workspace: %v", id, err)
				}
			}
		}
	}
}

func pluralModels(n int) string {
	if n == 1 {
		return "model"
	}
	return "models"
}

func reportAgent(rep *reporter, spec agentSpec, res agentResult, opts *setupOptions) {
	if res.Provider == "" || opts.finalScreen {
		return
	}
	// orq is the subject, the agent the object: "kimi  gateway" read as though
	// kimi supplied the gateway. The name matches the one the agent shows in its
	// own model list, so there is one name for one thing.
	line := launch.ProviderDisplayName + " configured for " + spec.ID
	// codex's profile names one default and takes its list from elsewhere, so only claim a count when there is one.
	if n := res.ModelCount; n > 0 {
		line += fmt.Sprintf("  (%d %s available)", n, pluralModels(n))
	}
	rep.ok("%s", line)
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

// promptForAgents offers agents that can receive one of the available
// capabilities. The setup wizard asks this before functionality, so it cannot
// filter from the user's not-yet-selected capability set. Filtering on
// writeProvider alone — which it used to do —
// hid claude, the one agent with no gateway provider config and the most common
// MCP agent there is, from every picker; agentReceives is the same question
// agentsToConnect asks for the bare-connect path.
func promptForAgents(rep *reporter, caps []string) ([]string, error) {
	registry := agentRegistry()
	options := make([]string, 0, len(registry))
	byOption := map[string]string{}
	defaults := []string{}

	detected := []string{}
	for _, spec := range registry {
		if !agentReceives(spec, caps) {
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
	}, &chosen, promptStdio()); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(chosen))
	for _, label := range chosen {
		ids = append(ids, byOption[label])
	}
	return ids, nil
}

// defaultCapabilities is what a bare `orq setup` connects: everything that is
// built. Tracing is excluded while dropUnavailableCaps still strips it —
// offering it in the picker would be offering something that then prints "not
// available yet" — which is exactly what availableCapabilities means, so the
// two are one list rather than two that can drift.
func defaultCapabilities() []string {
	return availableCapabilities()
}

// resolveCapabilities decides what this run connects. An explicit --capability
// flag wins, a non-interactive run (or --yes) takes the defaults, and an
// interactive one asks.
func resolveCapabilities(rep *reporter, opts *setupOptions) ([]string, error) {
	if len(opts.caps) > 0 {
		// Validated at the entry point (runSetup); this is the availability
		// filter every other path already applies, so `--capability tracing`
		// says "not available yet" here exactly as `orq connect tracing` does
		// instead of completing a setup that connected nothing.
		return dropUnavailableCaps(rep, opts.caps), nil
	}
	if opts.noInput || opts.yes {
		return defaultCapabilities(), nil
	}
	return promptForCapabilities(rep)
}

// promptForCapabilities is the multi-select, modeled on promptForAgents so the
// two questions in one wizard behave the same way.
func promptForCapabilities(rep *reporter) ([]string, error) {
	// Only what is built. The picker used to list tracing, which
	// dropUnavailableCaps then stripped with "not available yet" — offering a
	// choice and refusing it one keystroke later.
	options := availableCapabilities()
	labels := capabilityLabels()
	byOption := map[string]string{}
	display := make([]string, 0, len(options))
	for _, c := range options {
		label := labels[c]
		display = append(display, label)
		byOption[label] = c
	}
	defaults := make([]string, 0, len(display))
	for _, c := range defaultCapabilities() {
		defaults = append(defaults, labels[c])
	}

	var chosen []string
	if err := survey.AskOne(&survey.MultiSelect{
		Message: "What should orq connect?",
		Options: display,
		Default: defaults,
	}, &chosen, promptStdio()); err != nil {
		return nil, err
	}
	caps := make([]string, 0, len(chosen))
	for _, label := range chosen {
		caps = append(caps, byOption[label])
	}
	return dropUnavailableCaps(rep, caps), nil
}

// capabilityLabels is the one-line description the picker shows per capability.
// Its own function so a capability that reaches availableCapabilities without a
// label here is a test failure rather than a blank row in the picker.
//
// mcp names the login because the entry carries no credential: the agent
// authenticates to the server itself, and a user who expects the wire to be
// finished when setup exits would otherwise meet that step unannounced.
func capabilityLabels() map[string]string {
	return map[string]string{
		capGateway: fmt.Sprintf("%-9s route the agent's model calls through orq", capGateway),
		capTracing: fmt.Sprintf("%-9s send traces to orq", capTracing),
		capSkills:  fmt.Sprintf("%-9s install the orq skills so the agent knows how to use orq", capSkills),
		capMCP:     fmt.Sprintf("%-9s give the agent orq's MCP tools (the agent logs in itself)", capMCP),
	}
}

// resolveScope settles where the scope-capable capabilities write, before
// anything writes. A named flag is the answer, --yes and --no-input take the
// default, and only an otherwise-interactive run asks.
//
// Global is the default because every other artifact `orq setup` writes is
// machine-global, and install.sh runs setup non-interactively from wherever the
// user happens to be — a local default would give that one directory MCP tools
// and no other, silently. There is no inference: the working directory never
// decides this, only the prompt or the flag.
func resolveScope(rep *reporter, opts *setupOptions, caps []string) error {
	if opts.scope != scopeUnset || opts.noInput || opts.yes {
		return nil
	}
	if !scopeMatters(caps) {
		return nil
	}
	global, err := promptForScope(caps)
	if err != nil {
		return err
	}
	if global {
		opts.scope = scopeGlobal
	} else {
		opts.scope = scopeLocal
	}
	// The same refusal a typed --local gets: a project scope answered from a
	// directory that is not a project writes a file the agent never reads.
	return checkScopeFlags(rep, opts, caps)
}

// scopeMatters answers "would either answer change what this run writes?".
// Skills: any detected agent that receives them has two places to put them.
// MCP: only an agent with two config paths does. Anyone else is asked a
// question whose answer nothing consults.
func scopeMatters(caps []string) bool {
	for _, spec := range agentRegistry() {
		if !spec.detect() {
			continue
		}
		if hasCap(caps, capSkills) && skills.Receives(spec.ID) {
			return true
		}
		if hasCap(caps, capMCP) && spec.writeMCP != nil && mcpScopeAware(spec) {
			return true
		}
	}
	return false
}

// scopePrompt names only the capabilities this run writes: a skills-only run
// asked about "the MCP entry" is told something it did not ask for.
func scopePrompt(caps []string) string {
	var what []string
	if hasCap(caps, capMCP) {
		what = append(what, "the MCP entry")
	}
	if hasCap(caps, capSkills) {
		what = append(what, "skills")
	}
	return fmt.Sprintf("Where should %s go?", strings.Join(what, " and "))
}

// promptForScope is one question for every scope-capable capability rather than
// one each: the answer is the same kind of answer, and asking twice in a
// four-question wizard buys nothing. Global leads because it is the default.
func promptForScope(caps []string) (bool, error) {
	globalOption := fmt.Sprintf("%-9s every project on this machine", "global")
	localOption := fmt.Sprintf("%-9s this project only", "local")

	var chosen string
	if err := survey.AskOne(&survey.Select{
		Message: scopePrompt(caps),
		Options: []string{globalOption, localOption},
		Default: globalOption,
	}, &chosen, promptStdio()); err != nil {
		return false, err
	}
	return chosen == globalOption, nil
}

// ============================================================================
// Verification and the final screen
// ============================================================================

// verifySetup makes one authenticated call with the new credentials. Not whoami: an --api-key run has no session and whoami would report "not logged in".
func verifySetup(rep *reporter, client *auth.Client, state *authState, opts *setupOptions, mintedThisRun bool) bool {
	// A freshly minted key is not accepted for a second or two; without the retry a working setup reports as broken.
	attempts := 1
	if mintedThisRun {
		attempts = 4
	}
	// workspace-settings over projects: same reachability signal, and it names
	// the workspace the credential actually belongs to.
	var err error
	var keyWS string
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if keyWS, err = client.KeyWorkspace(state.bearer); err == nil {
			return reconcileKeyWorkspace(rep, client, state, opts, state.bearer, keyWS, false)
		}
	}
	if auth.Unauthorized(err) {
		// 401 is a revoked key, not a workspace mismatch: a key from another
		// workspace answers 200 and is handled by reconcileKeyWorkspace above.
		rep.fail("api key rejected  %s: %v", client.URLs.APIBaseURL, err)
		diagnoseRejectedKey(rep, client, state)
		return false
	}
	if auth.Forbidden(err) || auth.NotFound(err) {
		// The route did not answer for this credential: a self-hosted base
		// without it, or a key type it does not serve. That says nothing about
		// whether the setup works, so prove it rather than assume either way.
		// This is the call verification used before workspace-settings replaced
		// it. "verified" is a machine contract; it must never be a guess.
		if _, perr := client.ListProjects(state.bearer); perr == nil {
			rep.warn("could not confirm the key's workspace: %v", err)
			return true
		}
		rep.fail("api key rejected  %s: %v", client.URLs.APIBaseURL, err)
		diagnoseRejectedKey(rep, client, state)
		return false
	}
	rep.fail("api unreachable  %s: %v", client.URLs.APIBaseURL, err)
	return false
}

// diagnoseRejectedKey says why the key was refused, using the session token: a
// gateway key cannot read its own record, and the workspace-scoped route 404s
// for a key belonging to another workspace, which is the answer we want.
// Best effort — every path here is already reporting a failure.
func diagnoseRejectedKey(rep *reporter, client *auth.Client, state *authState) {
	if state.sessionBearer == "" {
		return
	}
	keyID := savedGatewayKeyID()
	if keyID == "" {
		keyID = auth.KeyIDFromToken(state.bearer)
	}
	if keyID == "" {
		return
	}
	rec, err := client.GetAPIKey(state.sessionBearer, keyID)
	if auth.NotFound(err) {
		// The route is workspace-scoped, so 404 covers both "revoked" and "lives
		// in another workspace" — measured, a key deleted from this workspace and
		// a key that was never in it are the same 404. Name both; claiming either
		// one sends half the users down the wrong remedy.
		//
		// info, not note: note() is suppressed under --no-input, which is exactly
		// the run whose only output is the failure line this explains.
		rep.info("key %s was revoked, or belongs to a workspace other than %s — delete it from credentials.json to mint a new one",
			keyID, activeWorkspaceKey(state.session))
		return
	}
	if err != nil {
		return
	}
	if len(rec.Projects) > 0 {
		rep.info("key %s is scoped to project(s) %s", keyID, strings.Join(rec.Projects, ", "))
		return
	}
	if rec.Active != nil && !*rec.Active {
		rep.info("key %s is in this workspace but inactive", keyID)
	}
}

// checkSavedKey settles the reused key here, at the step that decides to reuse
// it, rather than at the verification call three steps later: by then the key
// is written into every agent config and the shell env file. One request, the
// same one verifySetup makes. Returns false when the key must not be reused,
// which falls through to minting a replacement.
func checkSavedKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions, token string) bool {
	keyWS, err := client.KeyWorkspace(token)
	if auth.Unauthorized(err) {
		return reconcileRejectedSavedKey(rep, state, opts)
	}
	if err != nil {
		// Anything short of a 401 leaves the key in place. A blip, a 5xx, a 404
		// on a self-hosted base without the route, or a 403 from a key type the
		// route does not serve are all "could not tell", and re-minting over any
		// of them orphans a working key. Say so: "✓ using your saved key" has
		// already printed, and unannounced this reads as a check that passed.
		rep.warn("could not confirm the saved key's workspace: %v — reusing it", err)
		return true
	}
	return reconcileKeyWorkspace(rep, client, state, opts, token, keyWS, true)
}

// reconcileRejectedSavedKey handles a saved key the API answered 401 to.
//
// That is a revoked or unknown key, and nothing else: measured against the live
// API, a key belonging to a different workspace gets a 200 from
// /v2/workspace-settings naming that workspace, so it never reaches here. The
// server's wording ("API key is not valid for this workspace") invites the
// opposite reading, which is why this says so — an earlier version offered to
// switch workspace and reuse the key, and a switch cannot revive a key the
// server has no record of.
//
// So the only choice worth offering is replace-or-keep. Never replace silently:
// bare setup mints without a prompt, but discarding a credential the user may
// have wired somewhere else is an exceptional, destructive transition.
func reconcileRejectedSavedKey(rep *reporter, state *authState, opts *setupOptions) bool {
	_, savedWS := savedAPIKey()
	active := activeWorkspaceKey(state.session)
	where := active
	if where == "" {
		where = savedWS
	}
	prompt := "Saved key was rejected — it has been revoked or deleted. Create a new gateway key?"
	if where != "" {
		prompt = fmt.Sprintf("Saved key was rejected for workspace %s — it has been revoked or deleted. Create a new gateway key?", where)
	}
	if !opts.confirm(prompt, true) {
		state.skipDurableKey = true
		rep.info("kept the rejected saved key; no gateway key was created")
		return false
	}
	rep.info("saved key was rejected — creating a new key for %s", where)
	return false
}

// reconcileKeyWorkspace compares the workspace the API says the key belongs to
// against the one the user is logged in to and, on a mismatch, offers to move
// the login to the key's workspace. The local guard cannot see this case:
// profile["workspace"] is blank for keys minted before that field existed and
// for every --api-key run, and either side unknown means no mismatch.
// Only internal staff have a second workspace to be in the wrong one of.
// mayMint distinguishes the two call sites: step 2 falls through to minting a
// replacement, verification is the end of the run and creates nothing. Sharing
// one message told a user whose verification just failed that a key was being
// created for them, and said it with note(), which --no-input suppresses — so a
// CI run got "verified": false and not one line explaining why.
func reconcileKeyWorkspace(rep *reporter, client *auth.Client, state *authState, opts *setupOptions, probed, keyWS string, mayMint bool) bool {
	active := activeWorkspaceKey(state.session)
	if !keyWorkspaceMismatch(keyWS, active) {
		recordKeyWorkspace(rep, probed, keyWS)
		return true
	}
	if opts.confirmPersistent(fmt.Sprintf("Saved key is for workspace %s, you are in %s. Switch to %s?", keyWS, active, keyWS)) {
		updated, err := client.UseWorkspace(keyWS)
		if err != nil {
			rep.warn("could not switch to workspace %s: %v", keyWS, err)
			return false
		}
		state.session = updated
		recordKeyWorkspace(rep, probed, keyWS)
		rep.ok("switched to workspace %s", keyWS)
		return true
	}
	if mayMint {
		// info, not note: note is dropped by the quiet reporter, so --no-input
		// replaced the saved key with nothing on stderr explaining it.
		rep.info("key is for workspace %s — creating one for %s", keyWS, active)
	} else {
		rep.fail("api key belongs to workspace %s, this login is %s", keyWS, active)
	}
	return false
}

// recordKeyWorkspace persists the workspace the API just named for the saved
// key. Without it this check is a per-run diagnostic: profiles.<p>.workspace
// stays blank for every key minted before the field existed and for every
// --api-key run, so keyWorkspaceMismatch keeps reporting "no mismatch" and
// `orq connect`, `orq launch` and `orq doctor` stay blind to the very drift
// this function just resolved. It also stops the next run repeating the call.
//
// probed is the credential the workspace was resolved for, and it must be the
// saved key. Verification runs with the session token whenever no durable key
// was resolved (a declined mint, or --api-key), and recording the session's
// workspace beside a key that belongs elsewhere writes a match that is false,
// which is the drift this check exists to catch.
func recordKeyWorkspace(rep *reporter, probed, keyWS string) {
	// Creds nil is not an error here: bartolo's GetProfile dereferences the
	// global without a guard, and this is a best-effort backfill, not the
	// credential itself. ProfileServer guards the same way.
	if keyWS == "" || probed == "" || bartolocli.Creds == nil {
		return
	}
	saved, recorded := savedAPIKey()
	if probed != saved || recorded == keyWS {
		return
	}
	// Only the workspace field. writeCredsProfile also persists `server`, and a
	// profile server outranks `orq server set`, so backfilling through it would
	// pin the profile to this run's host without anyone asking for it.
	bartolocli.Creds.Set("profiles."+auth.ActiveProfile()+".workspace", keyWS)
	if err := saveCreds(); err != nil {
		// Not fatal: the key itself is fine, this only re-arms the local guard.
		rep.warn("could not record the key's workspace: %v", err)
	}
}

// webBaseURL is the dashboard origin, "" when self-hosted without ORQ_WEB_BASE_URL.
// webBaseFor derives the dashboard host, shared with doctor.
func webBaseFor(apiBase string) string {
	webBase := strings.TrimRight(os.Getenv("ORQ_WEB_BASE_URL"), "/")
	if webBase == "" && auth.IsHostedAPIBase(apiBase) {
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

func printFinalScreen(rep *reporter, agents []agentResult, links map[string]string, verified bool, opts *setupOptions) {
	if opts.noInput {
		return
	}
	w := rep.w
	fmt.Fprintln(w)
	// The verdict and the agent names sit flush left; everything they own is
	// indented under them, so the screen has one column to scan down.
	if verified {
		fmt.Fprintf(w, "%s %s\n", paint(ansiOK, "✓"), bold("Setup complete"))
	} else {
		fmt.Fprintf(w, "%s %s\n", paint(ansiWarn, "!"), bold("Setup finished with failed checks — see above"))
	}
	fmt.Fprintln(w)

	// The agent is the heading and its capabilities are indented under it, so
	// the name is written once however many capabilities it got, and each row
	// carries its own verdict. No per-framework start commands: the question
	// this screen answers is what is wired, not how to run each agent.
	wiredAny := false
	for _, a := range agents {
		type capRow struct{ mark, label, detail string }
		rows := []capRow{}
		// One gateway row per agent, whichever way the wire went: a failure
		// that prints nothing reads as an agent nobody asked about. Error and
		// Skipped are the gateway's own; the MCP leg has its own pair below.
		switch {
		// Error before Provider: a failed wire can still carry the path it was
		// writing to, and reporting that path with a tick is the claim the
		// error exists to deny.
		case a.Error != "":
			rows = append(rows, capRow{paint(ansiRed, "✗"), capGateway, a.Error})
		case a.Provider != "":
			detail := tilde(a.Provider)
			// codex takes its list from elsewhere and reports no count, so only
			// claim one when there is one.
			if n := a.ModelCount; n > 0 {
				detail += fmt.Sprintf("  (%d %s)", n, pluralModels(n))
			}
			rows = append(rows, capRow{paint(ansiOK, "✓"), capGateway, detail})
		case a.Skipped != "":
			rows = append(rows, capRow{paint(ansiRed, "✗"), capGateway, a.Skipped})
		}
		if a.Skills != "" {
			rows = append(rows, capRow{paint(ansiOK, "✓"), capSkills, tilde(a.Skills)})
		}
		// Its own row, under its own label: an MCP failure rendered under the
		// gateway's told the user to go and fix the wrong file.
		switch {
		case a.MCPError != "":
			rows = append(rows, capRow{paint(ansiRed, "✗"), capMCP, a.MCPError})
		case a.MCP != "":
			rows = append(rows, capRow{paint(ansiOK, "✓"), capMCP, tilde(a.MCP)})
		}
		if len(rows) == 0 {
			continue
		}
		if wiredAny {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\n", bold(paint(ansiBrand, a.Agent)))
		for _, row := range rows {
			fmt.Fprintf(w, "%s %-9s %s\n", row.mark, row.label, paint(ansiDim, row.detail))
		}
		wiredAny = true
	}
	if !wiredAny {
		fmt.Fprintln(w, "Nothing is wired on this machine yet.")
		fmt.Fprintf(w, "%s orq connect\n", padLabel("Wire"))
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
		fmt.Fprintf(w, "%s %s\n", paint(ansiWarn, "!"),
			"ORQ_API_KEY is not exported here, and agents inherit it from this shell.")
		switch {
		// A declined mint leaves no env file; sourcing it would be the remedy that fails.
		case fileMissing(sh.EnvFile):
			fmt.Fprintf(w, "%s\n", paint(ansiDim, "run 'orq setup' to create an API key and its env file"))
		case sh.Profile != "" && profileSourcesEnvFile(sh):
			fmt.Fprintf(w, "%s\n", sh.displayLine())
			fmt.Fprintf(w, "%s\n", paint(ansiDim, fmt.Sprintf("new shells already get it from %s", tilde(sh.Profile))))
		case sh.Profile == "":
			fmt.Fprintf(w, "%s\n", sh.displayLine())
			fmt.Fprintf(w, "%s\n", paint(ansiDim, "add that line to your shell profile so new shells get it too"))
		default:
			fmt.Fprintf(w, "echo '%s' >> %s && %s\n", sh.displayLine(), tilde(sh.Profile), sh.displayLine())
		}
	}
	fmt.Fprintln(w)
	if ws := links["workspace"]; ws != "" {
		fmt.Fprintf(w, "%s %s\n", padLabel("Workspace"), ws)
	}
	fmt.Fprintf(w, "%s orq doctor  %s  %s\n", padLabel("Stuck?"), paint(ansiDim, "·"), docsURL)
	fmt.Fprintln(w)
}

// labelWidth pads every action and link label; wide enough for "Workspace", the longest one printed.
const labelWidth = 11

func padLabel(s string) string {
	return paint(ansiDim, pad(s, labelWidth))
}
