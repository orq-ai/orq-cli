package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type doctorCheck struct {
	ID      string         `json:"id"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type resolvedValue struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type doctorReport struct {
	Binary  map[string]string `json:"binary"`
	Runtime map[string]string `json:"runtime"`
	Output  map[string]any    `json:"output"`
	Config  map[string]any    `json:"config"`
	Auth    map[string]any    `json:"auth"`
	Checks  []doctorCheck     `json:"checks"`
}

func NewDoctorCommand() *cobra.Command {
	var bugReport bool
	var fixPerms bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect config, auth state, and endpoint reachability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if bugReport && fixPerms {
				return errors.New("--report and --fix cannot be used together")
			}
			if bugReport {
				return emitBugReport(cmd)
			}
			if fixPerms && runtime.GOOS == "windows" {
				return errors.New("--fix repairs Unix permission bits, which Windows ACLs do not use — there is nothing here for it to change")
			}
			inspect := auth.InspectSession()

			// Provenance comes from where the value was decided, not from
			// comparing it afterwards: ORQ_SERVER usually holds exactly the
			// host the session was authenticated against, and string equality
			// would report that as the session's doing.
			apiBaseSource := auth.ServerSource()

			resolvedAPIBase := serverURL()
			if resolvedAPIBase == "" && inspect.Status == auth.StatusOK {
				resolvedAPIBase = inspect.Session.APIBaseURL
			}
			client := auth.NewClient(resolvedAPIBase)

			v1Source := "derived"
			switch {
			case os.Getenv("ORQ_V1_BASE_URL") != "":
				v1Source = "env"
			case inspect.Status == auth.StatusOK && inspect.Session.V1BaseURL == client.URLs.V1BaseURL:
				v1Source = "session"
			}
			profileSource := "derived"
			switch {
			case os.Getenv("ORQ_PROFILE_BASE_URL") != "":
				profileSource = "env"
			case inspect.Status == auth.StatusOK && inspect.Session.ProfileBaseURL == client.URLs.ProfileBaseURL:
				profileSource = "session"
			}

			checks := buildSessionChecks(inspect)
			// Coding-agent wiring rides in the same doctor rather than its own
			// subcommand: a user with a broken setup should not need to know
			// which doctor to run. All local stat + parse, so unconditional.
			checks = append(checks, codingAgentChecks(activeWorkspaceKey(inspect.Session))...)
			// Same reasoning as the agent wiring above, and the same cost:
			// a manifest read plus one Lstat per recorded link.
			if sk, ok := skillsCheck(); ok {
				checks = append(checks, sk)
			}
			if mcp, ok := mcpCheck(); ok {
				checks = append(checks, mcp)
			}
			if shadow, ok := gatewayKeyShadowsSessionCheck(inspect); ok {
				checks = append(checks, shadow)
			}
			if expiry, ok := gatewayKeyExpiryCheck(time.Now()); ok {
				checks = append(checks, expiry)
			}
			var permsErr error
			if perms, ok, err := credentialPermsCheck(fixPerms); ok {
				checks = append(checks, perms)
				permsErr = err
			}
			checks = append(checks, probeURL(cmd.Context(), "api_base_url", http.MethodGet, client.URLs.APIBaseURL, ""))
			checks = append(checks, probeURL(cmd.Context(), "auth_base_url", http.MethodGet, client.URLs.AuthBaseURL, ""))

			// Only meaningful for a login session: the profile endpoint answers
			// "who is this user", and a workspace API key is not a user — it
			// 401s there by design. Probing it unauthenticated was worse than
			// useless (a guaranteed 401 reported as a green "Reachable"), and
			// probing it with a key would fail every API-key setup.
			if inspect.Status == auth.StatusOK && !isTokenExpired(inspect.Session.BootstrapToken.ExpiresAt) {
				checks = append(checks, probeURL(cmd.Context(), "profile_base_url", http.MethodPost,
					client.URLs.ProfileBaseURL, inspect.Session.BootstrapToken.Token))
			}

			authStatus := string(inspect.Status)
			authSource := "none"
			var userEmail string
			var activeWS any
			workspaceCount := 0
			if inspect.Status == auth.StatusOK {
				authStatus = "authenticated"
				authSource = "session-file"
				if inspect.Session.User != nil {
					userEmail = inspect.Session.User.Email
				}
				activeWS = inspect.Session.ActiveWorkspaceKey
				workspaceCount = len(inspect.Session.Workspaces)
			} else if envAPIKeySet() {
				// No session, but an env key authenticates every command. Saying
				// "missing" here sends people hunting for a login problem that
				// does not exist.
				authStatus, authSource = "authenticated", "env:ORQ_API_KEY"
			} else if storedAPIKeyProfile() {
				authStatus, authSource = "authenticated", "credentials.json"
			}

			authMap := map[string]any{
				"status":               authStatus,
				"source":               authSource,
				"user_email":           userEmail,
				"active_workspace_key": activeWS,
				"workspace_count":      workspaceCount,
			}
			if inspect.Status != auth.StatusOK && inspect.Status != auth.StatusMissing {
				authMap["session_error"] = map[string]any{
					"code":    inspect.Code,
					"message": inspect.Message,
				}
			}

			report := doctorReport{
				Binary: map[string]string{
					"name":        "orq",
					"version":     bartolocli.Root.Version,
					"api_version": apiVersion,
				},
				Runtime: map[string]string{
					"name":     "go",
					"version":  runtime.Version(),
					"platform": runtime.GOOS,
					"arch":     runtime.GOARCH,
				},
				Output: map[string]any{
					"default_format":    "toon",
					"supported_formats": []string{"json", "yaml", "toon"},
				},
				Config: map[string]any{
					"profile":          auth.ActiveProfile(),
					"session_file":     auth.SessionFilePath(),
					"api_base_url":     resolvedValue{Value: client.URLs.APIBaseURL, Source: apiBaseSource},
					"v1_base_url":      resolvedValue{Value: client.URLs.V1BaseURL, Source: v1Source},
					"auth_base_url":    resolvedValue{Value: client.URLs.AuthBaseURL, Source: "derived"},
					"profile_base_url": resolvedValue{Value: client.URLs.ProfileBaseURL, Source: profileSource},
				},
				Auth:   authMap,
				Checks: checks,
			}
			// A person at a terminal gets the scannable colored checklist; the
			// full structured report is verbose diagnostic data meant for
			// machines and for `--json`/`-o`. Scripts (non-TTY) and an explicit
			// format request always get the structured report.
			// The report goes out first either way: a failed --fix has to name
			// which path it could not repair before the error ends the run.
			if wantsHumanView(cmd) {
				printDoctorSummary(authStatus, userEmail, checks)
				return permsErr
			}
			if err := emit(report); err != nil {
				return err
			}
			return permsErr
		},
	}
	DeprecatedAPIBaseFlag(cmd)
	cmd.Flags().BoolVar(&bugReport, "report", false, "Print a pre-filled GitHub issue URL for filing a bug report")
	// Registered on every platform so the single platform-neutral surface
	// manifest stays true; on Windows the flag is rejected at run time.
	cmd.Flags().BoolVar(&fixPerms, "fix", false, "Chmod the credential paths the permissions check flags (0600 files, 0700 directories)")
	return cmd
}

// emitBugReport prints a GitHub new-issue URL pre-filled with the environment
// details maintainers always have to ask for. Only non-sensitive facts go in:
// version, platform, profile name — never tokens, emails, or URLs from the
// session file.
func emitBugReport(cmd *cobra.Command) error {
	body := fmt.Sprintf(
		"### Environment\n\n"+
			"- orq version: %s\n"+
			"- orq API: %s\n"+
			"- platform: %s/%s\n"+
			"- go runtime: %s\n"+
			"- profile: %s\n\n"+
			"### What happened\n\n<!-- steps to reproduce, actual output -->\n\n"+
			"### What you expected\n",
		cmd.Root().Version, apiVersion, runtime.GOOS, runtime.GOARCH, runtime.Version(), auth.ActiveProfile(),
	)
	// Leave the title empty so GitHub shows its placeholder and the user writes
	// a real one; a literal "bug: " prefill just becomes the issue title verbatim.
	issueURL := "https://github.com/orq-ai/orq-cli/issues/new?body=" + url.QueryEscape(body)
	if wantsHumanView(cmd) {
		// The flag help promises "a pre-filled GitHub issue URL"; a person wants
		// the URL to open, not a structured object to parse.
		out := bartolocli.Stdout
		fmt.Fprintln(out, "Open this URL to file a pre-filled bug report (review the body before submitting):")
		fmt.Fprintln(out, issueURL)
		return nil
	}
	return emit(map[string]any{
		"report_url": issueURL,
		"note":       "Open the URL to file a pre-filled bug report. Review the body before submitting.",
	})
}

// printDoctorSummary is the scannable, colored checklist a person sees at a
// terminal. It is the primary output in that mode (the verbose structured
// report is reserved for scripts and --json/-o), so it writes to stdout.
func printDoctorSummary(authStatus, userEmail string, checks []doctorCheck) {
	out := bartolocli.Stdout
	authLine := authStatus
	if authStatus == "authenticated" && userEmail != "" {
		authLine = "authenticated as " + userEmail
	}
	// Healthy per-agent rows collapse into the coding_agents summary; only
	// fault rows earn their own line. --json keeps every row.
	rows := []tableRow{{marker: statusGlyph(authStatusToCheck(authStatus)), cells: []string{"auth", authLine}}}
	for _, c := range checks {
		// A clean credential-permissions check is retained in structured
		// output so automation can distinguish "checked" from "skipped",
		// but stays silent in the compact human checklist.
		if c.ID == "credential_permissions" && c.Status == "pass" {
			continue
		}
		if strings.HasPrefix(c.ID, "coding_agent_") && (c.Status == "pass" || c.Status == "info") {
			continue
		}
		rows = append(rows, tableRow{marker: statusGlyph(c.Status), cells: []string{c.ID, c.Message}})
	}
	printTable(out, []string{"CHECK", "RESULT"}, rows)
	fmt.Fprintln(out, paint(ansiDim, "\nRun `orq doctor --json` for full details."))
}

func authStatusToCheck(status string) string {
	switch status {
	case "authenticated":
		return "pass"
	case "missing":
		return "warn"
	default:
		return "fail"
	}
}

func buildSessionChecks(inspect auth.SessionInspectResult) []doctorCheck {
	switch inspect.Status {
	case auth.StatusOK:
		bootstrapExpired := isTokenExpired(inspect.Session.BootstrapToken.ExpiresAt)
		bootstrapStatus := "pass"
		bootstrapMsg := "Bootstrap token is present"
		if bootstrapExpired {
			bootstrapStatus = "warn"
			bootstrapMsg = "Bootstrap token is expired and will need refresh"
		}
		return []doctorCheck{
			{
				ID:      "session_file",
				Status:  "pass",
				Message: "Session file loaded",
				Details: map[string]any{"session_file": inspect.Path},
			},
			{
				ID:      "bootstrap_token",
				Status:  bootstrapStatus,
				Message: bootstrapMsg,
				Details: map[string]any{"expires_at": inspect.Session.BootstrapToken.ExpiresAt},
			},
		}
	case auth.StatusMissing:
		// Not a problem when a key authenticates: ORQ_API_KEY with no session
		// is the standard CI shape, and warning about it sends people looking
		// for a login they never needed.
		status, message := "warn", "No local session file found"
		if envAPIKeySet() || storedAPIKeyProfile() {
			status = "pass"
			message = "No session file (authenticated with an API key)"
		}
		return []doctorCheck{{
			ID:      "session_file",
			Status:  status,
			Message: message,
			Details: map[string]any{"session_file": inspect.Path},
		}}
	default:
		return []doctorCheck{{
			ID:      "session_file",
			Status:  "fail",
			Message: inspect.Message,
			Details: map[string]any{"session_file": inspect.Path, "code": inspect.Code},
		}}
	}
}

func isTokenExpired(expiresAt string) bool {
	if expiresAt == "" {
		return true
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000Z07:00", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, expiresAt); err == nil {
			return time.Now().Add(60 * time.Second).After(t)
		}
	}
	return true
}

// probeURL checks reachability with a 5s budget, parented on the command
// context so Ctrl+C cancels an in-flight probe instead of waiting it out.
// method matters for the profile RPC: Connect unary calls only answer POST, so
// probing it with a GET reports a 404/405 that says nothing about reachability.
func probeURL(parent context.Context, id, method, url, bearer string) doctorCheck {
	authenticated := bearer != ""
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return doctorCheck{
			ID:      id,
			Status:  "fail",
			Message: err.Error(),
			Details: map[string]any{"url": url},
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return doctorCheck{
			ID:      id,
			Status:  "fail",
			Message: err.Error(),
			Details: map[string]any{"url": url},
		}
	}
	defer res.Body.Close()
	// These probe base URLs, which mostly have no handler — the API root answers
	// 404 by design. What is being checked is the network path, so the message
	// says that rather than printing a status code the user reads as a failure.
	//
	// A 5xx means reachable but unhealthy; a green check there would read as
	// "all good" while the server is down. And a 401/403 when we DID send a
	// credential is the one case worth failing on: the endpoint is fine, the
	// credential is not — which is exactly what someone runs doctor to find out.
	status := "pass"
	message := "Reachable"
	switch {
	case res.StatusCode >= 500:
		status = "fail"
		message = fmt.Sprintf("Reachable but returned a server error (HTTP %d)", res.StatusCode)
	case authenticated && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden):
		status = "fail"
		message = fmt.Sprintf("Reachable, but the credential was rejected (HTTP %d)", res.StatusCode)
	}
	return doctorCheck{
		ID:      id,
		Status:  status,
		Message: message,
		Details: map[string]any{"url": url, "http_status": res.StatusCode},
	}
}

// mcpCheck reports detected MCP-capable agents and the OAuth command their
// users must run. An entry is only the wiring state: doctor cannot know
// whether the agent's OAuth flow has completed.
//
// wiredPath probes both project and machine-wide scopes. Pi is deliberately
// absent from the registry's MCP fields, so it is never reported here.
func mcpCheck() (doctorCheck, bool) {
	var present, missing []string
	for _, spec := range agentRegistry() {
		if !spec.detect() || spec.writeMCP == nil || spec.mcpConfig == nil || spec.mcpPresent == nil {
			continue
		}
		if _, wired := wiredPath(spec.mcpConfig, spec.mcpPresent); wired {
			present = append(present, spec.ID)
		} else {
			missing = append(missing, spec.ID)
		}
	}
	if len(present) == 0 && len(missing) == 0 {
		return doctorCheck{}, false
	}

	check := doctorCheck{
		ID: "mcp",
		Details: map[string]any{
			"detected":       len(present) + len(missing),
			"present":        len(present),
			"missing":        len(missing),
			"present_agents": present,
			"missing_agents": missing,
		},
	}
	var messages []string
	for _, id := range present {
		messages = append(messages, fmt.Sprintf("%s MCP entry present — %s", id, mcpLoginLine(id)))
	}
	if len(missing) > 0 {
		check.Status = "warn"
		for _, id := range missing {
			messages = append(messages, fmt.Sprintf("%s detected without an MCP entry — run 'orq connect %s mcp'", id, id))
		}
	} else {
		check.Status = "pass"
	}
	check.Message = strings.Join(messages, "; ")
	return check, true
}

// gatewayKeyShadowsSessionCheck names the one state the credential split can
// still land a user in: the minted key is gateway-scoped, so exporting it makes
// every `orq <entity>` command authenticate with a key that cannot reach the
// platform, in a shell where the login would have worked.
func gatewayKeyShadowsSessionCheck(inspect auth.SessionInspectResult) (doctorCheck, bool) {
	if inspect.Status != auth.StatusOK {
		return doctorCheck{}, false
	}
	gatewayKey := strings.TrimSpace(bartolocli.Creds.GetString("profiles." + auth.ActiveProfile() + ".gateway_key"))
	exported := strings.TrimSpace(UserEnvAPIKey())
	if gatewayKey == "" || exported != gatewayKey {
		return doctorCheck{}, false
	}
	return doctorCheck{
		ID:      "gateway_key_exported",
		Status:  "warn",
		Message: "ORQ_API_KEY in this shell is the gateway-scoped key, so commands like 'orq prompts list' will be refused. Run 'unset ORQ_API_KEY' to use your login instead",
	}, true
}

// gatewayKeyExpiryCheck counts down to the minted key's expiry. Wired agents
// hold that key, so the day it lapses every one of them 401s at once; the row
// exists so that day is never a surprise. Absent when no expiry is recorded,
// which is a key minted before expiry existed rather than one that never
// expires.
func gatewayKeyExpiryCheck(now time.Time) (doctorCheck, bool) {
	at, ok := gatewayKeyExpiry()
	if !ok {
		return doctorCheck{}, false
	}
	// Round up: 23 hours left is "1 day", not "0 days" at warn.
	days := int(math.Ceil(at.Sub(now).Hours() / 24))
	check := doctorCheck{
		ID:      "gateway_key_expiry",
		Details: map[string]any{"expires_at": at.UTC().Format(time.RFC3339), "days_left": days},
	}
	switch {
	case !at.After(now):
		check.Status = "fail"
		check.Message = "The gateway key expired — wired agents are failing to authenticate. Run 'orq setup' to replace it, then 'orq connect' to rewire"
	case at.Sub(now) < gatewayKeyRenewWindow:
		check.Status = "warn"
		check.Message = fmt.Sprintf("The gateway key expires in %d days — run 'orq setup' to replace it, then 'orq connect' to rewire", days)
	default:
		check.Status = "pass"
		check.Message = fmt.Sprintf("The gateway key expires in %d days", days)
	}
	return check, true
}

// credPermClass is what a candidate path holds, which decides both the mode
// it should carry and the advice a leak earns.
type credPermClass int

const (
	credClassDir credPermClass = iota
	credClassAPIKey
	credClassSession
)

// credPermOutcome is the one thing worth saying about a candidate path.
type credPermOutcome int

const (
	credPermLoose credPermOutcome = iota
	credPermRepaired
	credPermFixFailed
	credPermWrongType
	credPermUnreadable
)

type credPermCandidate struct {
	path  string
	class credPermClass
}

// credPermResult is one candidate's finding. humanPath is the tilde'd,
// shell-quoted form printed in messages; path stays the raw absolute path for
// --json's Details.
type credPermResult struct {
	path      string
	humanPath string
	realPath  string // resolved target, set only when path is a symlink
	humanReal string
	class     credPermClass
	mode      os.FileMode
	want      os.FileMode
	outcome   credPermOutcome
	found     string // what the path turned out to be, for credPermWrongType
	err       error
}

// shellQuotePath renders path for safe pasting into a shell command. Most
// paths need nothing; a home directory with a space or shell metacharacter
// would otherwise break the printed `chmod` the moment it is pasted.
func shellQuotePath(path string) string {
	// A quoted ~ is a literal directory name, so quoting the whole path turns
	// every printed chmod into one that cannot find the file.
	prefix := ""
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		prefix, path = "~/", rest
	}
	if !strings.ContainsAny(path, " \t\n'\"\\$`!*?[]{}()<>|;&~") {
		return prefix + path
	}
	return prefix + "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func describeFileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "a directory"
	case mode.IsRegular():
		return "a regular file"
	case mode&os.ModeNamedPipe != 0:
		return "a FIFO"
	case mode&os.ModeSocket != 0:
		return "a socket"
	case mode&os.ModeDevice != 0:
		return "a device"
	default:
		return "not a regular file"
	}
}

// display is the path as messages name it. A symlinked credential path is
// judged and repaired on its target, so the target has to appear too:
// otherwise --fix chmods a file whose name never reaches the user.
func (r credPermResult) display() string {
	if r.humanReal == "" {
		return r.humanPath
	}
	return r.humanPath + " (a symlink to " + r.humanReal + ")"
}

// resolveCredPath returns the target of a symlinked candidate, or "" when the
// path is not a symlink or cannot be resolved. Reporting only: the judgement
// and the repair go through the descriptor inspectCredPath already holds.
func resolveCredPath(path string) string {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real == path {
		return ""
	}
	return real
}

// inspectCredPath judges one candidate and, when fix is set, repairs it
// through the very descriptor it judged: a path-based chmod in a second pass
// would act on whatever the path resolves to by then, which a swapped symlink
// or parent directory can change between the two. Returns nil when there is
// nothing to report; judged says whether the mode could be read at all.
func inspectCredPath(c credPermCandidate, fix bool) (result *credPermResult, judged bool) {
	r := credPermResult{
		path:      c.path,
		humanPath: shellQuotePath(tilde(c.path)),
		class:     c.class,
		want:      0o600,
	}
	wantDir := c.class == credClassDir
	if wantDir {
		r.want = 0o700
	}
	// O_NONBLOCK so a FIFO left where a credential file belongs cannot block
	// the open waiting for a writer. Opening follows symlinks — a dotfile
	// manager's link is judged and repaired on its target, which is the file
	// the CLI actually reads.
	f, err := os.OpenFile(c.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// Missing (including a broken symlink) is not a finding: nothing to leak.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		r.outcome, r.err = credPermUnreadable, err
		return &r, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		r.outcome, r.err = credPermUnreadable, err
		return &r, false
	}
	expected := info.Mode().IsRegular()
	if wantDir {
		expected = info.IsDir()
	}
	if !expected {
		// Silence means the tree is clean, so a credential path that is a
		// directory, FIFO or device cannot simply be skipped.
		r.outcome, r.found = credPermWrongType, describeFileType(info.Mode())
		return &r, false
	}

	r.mode = info.Mode().Perm()
	if r.mode&0o077 == 0 {
		return nil, true
	}
	r.outcome = credPermLoose
	if real := resolveCredPath(c.path); real != "" {
		r.realPath, r.humanReal = real, shellQuotePath(tilde(real))
	}
	if fix {
		if err := credPermChmod(f, r.want); err != nil {
			r.outcome, r.err = credPermFixFailed, err
		} else {
			r.outcome = credPermRepaired
		}
	}
	return &r, true
}

// exposedAPIKeyAdvice is the revoke-and-rotate advice for a workspace API key
// that was readable by other local accounts. Shared with `orq setup`, which
// repairs such a file silently and would otherwise erase the evidence before
// doctor could ever report it. The chmod does not un-expose it: what was
// readable stays compromised until the key is revoked.
//
// keyID names the key to revoke, and callers that cannot know it pass "".
// `orq setup` is such a caller: it has already persisted the key it just
// minted by the time it rewrites the env file, so the saved ID there is the
// new key, not the exposed one it is replacing.
func exposedAPIKeyAdvice(keyID string) string {
	revoke := "revoke it in the orq dashboard"
	if keyID != "" {
		revoke = fmt.Sprintf("revoke it with 'orq api-keys delete %s'", keyID)
	}
	return "treat the exposed API key as compromised: " + revoke
}

// credPermChmod is the --fix repair, indirected so a test can make it fail
// without needing a path chmod can refuse on every platform the tests run on.
var credPermChmod = func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }

// credentialPermsCheck reports credential files and directories left with
// group/other permission bits set. Bartolo v0.6.0 already writes every
// credentials.json as 0600, so a loose file here is leftover from an older
// CLI version rather than something this CLI would produce today. Repair is
// opt-in: doctor chmods nothing unless the caller passed --fix.
//
// Unix only: Windows ACLs do not map onto the Unix permission bits this
// check reads, so the check is absent there rather than reporting a
// meaningless pass.
func credentialPermsCheck(fix bool) (doctorCheck, bool, error) {
	if runtime.GOOS == "windows" {
		return doctorCheck{}, false, nil
	}
	dir := viper.GetString("config-directory")
	if dir == "" {
		return doctorCheck{}, false, nil
	}
	if _, err := os.UserHomeDir(); err != nil {
		// auth builds a relative `.orq/...` when the home directory cannot be
		// resolved; auditing — let alone chmodding — whatever sits under the
		// working directory is not what this check is for.
		return doctorCheck{}, false, nil
	}

	candidates := []credPermCandidate{
		{dir, credClassDir},
		{filepath.Join(dir, "credentials.json"), credClassAPIKey},
	}
	for _, name := range shellEnvFileNames {
		candidates = append(candidates, credPermCandidate{filepath.Join(dir, name), credClassAPIKey})
	}
	// auth.SessionsDir/LegacySessionFilePath are the package that owns the
	// session layout; deriving the directory from them (rather than
	// hardcoding it here) is what makes this check track that layout instead
	// of silently drifting from it if it ever changes.
	sessionsDir := auth.SessionsDir()
	candidates = append(candidates, credPermCandidate{sessionsDir, credClassDir})
	candidates = append(candidates, credPermCandidate{auth.LegacySessionFilePath(), credClassSession})

	var results []credPermResult
	checked := 0

	// ReadDir can return entries *and* an error; the entries it did read are
	// still worth auditing, and the truncated listing is its own finding —
	// reporting "clean" over files this check never saw would be a lie.
	entries, err := os.ReadDir(sessionsDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			candidates = append(candidates, credPermCandidate{filepath.Join(sessionsDir, entry.Name()), credClassSession})
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		results = append(results, credPermResult{
			path:      sessionsDir,
			humanPath: shellQuotePath(tilde(sessionsDir)),
			class:     credClassDir,
			outcome:   credPermUnreadable,
			err:       err,
		})
	}

	for _, c := range candidates {
		r, judged := inspectCredPath(c, fix)
		if judged {
			checked++
		}
		if r != nil {
			results = append(results, *r)
		}
	}
	if len(results) == 0 {
		if checked == 0 {
			// Nothing existed to inspect, or the check could not establish a
			// safe home directory. Absence is preferable to claiming a clean
			// check that did not actually run.
			return doctorCheck{}, false, nil
		}
		return doctorCheck{ID: "credential_permissions", Status: "pass",
			Message: "Credential paths are not accessible by other accounts",
			Details: map[string]any{"checked": checked}}, true, nil
	}

	var repairedMsgs, looseMsgs, fixErrors, wrongTypeMsgs, unreadable []string
	looseDetails := make([]map[string]any, 0, len(results))
	fixedDetails := make([]map[string]any, 0, len(results))
	exposed := map[credPermClass]bool{}
	for _, r := range results {
		switch r.outcome {
		case credPermRepaired:
			exposed[r.class] = true
			repairedMsgs = append(repairedMsgs, fmt.Sprintf("%s was mode %04o — changed to %04o", r.display(), r.mode, r.want))
			fixed := map[string]any{
				"path":          r.path,
				"previous_mode": fmt.Sprintf("%04o", r.mode),
				"mode":          fmt.Sprintf("%04o", r.want),
			}
			if r.realPath != "" {
				fixed["resolved_path"] = r.realPath
			}
			fixedDetails = append(fixedDetails, fixed)
		case credPermLoose, credPermFixFailed:
			exposed[r.class] = true
			// chmod follows symlinks, so the path the user recognizes is also
			// the one that repairs the target.
			chmod := fmt.Sprintf("chmod %o %s", r.want, r.humanPath)
			loose := map[string]any{
				"path": r.path,
				"mode": fmt.Sprintf("%04o", r.mode),
				"fix":  chmod,
			}
			if r.realPath != "" {
				loose["resolved_path"] = r.realPath
			}
			looseDetails = append(looseDetails, loose)
			looseMsgs = append(looseMsgs, fmt.Sprintf("%s is mode %04o — run %s", r.display(), r.mode, chmod))
			if r.outcome == credPermFixFailed {
				fixErrors = append(fixErrors, fmt.Sprintf("%s could not be chmodded to %04o: %v", r.display(), r.want, r.err))
			}
		case credPermWrongType:
			want := "a regular file"
			if r.class == credClassDir {
				want = "a directory"
			}
			wrongTypeMsgs = append(wrongTypeMsgs, fmt.Sprintf("%s is %s, not %s — left untouched, inspect it by hand", r.display(), r.found, want))
			if fix {
				fixErrors = append(fixErrors, fmt.Sprintf("%s has the wrong type and could not be repaired", r.display()))
			}
		case credPermUnreadable:
			unreadable = append(unreadable, fmt.Sprintf("%s could not be inspected: %v", r.display(), r.err))
			if fix {
				fixErrors = append(fixErrors, fmt.Sprintf("%s could not be inspected", r.display()))
			}
		}
	}

	messages := make([]string, 0, len(results)+2)
	messages = append(messages, repairedMsgs...)
	messages = append(messages, looseMsgs...)
	messages = append(messages, fixErrors...)
	messages = append(messages, wrongTypeMsgs...)
	messages = append(messages, unreadable...)
	// A credential that leaked to other local accounts is compromised the
	// moment it was readable, not just when it is found — a chmod after the
	// fact does not un-expose it. What undoes it differs per credential:
	// `orq setup` reuses a saved, still-valid API key rather than rotating
	// it, and revokes no refresh token at all.
	if exposed[credClassAPIKey] {
		messages = append(messages, exposedAPIKeyAdvice(savedGatewayKeyID())+
			", then 'orq auth logout' to clear the local copy and 'orq setup' to mint a new one")
	}
	if exposed[credClassSession] {
		messages = append(messages, "treat the exposed session as compromised: run 'orq auth logout' to revoke the refresh token and clear the local copy")
	}

	details := map[string]any{"checked": checked, "loose": looseDetails}
	if len(fixedDetails) > 0 {
		details["fixed"] = fixedDetails
	}
	if len(fixErrors) > 0 {
		details["fix_errors"] = fixErrors
	}
	if len(wrongTypeMsgs) > 0 {
		details["invalid_type"] = wrongTypeMsgs
	}
	if len(unreadable) > 0 {
		details["unreadable"] = unreadable
	}
	status := "warn"
	if len(fixErrors) > 0 {
		status = "fail"
	}
	check := doctorCheck{
		ID:      "credential_permissions",
		Status:  status,
		Message: strings.Join(messages, "; "),
		Details: details,
	}
	// Only a --fix run that failed to repair something is an error: `orq
	// doctor` reports findings and exits 0, and a repair that did not happen
	// is a failed action, not a finding.
	if len(fixErrors) > 0 {
		return check, true, fmt.Errorf("--fix could not repair every flagged credential path: %s", strings.Join(fixErrors, "; "))
	}
	return check, true, nil
}
