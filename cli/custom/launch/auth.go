package launch

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	bartolocli "github.com/orq-ai/bartolo/cli"

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

// shadowsSession reports whether the env key provably belongs to a different
// workspace than the session. Coexistence alone is not a conflict: setup writes
// ~/.orq/env exporting the key it minted for the workspace resolved at login,
// so the ordinary machine has both, pointing at the same place.
func shadowsSession(key string, session *auth.Session) bool {
	if session == nil || session.ActiveWorkspaceKey == nil {
		return false
	}
	active := strings.TrimSpace(*session.ActiveWorkspaceKey)
	if active == "" {
		return false
	}
	if bartolocli.Creds == nil {
		return false // no credential store to compare against
	}
	profile := bartolocli.GetProfile()
	if strings.TrimSpace(profile["api_key"]) != key {
		return true // not the key we minted; its workspace is unknowable
	}
	saved := strings.TrimSpace(profile["workspace"])
	return saved != "" && saved != active
}

// ResolveCredentials resolves the orq API key and API base URL explicitly
// (not relying on the session PreRun env side effect): ORQ_API_KEY env wins
// (the session is not read at all), else the active workspace token from the
// login session. API base: ORQ_API_BASE_URL env → session APIBaseURL →
// default.
func ResolveCredentials(getenv func(string) string) (*Credentials, error) {
	apiBase := firstNonEmpty(getenv("ORQ_API_BASE_URL"), DefaultGatewayAPIBaseURL)

	if key := getenv("ORQ_API_KEY"); key != "" {
		session, _ := auth.ReadSession()
		return &Credentials{APIKey: key, APIBaseURL: apiBase, ShadowsSession: shadowsSession(key, session)}, nil
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

// LoginHook, when set, runs the interactive device login and persists the
// session. The commands package injects it at wiring time — launch cannot
// call the login flow directly without an import cycle.
var LoginHook func() error

// isTerminalFd is swappable in tests; prompting depends on a real TTY.
var isTerminalFd = func(fd int) bool { return term.IsTerminal(fd) }

// resolveCredentialsOrLogin is ResolveCredentials plus a recovery path: when
// the only problem is "not logged in" and a human is present, offer to run
// the device login right here instead of dead-ending on an error that tells
// them to run another command and come back.
func resolveCredentialsOrLogin(getenv func(string) string, allowPrompt bool) (*Credentials, error) {
	creds, err := ResolveCredentials(getenv)
	if err == nil || !errors.Is(err, errNotLoggedIn) || LoginHook == nil || !allowPrompt {
		return creds, err
	}
	// Same interactivity rules as the local-mode warning prompt.
	if getenv("ORQ_LAUNCH_NON_INTERACTIVE") == "1" ||
		!isTerminalFd(int(os.Stdin.Fd())) || !isTerminalFd(int(os.Stderr.Fd())) {
		return creds, err
	}

	fmt.Fprintln(os.Stderr, "Not logged in.")
	fmt.Fprint(os.Stderr, "Log in now via browser? [Y/n]: ")
	line, rerr := bufio.NewReader(os.Stdin).ReadString('\n')
	if rerr != nil {
		return nil, err // read failure falls back to the original error
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "":
	default:
		return nil, err
	}
	if lerr := LoginHook(); lerr != nil {
		return nil, lerr
	}
	return ResolveCredentials(getenv)
}

var errNotLoggedIn = notLoggedInError{}

type notLoggedInError struct{}

func (notLoggedInError) Error() string {
	return "not logged in. Run 'orq auth login' or export ORQ_API_KEY"
}
