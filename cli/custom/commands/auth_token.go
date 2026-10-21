package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// tokenReport is `orq auth token -o json`: the browser login's workspace token
// and what it is scoped to, so a tool that reuses the login needs neither the
// session file's path nor its shape.
type tokenReport struct {
	Token       string `json:"token"`
	ExpiresAt   string `json:"expires_at"`
	Workspace   string `json:"workspace"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Server      string `json:"server"`
}

// NewTokenCommand prints the browser login's workspace access token. It is the
// supported way for another program (orqi, a script) to reuse `orq auth
// login`: where the session keeps its secrets is this CLI's business, and they
// can move out of the session file into the OS credential store.
func NewTokenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the access token of your browser login",
		Long: `Print the workspace access token of your browser login ('orq auth login'),
refreshing it first when it is expired or about to expire.

The token is scoped to the login's active workspace and project, or to the ones
--workspace and --project name. It lasts about an hour, so ask again rather
than storing it. With -o json the output also carries its expiry, workspace,
project and server.

API keys are not printed: a profile or an exported ORQ_API_KEY is already a
credential you hold. While one is in force, --workspace and --project do not
apply and the active pair's token is printed.`,
		Example: `  orq auth token
  curl -H "Authorization: Bearer $(orq auth token)" https://my.orq.ai/v2/projects
  orq auth token -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return auth.ErrNotLoggedIn
			}
			report, err := sessionTokenReport(cmd, session)
			if err != nil {
				return err
			}
			if machineFormatRequested(cmd) {
				return emit(report)
			}
			// Bare, so $(orq auth token) is the token and nothing else.
			_, err = fmt.Fprintln(bartolocli.Stdout, report.Token)
			return err
		},
	}
}

// sessionTokenReport finds the token this invocation would authenticate with.
// When no API key outranks the session, the root PreRun has already exchanged
// one into ORQ_API_KEY for the workspace and project this run resolved
// (--workspace, --project, else the active pair), so that one is reported.
// Otherwise, or when that token is near expiry, the active pair's token is
// fetched here, which exchanges a new one if needed.
func sessionTokenReport(cmd *cobra.Command, session *auth.Session) (*tokenReport, error) {
	server := sessionAPIBase(session)
	if !explicitAPIKey {
		if minted := strings.TrimSpace(os.Getenv("ORQ_API_KEY")); minted != "" {
			if fresh, err := auth.ReadSession(); err == nil && fresh != nil {
				for key, tok := range fresh.WorkspaceTokens {
					if tok.Token == minted && !expiresWithin(tok.ExpiresAt, time.Minute) {
						return newTokenReport(key, tok, server), nil
					}
				}
			}
		}
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey == "" {
		return nil, fmt.Errorf("no active workspace selected. Run `orq workspace use <key>` first")
	}
	workspace := *session.ActiveWorkspaceKey
	client := auth.NewClient(server).WithContext(cmd.Context()).WithProject(session.ActiveProjectID)
	token, err := client.WorkspaceToken(session, workspace)
	if err != nil {
		return nil, err
	}
	key := auth.TokenCacheKey(workspace, session.ActiveProjectID)
	tok := session.WorkspaceTokens[key]
	tok.Token = token
	return newTokenReport(key, tok, server), nil
}

// expiresWithin reports whether expiresAt is unreadable or less than d away.
// The PreRun can leave an inherited token in ORQ_API_KEY when its own exchange
// failed; this keeps that one from being handed out as fresh.
func expiresWithin(expiresAt string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	return err != nil || time.Until(t) < d
}

func newTokenReport(cacheKey string, tok auth.StoredAccessToken, server string) *tokenReport {
	workspace, project := auth.ParseTokenCacheKey(cacheKey)
	return &tokenReport{
		Token:       tok.Token,
		ExpiresAt:   tok.ExpiresAt,
		Workspace:   workspace,
		WorkspaceID: tok.WorkspaceID,
		ProjectID:   project,
		Server:      server,
	}
}
