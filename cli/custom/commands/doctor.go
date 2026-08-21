package commands

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
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
	var apiBase string
	var bugReport bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect config, auth state, and endpoint reachability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if bugReport {
				return emitBugReport(cmd)
			}
			inspect := auth.InspectSession()

			apiBaseSource := "default"
			switch {
			case apiBase != "":
				apiBaseSource = "flag"
			case inspect.Status == auth.StatusOK:
				apiBaseSource = "session"
			case os.Getenv("ORQ_API_BASE_URL") != "":
				apiBaseSource = "env"
			}

			resolvedAPIBase := apiBase
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
			checks = append(checks, codingAgentChecks()...)
			if shadow, ok := gatewayKeyShadowsSessionCheck(inspect); ok {
				checks = append(checks, shadow)
			}
			if expiry, ok := gatewayKeyExpiryCheck(time.Now()); ok {
				checks = append(checks, expiry)
			}
			checks = append(checks, probeURL(cmd.Context(), "api_base_url", client.URLs.APIBaseURL, ""))
			checks = append(checks, probeURL(cmd.Context(), "auth_base_url", client.URLs.AuthBaseURL, ""))

			// Only meaningful for a login session: the profile endpoint answers
			// "who is this user", and a workspace API key is not a user — it
			// 401s there by design. Probing it unauthenticated was worse than
			// useless (a guaranteed 401 reported as a green "Reachable"), and
			// probing it with a key would fail every API-key setup.
			if inspect.Status == auth.StatusOK && !isTokenExpired(inspect.Session.BootstrapToken.ExpiresAt) {
				checks = append(checks, probeURL(cmd.Context(), "profile_base_url",
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
					"name":    "orq",
					"version": bartolocli.Root.Version,
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
			if wantsHumanView(cmd) {
				printDoctorSummary(authStatus, userEmail, checks)
				return nil
			}
			return emit(report)
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().BoolVar(&bugReport, "report", false, "Print a pre-filled GitHub issue URL for filing a bug report")
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
			"- platform: %s/%s\n"+
			"- go runtime: %s\n"+
			"- profile: %s\n\n"+
			"### What happened\n\n<!-- steps to reproduce, actual output -->\n\n"+
			"### What you expected\n",
		cmd.Root().Version, runtime.GOOS, runtime.GOARCH, runtime.Version(), auth.ActiveProfile(),
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
	printed := checks[:0:0]
	width := 16
	for _, c := range checks {
		if strings.HasPrefix(c.ID, "coding_agent_") && (c.Status == "pass" || c.Status == "info") {
			continue
		}
		printed = append(printed, c)
		if n := utf8.RuneCountInString(c.ID); n > width {
			width = n
		}
	}
	// Header row: 5-space gutter matches "  <glyph>  " before the label column.
	fmt.Fprintf(out, "     %s  %s\n", paint(ansiDim, pad("CHECK", width)), paint(ansiDim, "RESULT"))
	fmt.Fprintf(out, "  %s  %s  %s\n", statusGlyph(authStatusToCheck(authStatus)), pad("auth", width), authLine)
	for _, c := range printed {
		fmt.Fprintf(out, "  %s  %s  %s\n", statusGlyph(c.Status), pad(c.ID, width), c.Message)
	}
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
func probeURL(parent context.Context, id, url, bearer string) doctorCheck {
	authenticated := bearer != ""
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return doctorCheck{
			ID:      id,
			Status:  "fail",
			Message: err.Error(),
			Details: map[string]any{"url": url},
		}
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
