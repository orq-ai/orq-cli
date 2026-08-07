package launch

import (
	"fmt"

	"orq/cli/custom/auth"
)

// Credentials is the resolved auth pair every launch needs.
type Credentials struct {
	APIKey     string
	APIBaseURL string
	// FromSession is set when APIKey is a login-session workspace token
	// rather than a real API key. Session tokens work against the gateway
	// and platform API but are minted without the MCP scope.
	FromSession bool
}

// ResolveCredentials resolves the orq API key and API base URL explicitly
// (not relying on the session PreRun env side effect): ORQ_API_KEY env wins
// (the session is not read at all), else the active workspace token from the
// login session. API base: ORQ_API_BASE_URL env → session APIBaseURL →
// default.
func ResolveCredentials(getenv func(string) string) (*Credentials, error) {
	apiBase := firstNonEmpty(getenv("ORQ_API_BASE_URL"), DefaultGatewayAPIBaseURL)

	if key := getenv("ORQ_API_KEY"); key != "" {
		return &Credentials{APIKey: key, APIBaseURL: apiBase}, nil
	}

	session, err := auth.ReadSession()
	if err != nil {
		// Unreadable or corrupt session ≠ not logged in — telling the user to
		// log in again would loop without surfacing the real cause.
		return nil, fmt.Errorf("cannot read login session: %w (fix the session file or export ORQ_API_KEY)", err)
	}
	if session == nil {
		return nil, errNotLoggedIn
	}
	if session.APIBaseURL != "" && getenv("ORQ_API_BASE_URL") == "" {
		apiBase = session.APIBaseURL
	}
	client := auth.NewClient(session.APIBaseURL)
	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil {
		return nil, err
	}
	return &Credentials{APIKey: active.AccessToken, APIBaseURL: apiBase, FromSession: true}, nil
}

var errNotLoggedIn = notLoggedInError{}

type notLoggedInError struct{}

func (notLoggedInError) Error() string {
	return "not logged in. Run 'orq auth login' or export ORQ_API_KEY"
}
