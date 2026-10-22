package commands

import (
	"fmt"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// tokenReport is `orq auth token -o json`: the browser login's workspace token
// and what it is scoped to, so a tool that reuses the login needs neither the
// session file's path nor its shape.
type tokenReport struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Workspace string `json:"workspace"`
	ProjectID string `json:"project_id,omitempty"`
	Server    string `json:"server"`
}

// sessionScope is the workspace and project the root PreRun resolved for the
// session token (--workspace, --project, else the active pair). Both are empty
// when an API key is in force or no workspace is active.
var sessionScope struct{ workspace, project string }

// SetSessionScope is called by the root PreRun before it mints the session
// token, so `orq auth token` reports the same slot instead of re-deriving it.
func SetSessionScope(workspace, project string) {
	sessionScope.workspace, sessionScope.project = workspace, project
}

// NewTokenCommand prints the browser login's workspace access token. It is the
// supported way for another program (orqi, a script) to reuse `orq auth
// login`: where the session keeps its secrets is this CLI's business, and they
// can move out of the session file into the OS credential store.
func NewTokenCommand() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the access token of your browser login",
		Long: `Print the workspace access token of your browser login ('orq auth login'),
exchanging a new one first when it is expired or about to expire.

The token is scoped to the login's active workspace and project, or to the ones
--workspace and --project name. It lasts about an hour, so ask again rather
than storing it. With -o json the output also carries its expiry, workspace,
project and server; the token is only valid against that server.

--refresh exchanges a new token even when the cached one has not expired, for
a caller the API answered with authz_stale (its workspace access changed).

API keys are not printed: a profile or an exported ORQ_API_KEY is already a
credential you hold. While one is in force, --workspace and --project do not
apply and the active pair's token is printed.`,
		Example: `  orq auth token
  curl -H "Authorization: Bearer $(orq auth token)" "$(orq auth token -o json | jq -r .server)/v2/projects"
  orq auth token --refresh -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return auth.ErrNotLoggedIn
			}
			report, err := sessionTokenReport(cmd, session, refresh)
			if err != nil {
				return err
			}
			// Only a typed -o switches to JSON: a standing ORQ_OUTPUT_FORMAT or
			// config default must not turn $(orq auth token) into a document.
			if ownFormatRequested(cmd) {
				return emit(report)
			}
			_, err = fmt.Fprintln(bartolocli.Stdout, report.Token)
			return err
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Exchange a new token even when the cached one has not expired")
	return cmd
}

// sessionTokenReport returns the token for the slot the PreRun resolved, or for
// the active pair when the PreRun minted none (an API key is in force). An
// unexpired cached token needs no request; the PreRun has usually just cached
// one, so this costs nothing beyond what every command already paid.
func sessionTokenReport(cmd *cobra.Command, session *auth.Session, refresh bool) (*tokenReport, error) {
	workspace, project := sessionScope.workspace, sessionScope.project
	if workspace == "" {
		if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey == "" {
			return nil, fmt.Errorf("no active workspace selected. Run `orq workspace use <key>` first")
		}
		workspace, project = *session.ActiveWorkspaceKey, session.ActiveProjectID
	}
	server := sessionAPIBase(session)
	client := auth.NewClient(server).WithContext(cmd.Context()).WithProject(project)
	tok, err := slotToken(client, session, workspace, project, refresh)
	if err != nil {
		return nil, err
	}
	return &tokenReport{Token: tok.Token, ExpiresAt: tok.ExpiresAt, Workspace: workspace, ProjectID: project, Server: server}, nil
}

func slotToken(client *auth.Client, session *auth.Session, workspace, project string, refresh bool) (auth.StoredAccessToken, error) {
	if refresh {
		return client.RefreshWorkspaceToken(session, workspace)
	}
	if _, err := client.WorkspaceToken(session, workspace); err != nil {
		return auth.StoredAccessToken{}, err
	}
	// WorkspaceToken leaves the token it returned, cached or fresh, in the slot.
	return session.WorkspaceTokens[auth.TokenCacheKey(workspace, project)], nil
}
