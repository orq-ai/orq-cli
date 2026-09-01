package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

type Client struct {
	URLs       URLs
	HTTPClient *http.Client
	// ctx cancels in-flight requests. Set via WithContext(cmd.Context()) so a
	// Ctrl+C during a slow call (device-login start, profile fetch) returns
	// immediately instead of hanging until the 30s HTTP timeout.
	ctx context.Context
	// projectID narrows every access token this client mints to one project.
	// Empty means all projects the user can see.
	projectID string
}

func NewClient(apiBase string) *Client {
	return &Client{
		URLs:       ResolveURLs(apiBase),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// WithContext binds a cancellation context to every request this client makes.
// Returns the client for chaining.
func (c *Client) WithContext(ctx context.Context) *Client {
	c.ctx = ctx
	return c
}

// WithProject narrows the access tokens this client mints to one project, so
// the server scopes both reads and creates to it. Returns the client for
// chaining.
func (c *Client) WithProject(projectID string) *Client {
	c.projectID = strings.TrimSpace(projectID)
	return c
}

// TokenCacheKey is the WorkspaceTokens cache key for a workspace/project pair.
// An unscoped token keeps the bare workspace key, so sessions written before
// project scoping stay valid.
//
// Exported because three packages read this map and each one spelled the
// separator itself. When project scoping landed, the two that were not updated
// stopped finding the active token and resurrected a credential warning that
// had already been fixed once.
func TokenCacheKey(workspaceKey, projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return workspaceKey
	}
	return workspaceKey + "#" + strings.TrimSpace(projectID)
}

// TokenCacheKeyWorkspace returns the workspace a cache key belongs to,
// whichever project it is scoped to.
func TokenCacheKeyWorkspace(cacheKey string) string {
	key, _, _ := strings.Cut(cacheKey, "#")
	return key
}

func (c *Client) tokenKey(workspaceKey string) string {
	return TokenCacheKey(workspaceKey, c.projectID)
}

func (c *Client) reqContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// ============================================================================
// Device login
// ============================================================================

type DeviceLoginStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type ApprovedDeviceLogin struct {
	RefreshToken string
	AccessToken  string
	ExpiresIn    int
}

func (c *Client) StartDeviceLogin(clientName string) (*DeviceLoginStart, error) {
	if clientName == "" {
		clientName = "orq-cli"
	}
	var resp DeviceLoginStart
	err := c.jsonRequest(
		http.MethodPost,
		c.URLs.AuthBaseURL+"/cli/device/start",
		"",
		map[string]any{
			"client_name": clientName,
			// Request MCP scopes and audience up front so the session's access
			// tokens can authenticate against the orq MCP server; without them
			// every MCP call fails with insufficient_scope / invalid_token.
			// Platform and gateway auth ignore both claims.
			"scope":    []string{"mcp:tools", "mcp:resources"},
			"audience": strings.TrimRight(c.URLs.APIBaseURL, "/") + "/v2/mcp",
		},
		&resp,
	)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

type devicePollResult struct {
	Status   string // "approved", "pending", "slow_down"
	Approved *ApprovedDeviceLogin
	Interval int
}

func (c *Client) PollDeviceLogin(ctx context.Context, deviceCode string, interval int) (*devicePollResult, error) {
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URLs.AuthBaseURL+"/cli/device/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("invalid device token response: %w", err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && data.Error == "" {
		return &devicePollResult{
			Status: "approved",
			Approved: &ApprovedDeviceLogin{
				RefreshToken: data.RefreshToken,
				AccessToken:  data.AccessToken,
				ExpiresIn:    data.ExpiresIn,
			},
		}, nil
	}
	switch data.Error {
	case "authorization_pending":
		return &devicePollResult{Status: "pending", Interval: interval}, nil
	case "slow_down":
		return &devicePollResult{Status: "slow_down", Interval: interval + 5}, nil
	case "expired_token":
		return nil, errors.New("device login expired")
	case "":
		return nil, errors.New("device login returned an invalid response")
	default:
		return nil, errors.New("device login was denied")
	}
}

// AwaitDeviceApproval polls until the browser approves the device login. It
// honors ctx between polls and inside the HTTP request, so Ctrl+C interrupts
// the wait immediately instead of only once the device code expires.
func (c *Client) AwaitDeviceApproval(ctx context.Context, deviceCode string, expiresIn, initialInterval int) (*ApprovedDeviceLogin, error) {
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	interval := initialInterval
	for time.Now().Before(deadline) {
		result, err := c.PollDeviceLogin(ctx, deviceCode, interval)
		if err != nil {
			return nil, err
		}
		if result.Status == "approved" {
			return result.Approved, nil
		}
		interval = result.Interval
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
	return nil, errors.New("timed out waiting for browser approval")
}

// ============================================================================
// Profile
// ============================================================================

type Profile struct {
	ID          string           `json:"id"`
	Email       string           `json:"email"`
	DisplayName string           `json:"display_name"`
	Workspaces  []map[string]any `json:"workspaces"`
	Preferences struct {
		ActiveWorkspace string `json:"active_workspace"`
	} `json:"preferences"`
}

// FetchProfile calls the identity-api ProfileService.GetProfile Connect RPC.
// Connect unary calls are POSTs with a JSON request message, and the profile
// arrives wrapped in the GetProfileResponse envelope.
func (c *Client) FetchProfile(accessToken string) (*Profile, error) {
	var resp struct {
		Profile Profile `json:"profile"`
	}
	err := c.jsonRequest(http.MethodPost, c.URLs.ProfileBaseURL, accessToken, map[string]any{}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Profile.Workspaces == nil {
		return nil, fmt.Errorf("invalid profile response from %s", c.URLs.ProfileBaseURL)
	}
	return &resp.Profile, nil
}

// ============================================================================
// Token exchange
// ============================================================================

func (c *Client) ExchangeAccessToken(refreshToken, workspaceKey string) (StoredAccessToken, error) {
	body := map[string]string{"refresh_token": refreshToken}
	if workspaceKey != "" {
		body["workspace_key"] = workspaceKey
	}
	if c.projectID != "" {
		body["project_id"] = c.projectID
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.jsonRequest(http.MethodPost, c.URLs.AuthBaseURL+"/access-token", "", body, &resp); err != nil {
		return StoredAccessToken{}, err
	}
	exp, err := decodeJWTExpiry(resp.AccessToken)
	if err != nil {
		return StoredAccessToken{}, err
	}
	return StoredAccessToken{
		Token:     resp.AccessToken,
		ExpiresAt: formatISO(exp),
	}, nil
}

func (c *Client) Logout(refreshToken string) error {
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req, err := http.NewRequestWithContext(c.reqContext(), http.MethodDelete, c.URLs.AuthBaseURL+"/refresh-token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("logout failed with status %d", res.StatusCode)
	}
	return nil
}

// ============================================================================
// Session lifecycle
// ============================================================================

func resolveDisplayName(p *Profile) string {
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		return name
	}
	return p.Email
}

func resolveWorkspaceKey(p *Profile, preferred string) string {
	if preferred != "" {
		return preferred
	}
	if p.Preferences.ActiveWorkspace != "" {
		return p.Preferences.ActiveWorkspace
	}
	if len(p.Workspaces) > 0 {
		if k, ok := p.Workspaces[0]["key"].(string); ok {
			return k
		}
	}
	return ""
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func (c *Client) CreateSessionFromDeviceApproval(approved *ApprovedDeviceLogin, profile *Profile, workspaceKey string) (*Session, error) {
	if profile == nil {
		var err error
		profile, err = c.FetchProfile(approved.AccessToken)
		if err != nil {
			return nil, err
		}
	}
	resolvedKey := resolveWorkspaceKey(profile, workspaceKey)
	workspaceTokens := map[string]StoredAccessToken{}
	if resolvedKey != "" {
		tok, err := c.ExchangeAccessToken(approved.RefreshToken, resolvedKey)
		if err != nil {
			return nil, err
		}
		workspaceTokens[resolvedKey] = tok
	}
	bootstrapExp, err := decodeJWTExpiry(approved.AccessToken)
	if err != nil {
		return nil, err
	}
	session := &Session{
		Version:        1,
		APIBaseURL:     c.URLs.APIBaseURL,
		V1BaseURL:      c.URLs.V1BaseURL,
		AuthBaseURL:    c.URLs.AuthBaseURL,
		ProfileBaseURL: c.URLs.ProfileBaseURL,
		User: &SessionUser{
			ID:          profile.ID,
			Email:       profile.Email,
			DisplayName: resolveDisplayName(profile),
		},
		Workspaces:         profile.Workspaces,
		ActiveWorkspaceKey: stringPtr(resolvedKey),
		RefreshToken:       approved.RefreshToken,
		BootstrapToken: StoredAccessToken{
			Token:     approved.AccessToken,
			ExpiresAt: formatISO(bootstrapExp),
		},
		WorkspaceTokens: workspaceTokens,
	}
	if err := SaveSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *Client) EnsureBootstrapToken(session *Session) (*Session, error) {
	if !isExpired(session.BootstrapToken.ExpiresAt, 60) {
		return session, nil
	}
	tok, err := c.ExchangeAccessToken(session.RefreshToken, "")
	if err != nil {
		return nil, err
	}
	session.BootstrapToken = tok
	if err := SaveSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *Client) RefreshProfile(session *Session) (*Session, error) {
	session, err := c.EnsureBootstrapToken(session)
	if err != nil {
		return nil, err
	}
	profile, err := c.FetchProfile(session.BootstrapToken.Token)
	if err != nil {
		return nil, err
	}
	var activeKey *string
	if session.ActiveWorkspaceKey != nil {
		for _, w := range profile.Workspaces {
			if k, ok := w["key"].(string); ok && k == *session.ActiveWorkspaceKey {
				activeKey = session.ActiveWorkspaceKey
				break
			}
		}
	}
	if activeKey == nil {
		activeKey = stringPtr(resolveWorkspaceKey(profile, ""))
	}
	session.User = &SessionUser{
		ID:          profile.ID,
		Email:       profile.Email,
		DisplayName: resolveDisplayName(profile),
	}
	session.Workspaces = profile.Workspaces
	session.ActiveWorkspaceKey = activeKey
	if err := SaveSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *Client) EnsureWorkspaceToken(session *Session, workspaceKey string) (*Session, error) {
	cacheKey := c.tokenKey(workspaceKey)
	current, cached := session.WorkspaceTokens[cacheKey]
	if !cached || isExpired(current.ExpiresAt, 60) {
		tok, err := c.ExchangeAccessToken(session.RefreshToken, workspaceKey)
		if err != nil {
			return nil, err
		}
		if session.WorkspaceTokens == nil {
			session.WorkspaceTokens = map[string]StoredAccessToken{}
		}
		session.WorkspaceTokens[cacheKey] = tok
	}
	session.ActiveWorkspaceKey = stringPtr(workspaceKey)
	if err := SaveSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

// WorkspaceToken returns an access token for the given workspace WITHOUT
// changing the stored session's active workspace, so a per-invocation
// `--workspace`/`ORQ_WORKSPACE` override cannot flip another invocation's
// active-workspace state. The fetched token is cached by merging ONLY the new
// WorkspaceTokens entry onto the current on-disk session (re-read immediately
// before the write), never by saving the caller's start-of-process snapshot —
// so a concurrent `workspace use` that changed the active workspace between the
// caller's read and this cache write is not silently reverted. The write itself
// is atomic (temp file + rename), so no reader ever sees a torn file. Two
// concurrent token exchanges for different workspaces both survive; two for the
// SAME workspace last-writer-wins on that one entry, costing at most one extra
// exchange.
func (c *Client) WorkspaceToken(session *Session, workspaceKey string) (string, error) {
	cacheKey := c.tokenKey(workspaceKey)
	if tok, ok := session.WorkspaceTokens[cacheKey]; ok && !isExpired(tok.ExpiresAt, 60) {
		return tok.Token, nil
	}
	tok, err := c.ExchangeAccessToken(session.RefreshToken, workspaceKey)
	if err != nil {
		return "", err
	}
	// Keep the in-memory session usable for this invocation regardless of the
	// cache outcome.
	if session.WorkspaceTokens == nil {
		session.WorkspaceTokens = map[string]StoredAccessToken{}
	}
	session.WorkspaceTokens[cacheKey] = tok
	if err := mergeWorkspaceToken(cacheKey, tok); err != nil {
		// Not fatal - the token works for this invocation - but a silent drop
		// (e.g. a read-only session dir) would re-exchange on every single call
		// with no explanation. Route through bartolo's writer so --no-color and
		// the ANSI-stripping swap still apply.
		fmt.Fprintf(bartolocli.Stderr, "warning: could not cache the workspace token (%v); it will be re-exchanged next invocation\n", err)
	}
	return tok.Token, nil
}

// mergeWorkspaceToken persists a single workspace token by re-reading the
// on-disk session and writing back only the added entry, so a concurrent writer
// that changed the active workspace (or another token) is not clobbered. A
// session that vanished between read and now (e.g. a concurrent logout) is left
// alone rather than recreated.
func mergeWorkspaceToken(workspaceKey string, tok StoredAccessToken) error {
	current, err := ReadSession()
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if current.WorkspaceTokens == nil {
		current.WorkspaceTokens = map[string]StoredAccessToken{}
	}
	current.WorkspaceTokens[workspaceKey] = tok
	return SaveSession(current)
}

func (c *Client) UseWorkspace(workspaceKey string) (*Session, error) {
	session, err := ReadSession()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("you are not logged in")
	}
	session, err = c.RefreshProfile(session)
	if err != nil {
		return nil, err
	}
	found := false
	for _, w := range session.Workspaces {
		if k, ok := w["key"].(string); ok && k == workspaceKey {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("workspace %q is not available to this user", workspaceKey)
	}
	return c.EnsureWorkspaceToken(session, workspaceKey)
}

func (c *Client) WhoAmI() (*Session, error) {
	session, err := ReadSession()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("you are not logged in")
	}
	return c.RefreshProfile(session)
}

type ActiveAccessToken struct {
	AccessToken  string
	Session      *Session
	WorkspaceKey string
}

func (c *Client) GetActiveWorkspaceAccessToken() (*ActiveAccessToken, error) {
	session, err := ReadSession()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("you are not logged in")
	}
	session, err = c.RefreshProfile(session)
	if err != nil {
		return nil, err
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey == "" {
		return nil, errors.New("no active workspace selected. Run `orq workspace use <key>` first")
	}
	key := *session.ActiveWorkspaceKey
	session, err = c.EnsureWorkspaceToken(session, key)
	if err != nil {
		return nil, err
	}
	tok := session.WorkspaceTokens[c.tokenKey(key)]
	return &ActiveAccessToken{
		AccessToken:  tok.Token,
		Session:      session,
		WorkspaceKey: key,
	}, nil
}

func (c *Client) ClearLocalSession() error {
	return ClearSession()
}

func (c *Client) Login(ctx context.Context, workspaceKey, clientName string) (*Session, error) {
	start, err := c.StartDeviceLogin(clientName)
	if err != nil {
		return nil, err
	}
	approved, err := c.AwaitDeviceApproval(ctx, start.DeviceCode, start.ExpiresIn, start.Interval)
	if err != nil {
		return nil, err
	}
	return c.CreateSessionFromDeviceApproval(approved, nil, workspaceKey)
}

// ============================================================================
// Helpers
// ============================================================================

// APIError is a response the API rejected, carrying the status so callers can
// tell "your credential is refused" from "the host did not answer" — the two
// read identically once the body is flattened into a string.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return e.Msg }

// Unauthorized reports whether err is the API refusing the credential itself.
//
// 401 only, and deliberately: measured against the live API, a revoked gateway
// key gets 401 from /v2/workspace-settings while a live one belonging to a
// different workspace gets 200 naming that workspace. So a 401 here means the
// key is gone, never that it belongs elsewhere — despite the message the server
// sends with it ("API key is not valid for this workspace").
//
// 403 is a different answer, see Forbidden.
func Unauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

// Forbidden reports whether err is the API accepting the credential but
// refusing it for this route — "This API key type cannot access this endpoint",
// which is what a gateway key gets from /v2/api-keys/{id}. Folding it into
// Unauthorized would read a route the key may not use as a dead key, and every
// run would mint a replacement for a credential that works.
func Forbidden(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
}

// NotFound reports whether err is the API saying the resource is not in this workspace.
func NotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func (c *Client) jsonRequest(method, url, bearer string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(c.reqContext(), method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return &APIError{Status: res.StatusCode, Msg: describeAPIError(res.StatusCode, raw)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

// describeAPIError turns an error response into something actionable. A bare
// "Request body failed validation." tells the user nothing, so any per-field
// detail the API returned is appended.
func describeAPIError(status int, raw []byte) string {
	// The API returns per-field problems under details.issues; older/other
	// endpoints use a flat issues/errors array. Handle all three.
	type issue struct {
		Path    []any    `json:"path"`
		Message string   `json:"message"`
		Values  []string `json:"values"`
	}
	var body struct {
		Message   string  `json:"message"`
		Error     string  `json:"error"`
		Detail    string  `json:"detail"`
		RequestID string  `json:"request_id"`
		Issues    []issue `json:"issues"`
		Errors    []issue `json:"errors"`
		Details   struct {
			Issues []issue `json:"issues"`
		} `json:"details"`
	}
	_ = json.Unmarshal(raw, &body)

	summary := firstNonEmpty(body.Message, body.Error, body.Detail)
	if summary == "" {
		summary = fmt.Sprintf("request failed with status %d", status)
	}

	details := []string{}
	all := append(append(append([]issue{}, body.Details.Issues...), body.Issues...), body.Errors...)
	for _, i := range all {
		line := joinPathMessage(i.Path, i.Message)
		if len(i.Values) > 0 {
			line += " (expected one of: " + strings.Join(i.Values, ", ") + ")"
		}
		details = append(details, line)
	}
	if len(details) > 0 && body.RequestID != "" {
		details = append(details, "request_id: "+body.RequestID)
	}
	if len(details) > 0 {
		return summary + "\n  " + strings.Join(details, "\n  ")
	}

	// Nothing structured to show: fall back to the raw payload, which at least
	// lets the user report what happened.
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "{}" && summary == fmt.Sprintf("request failed with status %d", status) {
		if len(trimmed) > 400 {
			trimmed = trimmed[:400] + "…"
		}
		return summary + "\n  " + trimmed
	}
	return summary
}

func joinPathMessage(path []any, message string) string {
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, fmt.Sprintf("%v", p))
	}
	rendered := strings.Join(parts, ".")
	if rendered == "" {
		return message
	}
	return rendered + ": " + message
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// OpenBrowser tries to open the given URL in the default browser. Returns true
// if the launcher was started successfully.
func OpenBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	_ = cmd.Process.Release()
	return true
}

// KeyWorkspace returns the workspace slug the bearer belongs to. The mismatch
// guard in setup can only compare a workspace it recorded locally, which is
// blank for keys minted before that field existed and for --api-key runs; this
// asks the API instead.
//
// The route resolves the workspace from the credential alone — there is no
// session context in the request — so it answers for a session token and for a
// gateway key alike, and a key minted in another workspace gets a 200 naming
// that other workspace rather than a refusal. Verified against api.orq.ai with
// a freshly minted restricted key of source "router": 200 here, 200 on
// /v2/projects, 403 on /v2/api-keys/{id}.
func (c *Client) KeyWorkspace(bearer string) (string, error) {
	var resp struct {
		Settings struct {
			Key string `json:"key"`
		} `json:"settings"`
	}
	url := c.URLs.APIBaseURL + "/v2/workspace-settings"
	if err := c.jsonRequest(http.MethodGet, url, bearer, nil, &resp); err != nil {
		return "", err
	}
	// A 200 with no workspace key is a broken response, not an answer. Returning
	// "" instead would read as "no mismatch" to every caller, so a renamed field
	// would silently turn both the reuse guard and verification into no-ops that
	// still report success.
	ws := strings.TrimSpace(resp.Settings.Key)
	if ws == "" {
		return "", errors.New("workspace-settings returned no workspace key")
	}
	return ws, nil
}
