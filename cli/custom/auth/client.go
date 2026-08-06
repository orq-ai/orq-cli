package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Client struct {
	URLs       URLs
	HTTPClient *http.Client
}

func NewClient(apiBase string) *Client {
	return &Client{
		URLs:       ResolveURLs(apiBase),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
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
		map[string]string{"client_name": clientName},
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

func (c *Client) PollDeviceLogin(deviceCode string, interval int) (*devicePollResult, error) {
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	req, err := http.NewRequest(http.MethodPost, c.URLs.AuthBaseURL+"/cli/device/token", bytes.NewReader(body))
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

func (c *Client) AwaitDeviceApproval(deviceCode string, expiresIn, initialInterval int) (*ApprovedDeviceLogin, error) {
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	interval := initialInterval
	for time.Now().Before(deadline) {
		result, err := c.PollDeviceLogin(deviceCode, interval)
		if err != nil {
			return nil, err
		}
		if result.Status == "approved" {
			return result.Approved, nil
		}
		interval = result.Interval
		time.Sleep(time.Duration(interval) * time.Second)
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

func (c *Client) FetchProfile(accessToken string) (*Profile, error) {
	var profile Profile
	err := c.jsonRequest(http.MethodGet, c.URLs.ProfileBaseURL, accessToken, nil, &profile)
	if err != nil {
		return nil, err
	}
	if profile.Workspaces == nil {
		return nil, fmt.Errorf("invalid profile response from %s", c.URLs.ProfileBaseURL)
	}
	return &profile, nil
}

// ============================================================================
// Token exchange
// ============================================================================

func (c *Client) ExchangeAccessToken(refreshToken, workspaceKey string) (StoredAccessToken, error) {
	body := map[string]string{"refresh_token": refreshToken}
	if workspaceKey != "" {
		body["workspace_key"] = workspaceKey
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
	req, err := http.NewRequest(http.MethodDelete, c.URLs.AuthBaseURL+"/refresh-token", bytes.NewReader(body))
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
	current, cached := session.WorkspaceTokens[workspaceKey]
	if !cached || isExpired(current.ExpiresAt, 60) {
		tok, err := c.ExchangeAccessToken(session.RefreshToken, workspaceKey)
		if err != nil {
			return nil, err
		}
		if session.WorkspaceTokens == nil {
			session.WorkspaceTokens = map[string]StoredAccessToken{}
		}
		session.WorkspaceTokens[workspaceKey] = tok
	}
	session.ActiveWorkspaceKey = stringPtr(workspaceKey)
	if err := SaveSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

// WorkspaceToken returns an access token for the given workspace WITHOUT
// changing the stored session's active workspace. Backs the per-invocation
// `--workspace`/`ORQ_WORKSPACE` override: two concurrent invocations with
// different overrides must not race each other's active-workspace state.
// The fetched token is still cached back into the session file.
func (c *Client) WorkspaceToken(session *Session, workspaceKey string) (string, error) {
	if tok, ok := session.WorkspaceTokens[workspaceKey]; ok && !isExpired(tok.ExpiresAt, 60) {
		return tok.Token, nil
	}
	tok, err := c.ExchangeAccessToken(session.RefreshToken, workspaceKey)
	if err != nil {
		return "", err
	}
	if session.WorkspaceTokens == nil {
		session.WorkspaceTokens = map[string]StoredAccessToken{}
	}
	session.WorkspaceTokens[workspaceKey] = tok
	_ = SaveSession(session)
	return tok.Token, nil
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
	tok := session.WorkspaceTokens[key]
	return &ActiveAccessToken{
		AccessToken:  tok.Token,
		Session:      session,
		WorkspaceKey: key,
	}, nil
}

func (c *Client) ClearLocalSession() error {
	return ClearSession()
}

func (c *Client) Login(workspaceKey, clientName string) (*Session, error) {
	start, err := c.StartDeviceLogin(clientName)
	if err != nil {
		return nil, err
	}
	approved, err := c.AwaitDeviceApproval(start.DeviceCode, start.ExpiresIn, start.Interval)
	if err != nil {
		return nil, err
	}
	return c.CreateSessionFromDeviceApproval(approved, nil, workspaceKey)
}

// ============================================================================
// Helpers
// ============================================================================

func (c *Client) jsonRequest(method, url, bearer string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reqBody)
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
		var errResp struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &errResp) == nil && errResp.Message != "" {
			return errors.New(errResp.Message)
		}
		return fmt.Errorf("request failed with status %d", res.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
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
