package launch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"orq/cli/custom/auth"
)

// Credentials is the resolved auth pair every launch needs.
type Credentials struct {
	APIKey     string
	APIBaseURL string
	// FromSession is set when APIKey is a login-session workspace token
	// rather than a real API key.
	FromSession bool
	// MCPScoped reports whether a session token carries the mcp:* scopes.
	// Logins made before the CLI requested them produce tokens without, and
	// the MCP server rejects those with insufficient_scope. Real API keys
	// (FromSession false) always pass MCP auth and skip this check.
	MCPScoped bool
	// ShadowsSession is set when ORQ_API_KEY won over an existing login
	// session. The workspace the key belongs to is then in force instead of
	// the one picked at login — invisible unless we say so.
	ShadowsSession bool
}

// SupportsMCP reports whether the credential can authenticate against the
// orq MCP server.
func (c *Credentials) SupportsMCP() bool {
	return !c.FromSession || c.MCPScoped
}

// tokenHasMCPScope decodes the JWT payload (unverified — this is a local
// capability hint, not authentication) and looks for an mcp:* scope entry.
func tokenHasMCPScope(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Scope []string `json:"scope"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	for _, s := range claims.Scope {
		if strings.HasPrefix(s, "mcp:") {
			return true
		}
	}
	return false
}

// ResolveCredentials resolves the orq API key and API base URL explicitly
// (not relying on the session PreRun env side effect): ORQ_API_KEY env wins
// (the session is not read at all), else the active workspace token from the
// login session. API base: ORQ_API_BASE_URL env → session APIBaseURL →
// default.
func ResolveCredentials(getenv func(string) string) (*Credentials, error) {
	apiBase := firstNonEmpty(getenv("ORQ_API_BASE_URL"), DefaultGatewayAPIBaseURL)

	if key := getenv("ORQ_API_KEY"); key != "" {
		// The session is not used, but knowing one exists lets the caller warn
		// that the env key silently outranks the workspace picked at login.
		session, _ := auth.ReadSession()
		return &Credentials{APIKey: key, APIBaseURL: apiBase, ShadowsSession: session != nil}, nil
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
	return &Credentials{
		APIKey:      active.AccessToken,
		APIBaseURL:  apiBase,
		FromSession: true,
		MCPScoped:   tokenHasMCPScope(active.AccessToken),
	}, nil
}

var errNotLoggedIn = notLoggedInError{}

type notLoggedInError struct{}

func (notLoggedInError) Error() string {
	return "not logged in. Run 'orq auth login' or export ORQ_API_KEY"
}
