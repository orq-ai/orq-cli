package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
)

// AuthzStaleCode is the 401 `code` the platform returns when the member
// snapshot a token was minted against is out of date: identity-api stamps a
// per-member Redis snapshot with the workspace's authz epoch at exchange time,
// and any project, team or membership write bumps that epoch. The token itself
// is still unexpired, so the expiry-driven refresh never replaces it; only a
// fresh exchange does (RES-1636).
const AuthzStaleCode = "authz_stale"

// StaleRetryTransport replays a request once with a freshly exchanged token
// when the server answers 401 authz_stale, the same refresh-and-retry-once the
// web app's token interceptor does. Only tokens this CLI minted and cached in
// the session are refreshed; an explicit API key never matches one, so its 401
// reaches the user unchanged.
type StaleRetryTransport struct {
	Base http.RoundTripper
}

// NewStaleRetryTransport wraps base, or http.DefaultTransport when nil.
func NewStaleRetryTransport(base http.RoundTripper) *StaleRetryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &StaleRetryTransport{Base: base}
}

func (t *StaleRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	bearer, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		return t.Base.RoundTrip(req)
	}
	// Buffer the body so the retry can resend it. Requests here are small
	// JSON payloads; the one-shot reader is otherwise gone after the first try.
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	res, err := t.Base.RoundTrip(withBody(req, req.Header.Get("Authorization"), body))
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || !isAuthzStale(raw) {
		return res, nil
	}
	fresh, ok := refreshStaleToken(req.Context(), bearer)
	if !ok {
		return res, nil
	}
	// One retry, straight to Base: a second authz_stale is reported as is.
	return t.Base.RoundTrip(withBody(req, "Bearer "+fresh, body))
}

func withBody(req *http.Request, authorization string, body []byte) *http.Request {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", authorization)
	if body != nil {
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		out.ContentLength = int64(len(body))
	}
	return out
}

func isAuthzStale(raw []byte) bool {
	var body struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Code == AuthzStaleCode
}

// refreshStaleToken swaps a stale cached workspace token for a new one. It
// finds the cache entry holding bearer, drops it, and re-exchanges for the
// same workspace/project pair. ORQ_API_KEY is updated when it carried the old
// token (the root pre-run puts it there), so later requests in this process
// send the new one instead of each paying for their own 401.
func refreshStaleToken(ctx context.Context, bearer string) (string, bool) {
	session, err := ReadSession()
	if err != nil || session == nil {
		return "", false
	}
	cacheKey := ""
	for key, tok := range session.WorkspaceTokens {
		if tok.Token == bearer {
			cacheKey = key
			break
		}
	}
	if cacheKey == "" {
		return "", false
	}
	workspaceKey, projectID, _ := strings.Cut(cacheKey, "#")
	delete(session.WorkspaceTokens, cacheKey)
	fresh, err := NewClient(session.APIBaseURL).WithContext(ctx).WithProject(projectID).WorkspaceToken(session, workspaceKey)
	if err != nil {
		return "", false
	}
	if os.Getenv("ORQ_API_KEY") == bearer {
		os.Setenv("ORQ_API_KEY", fresh)
	}
	return fresh, true
}
