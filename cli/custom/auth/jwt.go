package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func decodeBase64Segment(seg string) ([]byte, error) {
	if data, err := base64.RawURLEncoding.DecodeString(seg); err == nil {
		return data, nil
	}
	if data, err := base64.URLEncoding.DecodeString(seg); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(seg); err == nil {
		return data, nil
	}
	return base64.StdEncoding.DecodeString(seg)
}

func decodeJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, errors.New("invalid JWT format")
	}
	payload, err := decodeBase64Segment(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, errors.New("token missing exp claim")
	}
	return time.Unix(claims.Exp, 0).UTC(), nil
}

// TokenClaims is what a credential says about itself, read locally with no
// network call. Every field is best-effort: the opaque token shape carries only
// a key id, and a project-scoped JWT carries no key id at all.
type TokenClaims struct {
	WorkspaceID string
	KeyID       string
	Projects    []string
}

// ProjectID returns the single project a credential is scoped to, or empty when
// it reaches every project in the workspace.
func (c TokenClaims) ProjectID() string {
	if len(c.Projects) == 1 {
		return c.Projects[0]
	}
	return ""
}

// InspectToken reads what an `sk-orq-` credential can tell us about itself.
// Three shapes exist today and they answer different questions:
//
//	sk-orq-<ULID>-<secret>  opaque       key id only
//	[sk-orq-]<JWT>          workspace    workspace_id, key_id
//	[sk-orq-]<JWT>          project      workspace_id, projects[] and no key_id
//
// The `sk-orq-` prefix is optional on the JWT shapes: keys handed out by the
// dashboard carry none.
//
// Diagnostics only. The claims are unverified — anyone holding the token could
// have written them — so nothing that grants access may read this. It exists so
// `orq status` and `orq doctor` can say what a credential is without spending a
// round trip, and so a project key stops reporting as "unknown".
func InspectToken(token string) TokenClaims {
	// The prefix is optional: keys minted through the dashboard are handed out
	// as a bare JWT, and both spellings turn up in a real credentials file.
	rest := strings.TrimPrefix(strings.TrimSpace(token), "sk-orq-")
	// The JWT shapes have three dot-separated segments; the opaque shape has none.
	if parts := strings.Split(rest, "."); len(parts) == 3 {
		payload, err := decodeBase64Segment(parts[1])
		if err != nil {
			return TokenClaims{}
		}
		var claims struct {
			WorkspaceID string   `json:"workspace_id"`
			KeyID       string   `json:"key_id"`
			Projects    []string `json:"projects"`
		}
		if err := json.Unmarshal(payload, &claims); err != nil {
			return TokenClaims{}
		}
		return TokenClaims{WorkspaceID: claims.WorkspaceID, KeyID: claims.KeyID, Projects: claims.Projects}
	}
	return TokenClaims{KeyID: KeyIDFromToken(token)}
}

// formatISO formats a time as an ISO-8601 string with millisecond precision
// (e.g. 2026-04-13T12:34:56.000Z), matching JavaScript's Date.toISOString().
func formatISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// parseISO parses an ISO-8601 timestamp accepting both fractional-second and
// no-fractional-second forms.
func parseISO(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	layouts := []string{
		"2006-01-02T15:04:05.000Z07:00",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp: %s", s)
}

func isExpired(expiresAt string, skewSeconds int) bool {
	t, err := parseISO(expiresAt)
	if err != nil {
		return true
	}
	return time.Now().Add(time.Duration(skewSeconds) * time.Second).After(t)
}
