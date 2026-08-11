package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	setupSteps        = 5
)

type setupOptions struct {
	interactive bool
	project     string
	workspace   string
	apiKey      string
	agents      []string
	global      bool
	noAgent     bool
	noEnv       bool
	noInput     bool
}

type agentResult struct {
	Agent      string `json:"agent"`
	MCP        string `json:"mcp,omitempty"`
	Provider   string `json:"provider,omitempty"`
	ModelCount int    `json:"model_count,omitempty"`
	Skills     string `json:"skills,omitempty"`
	SkillCount int    `json:"skill_count,omitempty"`
	Error      string `json:"error,omitempty"`
}

func NewSetupCommand() *cobra.Command {
	opts := setupOptions{}

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Authenticate, pick a project, and wire up your coding agent",
		Long: bartolocli.Markdown(`Gets a new machine from zero to working: signs you in, selects or creates a ` +
			`project, mints a project-scoped API key, and registers the orq.ai MCP server plus skills ` +
			`with your coding agent.

Run it bare for the short path, with ` + "`-i`" + ` to be asked about every choice, or fully ` +
			`flagged with ` + "`--no-input`" + ` for CI.

Supported agents: ` + strings.Join(agentIDs(), ", ") + `.`),
		// A failure here is a runtime problem, not a usage problem; dumping the
		// flag list on top of the error just buries it.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd, &opts)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Ask about every choice instead of inferring")
	f.StringVar(&opts.project, "project", "", "Project name to select or create")
	f.StringVar(&opts.workspace, "workspace", "", "Workspace key to activate")
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key instead of logging in and minting one")
	f.StringSliceVar(&opts.agents, "agent", nil, "Coding agent to instrument (repeatable): "+strings.Join(agentIDs(), ", "))
	f.BoolVar(&opts.global, "global", false, "Write agent config to the home directory instead of this project")
	f.BoolVar(&opts.noAgent, "no-agent", false, "Skip coding-agent instrumentation")
	f.BoolVar(&opts.noEnv, "no-env", false, "Do not write ORQ_API_KEY to ./.env")
	f.BoolVar(&opts.noInput, "no-input", false, "Never prompt; missing values become errors")
	return cmd
}

func runSetup(cmd *cobra.Command, opts *setupOptions) error {
	// No TTY means no prompts, whatever the flags say.
	if !hasInteractiveTTY() {
		opts.noInput = true
	}
	if opts.noInput {
		opts.interactive = false
	}
	// A home-directory run (typically the installer chaining into setup) must
	// not scatter project files into $HOME.
	if !opts.global && !looksLikeProject() {
		opts.global = true
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

	// --- Step 2: project -----------------------------------------------------
	rep.step(2, setupSteps, "Project")
	project, created, err := resolveProject(rep, client, authState, opts)
	if err != nil {
		return err
	}
	result["project"] = map[string]any{
		"id":      project.ProjectID,
		"name":    project.Name,
		"created": created,
	}
	if authState.session != nil {
		authState.session.ActiveProjectID = project.ProjectID
		authState.session.ActiveProjectName = project.Name
		if err := auth.SaveSession(authState.session); err != nil {
			rep.warn("could not persist the project selection: %v", err)
		}
	}

	// --- Step 3: API key -----------------------------------------------------
	rep.step(3, setupSteps, "API key")
	keyInfo, mintedToken, err := resolveAPIKey(rep, client, authState, opts, project)
	if err != nil {
		return err
	}
	result["api_key"] = keyInfo
	// Verify with the credential the agents will actually use, not the session
	// token that happened to authenticate this run.
	if mintedToken != "" {
		authState.bearer = mintedToken
	}

	// --- Step 4: providers ---------------------------------------------------
	// The gateway routes to whatever the workspace has connected (BYOK). With
	// nothing connected there are no models, and every step after this one
	// degrades into a confusing "no models" instead of "connect a provider".
	rep.step(4, setupSteps, "Providers")
	result["models_enabled"] = resolveProviders(rep, client, authState, opts)

	// --- Step 5: coding agents ----------------------------------------------
	rep.step(5, setupSteps, "Coding agent")
	agentResults := instrumentAgents(rep, client, authState, opts)
	result["agents"] = agentResults

	// --- Verify --------------------------------------------------------------
	rep.note("")
	rep.note("Verifying…")
	verified := verifySetup(rep, client, authState)
	result["verified"] = verified
	// A failed gateway call is reported but does not fail setup: everything else
	// (MCP, skills, API key) still works without a connected provider.
	result["gateway_verified"] = verifyGateway(rep, client, authState)

	links := buildLinks(authState)
	if len(links) > 0 {
		result["links"] = links
	}
	result["setup_complete"] = verified

	printFinalScreen(rep, agentResults, links, client.RouterBaseURL(), opts)

	if err := emit(result); err != nil {
		return err
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

	// An environment key is usable as-is; do not persist or replace it.
	if envKey := strings.TrimSpace(os.Getenv("ORQ_API_KEY")); envKey != "" && session == nil {
		rep.ok("api key from ORQ_API_KEY")
		rep.note("credential order: ORQ_API_KEY (env) → login session. Unset it to sign in instead.")
		return &authState{apiBase: apiBaseFromEnv(), bearer: envKey, suppliedKey: envKey}, nil
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
	// Context-aware so Ctrl-C during the approval poll cancels instead of
	// waiting out the device-code expiry.
	client := auth.NewClient("").WithContext(ctx)
	start, err := client.StartDeviceLogin("orq-cli")
	if err != nil {
		return nil, err
	}
	rep.note("Open: %s", start.VerificationURIComplete)
	rep.note("Code: %s", start.UserCode)
	if !auth.OpenBrowser(start.VerificationURIComplete) {
		rep.note("Could not open the browser automatically. Open the URL manually.")
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
	workspaceKey := opts.workspace
	if workspaceKey == "" && len(profile.Workspaces) > 1 {
		workspaceKey, err = selectWorkspace(profile.Workspaces, "Choose an active workspace")
		if err != nil {
			return nil, err
		}
	}
	session, err := client.CreateSessionFromDeviceApproval(approved, profile, workspaceKey)
	if err != nil {
		return nil, err
	}
	if session.User != nil {
		rep.ok("signed in as %s", session.User.Email)
	}
	return session, nil
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

func writeAPIKeyProfile(profile, key string) error {
	bartolocli.Creds.Set("profiles."+profile+".type", "apikey")
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
// Step 2 — project
// ============================================================================

func resolveProject(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (*auth.Project, bool, error) {
	projects, err := client.ListProjects(state.bearer)
	if err != nil {
		return nil, false, err
	}

	if name := strings.TrimSpace(opts.project); name != "" {
		for i := range projects {
			if strings.EqualFold(projects[i].Name, name) {
				rep.ok("project %s (%s)", projects[i].Name, projects[i].ProjectID)
				return &projects[i], false, nil
			}
		}
		created, err := client.CreateProject(state.bearer, name, "")
		if err != nil {
			return nil, false, err
		}
		rep.ok("created project %s (%s)", created.Name, created.ProjectID)
		return created, true, nil
	}

	// Reuse the session's project unless the user asked to be prompted.
	if state.session != nil && state.session.ActiveProjectID != "" && !opts.interactive {
		for i := range projects {
			if projects[i].ProjectID == state.session.ActiveProjectID {
				rep.ok("project %s  (from session; --project to change)", projects[i].Name)
				return &projects[i], false, nil
			}
		}
	}

	if opts.noInput {
		return nil, false, errors.New("--no-input given but no project selected\n  Pass --project <name>, or run 'orq setup' interactively")
	}
	return promptForProject(rep, client, state, projects)
}

const createProjectOption = "+ Create a new project…"

func promptForProject(rep *reporter, client *auth.Client, state *authState, projects []auth.Project) (*auth.Project, bool, error) {
	options := make([]string, 0, len(projects)+1)
	options = append(options, createProjectOption)
	for _, p := range projects {
		if p.IsArchived {
			continue
		}
		options = append(options, p.Name)
	}

	var chosen string
	if err := survey.AskOne(&survey.Select{
		Message: "Select a project",
		Options: options,
	}, &chosen); err != nil {
		return nil, false, err
	}

	if chosen != createProjectOption {
		for i := range projects {
			if projects[i].Name == chosen {
				rep.ok("project %s (%s)", projects[i].Name, projects[i].ProjectID)
				return &projects[i], false, nil
			}
		}
		return nil, false, errors.New("no project selected")
	}

	var name string
	if err := survey.AskOne(&survey.Input{Message: "Project name"}, &name,
		survey.WithValidator(survey.Required)); err != nil {
		return nil, false, err
	}
	var description string
	if err := survey.AskOne(&survey.Input{Message: "Description (optional)"}, &description); err != nil {
		return nil, false, err
	}
	created, err := client.CreateProject(state.bearer, strings.TrimSpace(name), strings.TrimSpace(description))
	if err != nil {
		return nil, false, err
	}
	rep.ok("created project %s (%s)", created.Name, created.ProjectID)
	return created, true, nil
}

// ============================================================================
// Step 3 — API key
// ============================================================================

// resolveAPIKey returns the summary for the emitted payload and, when it minted
// one, the raw token so the caller can verify with it.
func resolveAPIKey(rep *reporter, client *auth.Client, state *authState, opts *setupOptions, project *auth.Project) (map[string]any, string, error) {
	info := map[string]any{"minted": false, "profile": auth.ActiveProfile()}

	if state.suppliedKey != "" {
		rep.ok("key already configured (profile: %s) — skipping mint", auth.ActiveProfile())
		return info, "", nil
	}

	if opts.interactive {
		mint := true
		if err := survey.AskOne(&survey.Confirm{
			Message: "Mint a project-scoped API key now?",
			Default: true,
		}, &mint); err != nil {
			return nil, "", err
		}
		if !mint {
			rep.ok("skipped minting an API key")
			return info, "", nil
		}
	}

	// Plain ASCII and no punctuation beyond spaces and hyphens: the name is
	// echoed back by the dashboard and we do not know its validation rules.
	hostname, _ := os.Hostname()
	hostname = strings.TrimSuffix(hostname, ".local")
	if hostname == "" {
		hostname = "unknown-host"
	}
	keyName := sanitizeKeyName(fmt.Sprintf("orq-cli %s %s", hostname, project.Name))

	token, scopedToProject, err := client.CreateAPIKey(state.bearer, keyName, project.ProjectID)
	if err != nil {
		return nil, "", err
	}
	// The raw token is returned once. Persist before doing anything else so a
	// later failure cannot leave a live key with no local record of it.
	if err := saveAPIKeyProfile(token); err != nil {
		return nil, "", fmt.Errorf("minted a key but could not save it: %w", err)
	}
	info["minted"] = true
	info["project_scoped"] = scopedToProject
	if scopedToProject {
		rep.ok("minted key  %s  (scoped to %s)", maskToken(token), project.Name)
	} else {
		// Do not let a broader-than-requested credential pass silently.
		rep.ok("minted key  %s", maskToken(token))
		rep.warn("this key covers the whole workspace, not just %s", project.Name)
		rep.note("  the API cannot scope a key to this project yet — see 'orq doctor'")
	}
	rep.ok("saved       %s", filepath.Join(viper.GetString("config-directory"), "credentials.json"))

	if opts.noEnv || opts.global {
		return info, token, nil
	}
	if opts.interactive {
		writeEnv := true
		if err := survey.AskOne(&survey.Confirm{
			Message: "Write ORQ_API_KEY to ./.env?",
			Default: true,
		}, &writeEnv); err != nil {
			return nil, "", err
		}
		if !writeEnv {
			return info, token, nil
		}
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
			rep.warn("  it differs from the key just minted — update it by hand if agents get 401s")
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
	count, err := countEnabledModels(client, state)
	if err != nil {
		rep.warn("could not list gateway models: %v", err)
		return 0
	}
	if count > 0 {
		rep.ok("%d model(s) enabled on the gateway", count)
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
	count, err = countEnabledModels(client, state)
	if err != nil {
		rep.warn("could not list gateway models: %v", err)
		return 0
	}
	if count == 0 {
		rep.warn("still no models enabled — continuing, but agents will have none to use")
		return 0
	}
	rep.ok("%d model(s) enabled on the gateway", count)
	return count
}

// countEnabledModels retries because this runs right after minting a key, and
// a fresh key is rejected for a second or two — without the wait the step
// reports "no provider connected" for a workspace that has one.
func countEnabledModels(client *auth.Client, state *authState) (int, error) {
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
	enabled := 0
	for _, m := range models {
		if m.Active {
			enabled++
		}
	}
	return enabled, nil
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

	results := make([]agentResult, 0, len(selected))
	sharedInstalled := false

	for _, id := range selected {
		spec, ok := lookupAgent(id)
		if !ok {
			rep.fail("%s is not a supported agent (%s)", id, strings.Join(agentIDs(), ", "))
			results = append(results, agentResult{Agent: id, Error: "unsupported agent"})
			continue
		}
		res := agentResult{Agent: id}

		// MCP registration.
		configPath, err := spec.mcpConfig(opts.global)
		switch {
		case err != nil:
			rep.fail("%-8s %v", id, err)
			res.Error = err.Error()
		case configPath == "":
			rep.note("%-8s no MCP support in this agent — installing skills only", id)
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
				note := ""
				if id == "codex" {
					note = "  (codex is global-only)"
				}
				rep.ok("%-8s MCP     %s → %s%s", id, configPath, mcpServerName, note)
				res.MCP = configPath
			}
		}

		// Provider registration, so the agent's own model calls go through orq.
		if spec.writeProvider != nil {
			if path, perr := spec.providerConfig(opts.global); perr == nil && path != "" {
				models := codingModels(rep, client, state)
				if werr := spec.writeProvider(path, client.RouterBaseURL(), state.bearer, models); werr != nil {
					rep.warn("%-8s provider  %v", id, werr)
				} else {
					rep.ok("%-8s provider  %s → orq gateway (%d models)", id, path, len(models))
					res.Provider = path
					res.ModelCount = len(models)
				}
			}
		}

		// Skills: agent-specific directory, or the shared one written once.
		dest, isShared, err := skillsDestination(spec, opts.global)
		if err != nil {
			rep.warn("%-8s could not resolve a skills directory: %v", id, err)
			results = append(results, res)
			continue
		}
		if isShared && sharedInstalled {
			res.Skills = dest
			results = append(results, res)
			continue
		}
		var stopSpin func()
		if !skillsCacheFresh() {
			stopSpin = rep.busy("%-8s skills  downloading…", id)
		}
		count, err := installSkillsInto(dest)
		if stopSpin != nil {
			stopSpin()
		}
		if err != nil {
			rep.warn("%-8s skills  %v", id, err)
		} else {
			label := id
			if isShared {
				label = "shared"
				sharedInstalled = true
			}
			rep.ok("%-8s skills  %s  %d skills", label, dest, count)
			res.Skills = dest
			res.SkillCount = count
		}
		results = append(results, res)
	}
	return results
}

// codingModels fetches the gateway catalogue once per run and narrows it to the
// preferred coding models that are actually on offer.
var cachedCodingModels []auth.RouterModel
var codingModelsFetched bool

func codingModels(rep *reporter, client *auth.Client, state *authState) []auth.RouterModel {
	if codingModelsFetched {
		return cachedCodingModels
	}
	codingModelsFetched = true

	stopSpin := rep.busy("probing gateway models for working candidates…")

	all, err := client.ListModels(state.bearer)
	if err != nil {
		stopSpin()
		rep.warn("could not list gateway models: %v", err)
		return nil
	}

	// The catalogue advertises models that return 500 on use, so take the best
	// candidate per family that actually answers. One tiny call each.
	picked := []auth.RouterModel{}
	skipped := 0
	for _, group := range auth.CandidateCodingModels(all, preferredCodingModels) {
		for _, candidate := range group {
			if client.ProbeModel(state.bearer, candidate.Ref()) {
				picked = append(picked, candidate)
				break
			}
			skipped++
		}
	}
	stopSpin()
	// Reported at warn level so the omission is visible even in quiet mode —
	// silently dropping models would read as "these are all that exist".
	if skipped > 0 {
		rep.warn("%d catalogue model(s) did not respond and were left out", skipped)
	}
	cachedCodingModels = picked
	return cachedCodingModels
}

func skillsDestination(spec agentSpec, global bool) (string, bool, error) {
	if spec.skillsDir == nil {
		if global {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", true, err
			}
			return filepath.Join(home, sharedSkillsDir), true, nil
		}
		return sharedSkillsDir, true, nil
	}
	dir, err := spec.skillsDir(global)
	return dir, false, err
}

// installSkillsInto fetches the skills archive once per process and copies it
// into dest.
func installSkillsInto(dest string) (int, error) {
	src, err := fetchSkills()
	if err != nil {
		return 0, err
	}
	return installSkills(src, dest)
}

func promptForAgents(rep *reporter) ([]string, error) {
	registry := agentRegistry()
	options := make([]string, 0, len(registry))
	byOption := map[string]string{}
	defaults := []string{}

	detected := []string{}
	for _, spec := range registry {
		label := fmt.Sprintf("%-9s %s", spec.ID, spec.Label)
		if spec.ID == "pi" {
			label += "  (skills only — pi has no MCP support)"
		}
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

func printFinalScreen(rep *reporter, agents []agentResult, links map[string]string, routerBase string, opts *setupOptions) {
	if opts.noInput {
		return
	}
	w := bartolocli.Stderr
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", 64))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ✓ Setup complete")
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
	providerWired := false
	for _, a := range agents {
		if a.Error == "" && a.Provider != "" {
			providerWired = true
		}
	}
	if providerWired && strings.TrimSpace(os.Getenv("ORQ_API_KEY")) == "" {
		fmt.Fprintln(w, "  ! agents read ORQ_API_KEY from the environment; it is not set in this shell.")
		fmt.Fprintln(w, "    Add to your shell profile (key saved in ~/.orq/credentials.json):")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "      export ORQ_API_KEY=<your key>")
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
