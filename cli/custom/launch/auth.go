package launch

import (
	"orq/cli/custom/auth"
)

// Credentials is the resolved auth pair every launch needs.
type Credentials struct {
	APIKey     string
	APIBaseURL string
}

// ResolveCredentials resolves the orq API key and API base URL explicitly
// (not relying on the session PreRun env side effect): ORQ_API_KEY env wins,
// else the active workspace token from the login session. API base: session
// APIBaseURL → ORQ_API_BASE_URL → default.
func ResolveCredentials(getenv func(string) string) (*Credentials, error) {
	apiBase := firstNonEmpty(getenv("ORQ_API_BASE_URL"), DefaultGatewayAPIBaseURL)

	if key := getenv("ORQ_API_KEY"); key != "" {
		return &Credentials{APIKey: key, APIBaseURL: apiBase}, nil
	}

	session, err := auth.ReadSession()
	if err == nil && session != nil {
		if session.APIBaseURL != "" && getenv("ORQ_API_BASE_URL") == "" {
			apiBase = session.APIBaseURL
		}
		client := auth.NewClient(session.APIBaseURL)
		active, err := client.GetActiveWorkspaceAccessToken()
		if err != nil {
			return nil, err
		}
		return &Credentials{APIKey: active.AccessToken, APIBaseURL: apiBase}, nil
	}

	return nil, errNotLoggedIn
}

var errNotLoggedIn = notLoggedInError{}

type notLoggedInError struct{}

func (notLoggedInError) Error() string {
	return "not logged in. Run 'orq auth login' or export ORQ_API_KEY"
}
