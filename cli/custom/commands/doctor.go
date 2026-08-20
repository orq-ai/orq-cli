package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

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
			if funding, ok := gatewayFundingCheck(client, inspect); ok {
				checks = append(checks, funding)
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
	// Header row: 5-space gutter matches "  <glyph>  " before the label column.
	fmt.Fprintf(out, "     %s  %s\n", paint(ansiDim, pad("CHECK", 16)), paint(ansiDim, "RESULT"))
	fmt.Fprintf(out, "  %s  %s  %s\n", statusGlyph(authStatusToCheck(authStatus)), pad("auth", 16), authLine)
	for _, c := range checks {
		fmt.Fprintf(out, "  %s  %s  %s\n", statusGlyph(c.Status), pad(c.ID, 16), c.Message)
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

// gatewayFundingCheck reports whether the workspace can pay for a model call.
// A wired agent that 401s or 500s on every request looks identical to a broken
// install from the terminal, and this is the one check that tells them apart.
//
// Present only for a login session, and deliberately so: reading the balance
// needs credits.view, which the API-key capability catalogue cannot express, so
// an API-key run would get a 403 that means nothing about the workspace. Rather
// than render a row that says "unknown" on every non-session run, the row is
// absent. A session token does hold the permission, so an error on this path
// is a backend or install problem and gets a warn row rather than silence.
//
// Reuses the token already on disk instead of refreshing one: doctor is a
// diagnostic and must not write state, and a refresh persists new tokens.
func gatewayFundingCheck(client *auth.Client, inspect auth.SessionInspectResult) (doctorCheck, bool) {
	if inspect.Status != auth.StatusOK || inspect.Session == nil || inspect.Session.ActiveWorkspaceKey == nil {
		return doctorCheck{}, false
	}
	// storedWorkspaceToken only, never a refresh: refreshing persists a new
	// session, and a diagnostic must not write state to answer a question.
	token := storedWorkspaceToken(inspect.Session)
	if token == "" {
		return doctorCheck{}, false
	}
	balance, err := client.Credits(token)
	if err != nil {
		return doctorCheck{
			ID:      "gateway_funding",
			Status:  "warn",
			Message: fmt.Sprintf("could not read the credit balance: %v", err),
		}, true
	}
	if balance.Balance > 0 {
		return doctorCheck{
			ID:      "gateway_funding",
			Status:  "pass",
			Message: fmt.Sprintf("Gateway funded (%.2f %s in credits)", balance.Balance, strings.ToUpper(balance.Currency)),
			Details: map[string]any{"credits": balance.Balance},
		}, true
	}
	// Warn, not fail: a BYOK provider key serves calls with a zero balance, and
	// this endpoint cannot see BYOK. Reporting a working workspace as broken
	// would be the worse error.
	// Same builder setup uses, rather than a second spelling of the same URL:
	// the first attempt at this fell back to prose where setup falls back to
	// the docs page, which is how one link becomes two different links.
	credits := dashboardURL(inspect.Session.APIBaseURL, *inspect.Session.ActiveWorkspaceKey, creditsPath)
	return doctorCheck{
		ID:     "gateway_funding",
		Status: "warn",
		Message: "No credits — the gateway may refuse model calls unless a provider key (BYOK) is connected. " +
			"Add credits at " + credits,
		Details: map[string]any{"credits": balance.Balance, "credits_url": credits},
	}, true
}
