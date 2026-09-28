package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// authzStaleCode is the 401 `code` the platform returns when the member
// snapshot a token was minted against is out of date or gone. identity-api
// stamps that snapshot with the workspace's authz epoch at exchange time, and
// changes to the workspace's projects, teams or membership bump the epoch
// (orquesta-web libs/go/authzsnapshot). The token is still unexpired, so the
// expiry-driven refresh never replaces it; only a fresh exchange does.
const authzStaleCode = "authz_stale"

// maxReplayBody is the largest body buffered when a request has no GetBody.
// Larger one-shot uploads stream through without a retry.
const maxReplayBody = 1 << 20

// errNotSessionToken marks a bearer this CLI did not mint, so its 401 is the
// user's to see.
var errNotSessionToken = errors.New("not a session token")

// StaleRetryTransport replays a request once with a freshly exchanged token
// when the server answers 401 authz_stale, the refresh-and-retry-once the web
// app's token interceptor does. Only tokens this CLI minted into the session
// are refreshed; an explicit API key never is.
type StaleRetryTransport struct {
	Base http.RoundTripper
}

// NewHTTPClient is the one way to build an HTTP client that sends an orq
// credential, so no authenticated path can miss the stale-token retry.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &StaleRetryTransport{Base: http.DefaultTransport}}
}

func (t *StaleRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	bearer, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		return t.Base.RoundTrip(req)
	}
	first, replay, err := replayable(req)
	if err != nil {
		return nil, err
	}
	res, err := t.Base.RoundTrip(first)
	if err != nil || res.StatusCode != http.StatusUnauthorized || replay == nil {
		return res, err
	}
	stale, err := isStaleResponse(res)
	if err != nil {
		return nil, err
	}
	if !stale {
		return res, nil
	}
	fresh, err := refreshStaleToken(req, bearer)
	if errors.Is(err, errNotSessionToken) {
		return res, nil
	}
	if err != nil {
		res.Body.Close()
		return nil, fmt.Errorf("the workspace token went stale and refreshing it failed: %w", err)
	}
	res.Body.Close()
	retry, err := replay()
	if err != nil {
		return nil, err
	}
	retry.Header.Set("Authorization", "Bearer "+fresh)
	// One retry, straight to Base, so it cannot loop.
	res, err = t.Base.RoundTrip(retry)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		stale, readErr := isStaleResponse(res)
		if readErr != nil {
			return nil, readErr
		}
		if stale {
			fmt.Fprintln(bartolocli.Stderr, "warning: a freshly exchanged workspace token was also rejected as authz_stale; the problem is on the server, not your login")
		}
	}
	return res, err
}

// replayable returns the request to send first and a way to build the retry.
// replay is nil when the body cannot be sent twice.
func replayable(req *http.Request) (*http.Request, func() (*http.Request, error), error) {
	clone := func(body io.ReadCloser) *http.Request {
		r := req.Clone(req.Context())
		r.Body = body
		return r
	}
	if req.Body == nil || req.Body == http.NoBody {
		return req, func() (*http.Request, error) { return clone(req.Body), nil }, nil
	}
	if req.GetBody != nil {
		return req, func() (*http.Request, error) {
			body, err := req.GetBody()
			return clone(body), err
		}, nil
	}
	head, err := io.ReadAll(io.LimitReader(req.Body, maxReplayBody+1))
	if err != nil {
		req.Body.Close()
		return nil, nil, err
	}
	if len(head) > maxReplayBody {
		rest := struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), req.Body), req.Body}
		return clone(rest), nil, nil
	}
	req.Body.Close()
	buffered := func() (*http.Request, error) { return clone(io.NopCloser(bytes.NewReader(head))), nil }
	first, _ := buffered()
	return first, buffered, nil
}

// isStaleResponse reads a 401 body to check its code, then puts the bytes
// back so the caller still sees the full response.
func isStaleResponse(res *http.Response) (bool, error) {
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return false, fmt.Errorf("reading the 401 response: %w", err)
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Code == authzStaleCode, nil
}

// StaleToken records which cache slot a replaced token belonged to, so a
// holder of the old token (another process, or an agent `orq launch` started
// with it) can find its replacement instead of a dead end.
type StaleToken struct {
	Key       string `json:"key"`
	ExpiresAt string `json:"expiresAt"`
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// slotFor finds the cache slot bearer was minted for: a live entry holding
// it, or the record left when it was replaced.
func slotFor(session *Session, bearer string) (string, bool) {
	for key, tok := range session.WorkspaceTokens {
		if tok.Token == bearer {
			return key, true
		}
	}
	stale, ok := session.StaleTokens[tokenHash(bearer)]
	return stale.Key, ok
}

// IsSessionMinted reports whether bearer is a workspace token this login
// issued, including one another process has already replaced after a 401.
func IsSessionMinted(bearer string) bool {
	if bearer == "" {
		return false
	}
	session, err := ReadSession()
	if err != nil || session == nil {
		return false
	}
	_, ok := slotFor(session, bearer)
	return ok
}

// CurrentToken returns the token that replaced bearer in the session cache,
// or bearer itself when it was not replaced or the replacement has expired.
func CurrentToken(bearer string) string {
	session, err := ReadSession()
	if err != nil || session == nil {
		return bearer
	}
	key, ok := slotFor(session, bearer)
	if !ok {
		return bearer
	}
	if cur, ok := session.WorkspaceTokens[key]; ok && cur.Token != "" && !isExpired(cur.ExpiresAt, 60) {
		return cur.Token
	}
	return bearer
}

// refreshStaleToken swaps a stale session token for a working one. When
// another process already refreshed the slot, its token is reused; otherwise
// the slot's workspace/project pair is exchanged again. ORQ_API_KEY is
// updated when it carried the old token (the root pre-run puts it there), so
// later requests in this process send the new one.
func refreshStaleToken(req *http.Request, bearer string) (string, error) {
	session, err := ReadSession()
	if err != nil {
		return "", err
	}
	if session == nil || !sessionServes(session, req.URL) {
		return "", errNotSessionToken
	}
	key, ok := slotFor(session, bearer)
	if !ok {
		return "", errNotSessionToken
	}
	fresh := ""
	if cur, ok := session.WorkspaceTokens[key]; ok && cur.Token != bearer && !isExpired(cur.ExpiresAt, 60) {
		fresh = cur.Token
	} else {
		workspaceKey, projectID := ParseTokenCacheKey(key)
		tok, err := NewClient(session.APIBaseURL).WithContext(req.Context()).WithProject(projectID).ExchangeAccessToken(session.RefreshToken, workspaceKey)
		if err != nil {
			return "", err
		}
		fresh = tok.Token
		if err := recordRefresh(key, bearer, tok); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "warning: could not cache the refreshed workspace token (%v); it will be re-exchanged next invocation\n", err)
		}
	}
	if os.Getenv("ORQ_API_KEY") == bearer {
		if err := os.Setenv("ORQ_API_KEY", fresh); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "warning: could not update ORQ_API_KEY with the refreshed token (%v)\n", err)
		}
	}
	return fresh, nil
}

// sessionServes reports whether u points at the session's server, so a
// session token that leaked into a request for another host is not refreshed
// against this one.
func sessionServes(session *Session, u *url.URL) bool {
	for _, base := range []string{session.APIBaseURL, session.V1BaseURL} {
		if b, err := url.Parse(base); err == nil && b.Host == u.Host {
			return true
		}
	}
	return false
}

// recordRefresh stores tok in the slot and remembers which slot the old token
// held. The session update lock spans the read and save, so concurrent refreshes
// merge onto the latest on-disk session instead of dropping one another's slots.
func recordRefresh(key, old string, tok StoredAccessToken) error {
	unlock, err := lockSessionUpdates()
	if err != nil {
		return err
	}
	defer unlock()

	current, err := ReadSession()
	if err != nil || current == nil {
		return err
	}
	if current.WorkspaceTokens == nil {
		current.WorkspaceTokens = map[string]StoredAccessToken{}
	}
	current.WorkspaceTokens[key] = tok
	expires := tok.ExpiresAt
	if exp, err := decodeJWTExpiry(old); err == nil {
		expires = formatISO(exp)
	}
	if current.StaleTokens == nil {
		current.StaleTokens = map[string]StaleToken{}
	}
	current.StaleTokens[tokenHash(old)] = StaleToken{Key: key, ExpiresAt: expires}
	return SaveSession(current)
}

// pruneStaleTokens drops records whose old token has expired: nobody can
// still be sending it.
func pruneStaleTokens(records map[string]StaleToken) map[string]StaleToken {
	if len(records) == 0 {
		return nil
	}
	kept := make(map[string]StaleToken, len(records))
	for hash, rec := range records {
		if !isExpired(rec.ExpiresAt, 0) {
			kept[hash] = rec
		}
	}
	return kept
}
