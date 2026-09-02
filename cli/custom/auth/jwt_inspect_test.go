package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func jwtToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, _ := json.Marshal(claims)
	seg := base64.RawURLEncoding.EncodeToString(payload)
	return "sk-orq-" + base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + seg + ".sig"
}

// Three token shapes are in circulation and the old reader handled one,
// silently reporting "" for the other two -- which is most keys in a real
// credentials file, and every key this CLI now mints.
func TestInspectTokenReadsAllThreeShapes(t *testing.T) {
	opaque := InspectToken("sk-orq-01HZXW2K7Y8Q9M0N1P2R3S4T5V-secretpart")
	if opaque.KeyID != "01HZXW2K7Y8Q9M0N1P2R3S4T5V" {
		t.Errorf("opaque: key id = %q", opaque.KeyID)
	}

	ws := InspectToken(jwtToken(t, map[string]any{"workspace_id": "ws-1", "key_id": "01HZXW2K7Y8Q9M0N1P2R3S4T5V"}))
	if ws.WorkspaceID != "ws-1" || ws.KeyID != "01HZXW2K7Y8Q9M0N1P2R3S4T5V" {
		t.Errorf("workspace key: %+v", ws)
	}
	if ws.ProjectID() != "" {
		t.Errorf("a workspace key must report no project, got %q", ws.ProjectID())
	}

	// A project key carries no key_id at all, which is why key diagnostics need
	// the token_prefix fallback rather than an id lookup.
	proj := InspectToken(jwtToken(t, map[string]any{"workspace_id": "ws-1", "projects": []string{"p-1"}}))
	if proj.ProjectID() != "p-1" || proj.KeyID != "" {
		t.Errorf("project key: %+v", proj)
	}

	// Dashboard-issued keys arrive without the prefix, and a real credentials
	// file holds both spellings.
	bare := InspectToken(strings.TrimPrefix(jwtToken(t, map[string]any{"workspace_id": "ws-1", "projects": []string{"p-1"}}), "sk-orq-"))
	if bare.ProjectID() != "p-1" {
		t.Errorf("bare JWT: %+v", bare)
	}

	if got := InspectToken("not-an-orq-token"); got.WorkspaceID != "" || got.KeyID != "" || len(got.Projects) != 0 {
		t.Errorf("a foreign token must yield nothing, got %+v", got)
	}
}
