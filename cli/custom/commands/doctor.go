package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
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
	var report bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect config, auth state, and endpoint reachability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if report {
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
			checks = append(checks, probeURL("api_base_url", client.URLs.APIBaseURL, ""))
			checks = append(checks, probeURL("auth_base_url", client.URLs.AuthBaseURL, ""))

			profileBearer := ""
			if inspect.Status == auth.StatusOK && !isTokenExpired(inspect.Session.BootstrapToken.ExpiresAt) {
				profileBearer = inspect.Session.BootstrapToken.Token
			}
			checks = append(checks, probeURL("profile_base_url", client.URLs.ProfileBaseURL, profileBearer))

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
			return emit(report)
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	cmd.Flags().BoolVar(&report, "report", false, "Print a pre-filled GitHub issue URL for filing a bug report")
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
	issueURL := "https://github.com/orq-ai/orq-cli/issues/new?title=" +
		url.QueryEscape("bug: ") + "&body=" + url.QueryEscape(body)
	return emit(map[string]any{
		"report_url": issueURL,
		"note":       "Open the URL to file a pre-filled bug report. Review the body before submitting.",
	})
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
		return []doctorCheck{{
			ID:      "session_file",
			Status:  "warn",
			Message: "No local session file found",
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

func probeURL(id, url, bearer string) doctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	return doctorCheck{
		ID:      id,
		Status:  "pass",
		Message: fmt.Sprintf("Reachable (HTTP %d)", res.StatusCode),
		Details: map[string]any{"url": url, "http_status": res.StatusCode},
	}
}
