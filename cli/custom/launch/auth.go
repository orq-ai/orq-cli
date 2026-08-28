package launch

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"orq/cli/custom/auth"
)

// CredentialKind is what ResolveCredentials decided the key is. The zero value
// is unknown on purpose: a credential nobody resolved must not claim a
// capability. This was two bools, where "real API key" was the zero value of
// FromSession, so a Credentials nobody filled in reported that it could reach
// the MCP server — which is how the pre-MCP-scopes warning stopped firing once
// the CLI began injecting its own session token into ORQ_API_KEY.
type CredentialKind int

const (
	CredentialUnknown CredentialKind = iota
	// CredentialAPIKey is a key the user exported or brought.
	CredentialAPIKey
	// CredentialSessionToken is a workspace token from the login session.
	CredentialSessionToken
)

// Credentials is the resolved auth pair every launch needs.
type Credentials struct {
	APIKey     string
	APIBaseURL string
	Kind       CredentialKind
	// ShadowsSession is set when ORQ_API_KEY won over an existing login
	// session. The workspace the key belongs to is then in force instead of
	// the one picked at login — invisible unless we say so.
	ShadowsSession bool
	// Workspace is the workspace this credential belongs to, when known.
	Workspace string
	// SupersededWorkspace names the workspace an exported key was minted for, when the session's
	// workspace was used instead.
	SupersededWorkspace string
}

// isSessionWorkspaceToken reports whether the value in ORQ_API_KEY is one of the
// session's own cached workspace tokens rather than a key the user exported.
func isSessionWorkspaceToken(key string, session *auth.Session) bool {
	for _, tok := range session.WorkspaceTokens {
		if strings.TrimSpace(tok.Token) == key {
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
	// installSessionPreRun injects the session's own workspace token into
	// ORQ_API_KEY whenever no api_key is configured, which is now the ordinary
	// state. That token is ours, not a key the user exported, so comparing it to
	// the saved key reported a mismatch on every single launch.
	if tok, ok := session.WorkspaceTokens[active]; ok && strings.TrimSpace(tok.Token) == key {
		return false
	}
	// auth.SavedAgentKey, not a local profile read: the minted key moved to
	// gateway_key and this copy kept reading api_key, so an empty savedKey made
	// every launch report a mismatch that was not there either.
	savedKey, savedWS := auth.SavedAgentKey()
	return auth.EnvKeyShadowsWorkspace(key, savedKey, savedWS, active)
}

// supersededBySession reports the workspace an exported ORQ_API_KEY was minted for, when the
// session has since moved elsewhere. Launch configures one throwaway process, so the workspace
// the user is in now wins; the agent's own config on disk is untouched, and `orq connect` stays
// the only thing that repoints it.
//
// Narrower than shadowsSession on purpose: a key we did not mint has an unknowable workspace,
// so it keeps winning.
func supersededBySession(key string, session *auth.Session, savedKey, savedWS string) (mintedFor string, superseded bool) {
	if session == nil || session.ActiveWorkspaceKey == nil {
		return "", false
	}
	active := strings.TrimSpace(*session.ActiveWorkspaceKey)
	if active == "" {
		return "", false
	}
	if isSessionWorkspaceToken(key, session) {
		return "", false
	}
	if savedWS == "" || savedKey != key {
		return "", false
	}
	if savedWS == active {
		return "", false
	}
	return savedWS, true
}

// APIBaseFor is the API base a persistent artifact should be written against:
// the env override, then the saved login session's own base, then the hosted
// default. It is ResolveCredentials' order for a caller that has no credential
// to resolve — `orq connect mcp` writes a URL and never authenticates — so a
// self-hosted user's persisted entry and the session `orq launch` starts cannot
// point at two different servers.
func APIBaseFor(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("ORQ_API_BASE_URL")); v != "" {
		return v
	}
	if session, err := auth.ReadSession(); err == nil && session != nil {
		if base := strings.TrimSpace(session.APIBaseURL); base != "" {
			return base
		}
	}
	return DefaultGatewayAPIBaseURL
}

// ResolveCredentials resolves the orq API key and API base URL explicitly
// (not relying on the session PreRun env side effect): ORQ_API_KEY env wins
// (the session is not read at all), else the active workspace token from the
// login session. API base: the server the root PreRun resolved (--server,
// ORQ_SERVER, the deprecated ORQ_API_BASE_URL) → session APIBaseURL → default.
func ResolveCredentials(getenv func(string) string) (*Credentials, error) {
	// auth.Server() is empty when the PreRun did not run — the launch tests,
	// which drive this with an injected getenv. The env fallback goes through
	// the same ladder the PreRun uses, so the two cannot disagree.
	envServer, _ := auth.ServerFromEnv(getenv)
	resolved := firstNonEmpty(auth.Server(), envServer)
	apiBase := firstNonEmpty(resolved, DefaultGatewayAPIBaseURL)

	var supersededWorkspace string
	var session *auth.Session
	if key := getenv("ORQ_API_KEY"); key != "" {
		// Error ignored here on purpose: an unreadable session must not block a
		// valid exported key from winning below.
		session, _ = auth.ReadSession()
		savedKey, savedWS := auth.SavedAgentKey()
		mintedFor, superseded := supersededBySession(key, session, savedKey, savedWS)
		if !superseded {
			creds := &Credentials{
				APIKey:         key,
				APIBaseURL:     apiBase,
				Kind:           CredentialAPIKey,
				ShadowsSession: shadowsSession(key, session),
			}
			if savedWS != "" && savedKey == key {
				creds.Workspace = savedWS
			}
			// installSessionPreRun injects the session's own workspace token into
			// ORQ_API_KEY whenever no api_key is configured, which the gateway_key
			// split made the ordinary state. Reading it as an exported key rather
			// than as the session's own is what made ShadowsSession fire wrongly.
			if session != nil && isSessionWorkspaceToken(key, session) {
				creds.Kind = CredentialSessionToken
			}
			return creds, nil
		}
		// superseded implies session != nil (supersededBySession requires it), so
		// the read below is skipped and ResolveCredentials itself makes exactly one
		// ReadSession call (the superseded path below still reads the session again,
		// via GetActiveWorkspaceAccessToken).
		supersededWorkspace = mintedFor
	}

	if session == nil {
		var err error
		session, err = auth.ReadSession()
		if err != nil {
			// Unreadable or corrupt session ≠ not logged in — telling the user to
			// log in again would loop without surfacing the real cause.
			return nil, fmt.Errorf("cannot read login session: %w (fix the session file or export ORQ_API_KEY)", err)
		}
	}
	if session == nil {
		return nil, errNotLoggedIn
	}
	if session.APIBaseURL != "" && resolved == "" {
		apiBase = session.APIBaseURL
	}
	// apiBase, not session.APIBaseURL: an explicit --server diverts the token
	// fetch too, so one run cannot straddle two hosts.
	client := auth.NewClient(apiBase)
	active, err := client.GetActiveWorkspaceAccessToken()
	if err != nil {
		return nil, err
	}
	return &Credentials{
		APIKey:              active.AccessToken,
		APIBaseURL:          apiBase,
		Kind:                CredentialSessionToken,
		Workspace:           active.WorkspaceKey,
		SupersededWorkspace: supersededWorkspace,
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
