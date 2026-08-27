package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A service-account key can only be created by a workspace admin, so asking for
// one unconditionally locks every non-admin member out of `orq setup`. When the
// caller knows who is logged in, the key must be attributed to them.
func TestCreateAPIKeyPrefersUserOwnership(t *testing.T) {
	body, scoped := createAPIKeyBody(APIKeyRequest{name: "orq-cli laptop", userID: "user_123", access: GatewayAccess()})
	if scoped {
		t.Error("no project id given, key should not claim project scope")
	}
	owner, ok := body["owner"].(map[string]any)
	if !ok {
		t.Fatalf("owner is %T, want a map", body["owner"])
	}
	if owner["type"] != "user" || owner["user_id"] != "user_123" {
		t.Errorf("owner = %v, want a user key attributed to user_123", owner)
	}

	// No session, no user to attribute to: the service account is the only
	// remaining option, and this path never mints anyway.
	body, _ = createAPIKeyBody(APIKeyRequest{name: "orq-cli laptop", userID: "  ", access: GatewayAccess()})
	if owner := body["owner"].(map[string]any); owner["type"] != "service_account" {
		t.Errorf("owner without a user id = %v, want service_account", owner)
	}
}

// The two endpoints disagree about project id formats, so a UUID from
// /v2/projects must not be sent as a scope the key endpoint will reject.
func TestCreateAPIKeyScopesOnlyOnULIDs(t *testing.T) {
	for _, tc := range []struct {
		projectID string
		wantScope bool
	}{
		{"01HZXW2K7Y8Q9M0N1P2R3S4T5V", true},
		{"proj_01HZXW2K7Y8Q9M0N1P2R3S4T5V", true},
		{"019def44-a743-7000-a442-c0db96b06699", false},
		{"", false},
	} {
		body, scoped := createAPIKeyBody(APIKeyRequest{name: "k", projectID: tc.projectID, userID: "user_1", access: GatewayAccess()})
		if scoped != tc.wantScope {
			t.Errorf("%q: scopedToProject = %v, want %v", tc.projectID, scoped, tc.wantScope)
		}
		scope := body["project_scope"].(map[string]any)
		wantMode := "all"
		if tc.wantScope {
			wantMode = "single"
		}
		if scope["mode"] != wantMode {
			t.Errorf("%q: mode = %v, want %v", tc.projectID, scope["mode"], wantMode)
		}
	}
}

// A coding-agent key is written in cleartext into another program's config, so
// it must hold gateway verbs and nothing else. Under permission_mode "all" the
// server resolves the whole catalog, including member, billing and sso.
func TestCreateAPIKeyBodyRestrictsWhenGivenAnAccessMap(t *testing.T) {
	body, _ := createAPIKeyBody(APIKeyRequest{name: "k", userID: "user_1", access: map[string]string{"chat_completions": "write", "model": "read"}})
	if body["permission_mode"] != "restricted" {
		t.Errorf("permission_mode = %v, want restricted", body["permission_mode"])
	}
	access, ok := body["access"].(map[string]string)
	if !ok {
		t.Fatalf("access is %T, want a map", body["access"])
	}
	if access["chat_completions"] != "write" || access["model"] != "read" {
		t.Errorf("access = %v, want lowercase levels per domain", access)
	}

	// source is the field the server acts on; permission_mode and access are
	// sent for platform-api's proto service, which may answer instead.
	if body["source"] != "router" {
		t.Errorf("source = %v, want router, which is what restricts the key", body["source"])
	}
}

// A coding-agent key must hold gateway verbs and nothing else. The map is local
// because the catalog endpoint is unreachable in production — see the comment on
// gatewayAccessMap — and a fetch that fails means minting with full permissions.
func TestGatewayAccessGrantsOnlyGatewayDomains(t *testing.T) {
	access := GatewayAccess()

	for _, denied := range []string{
		"member", "billing", "sso", "group", "workspace", "audit-log", // workspace admin
		"prompt", "dataset", "agent", "traces", "logs", "project", "file", // platform
		"mcp_gateway", // gateway group, but MCP data plane rather than wiring
	} {
		if level, granted := access[denied]; granted {
			t.Errorf("%s granted at %q to a gateway-only key", denied, level)
		}
	}

	// Execution plus the catalogue read every coding agent needs to list models.
	for domain, want := range map[string]string{
		"chat_completions": "write",
		"responses":        "write",
		"count_tokens":     "write",
		"model":            "read",
	} {
		if access[domain] != want {
			t.Errorf("%s = %q, want %q", domain, access[domain], want)
		}
	}

	// model is read-only in the catalog; asking for write resolves to nothing.
	for domain, level := range access {
		if level != "read" && level != "write" {
			t.Errorf("%s has level %q, want read or write", domain, level)
		}
	}
}

// Callers pass the map straight into a request body; a shared map would let one
// mint mutate the next.
func TestGatewayAccessReturnsACopy(t *testing.T) {
	first := GatewayAccess()
	first["member"] = "write"
	if _, leaked := GatewayAccess()["member"]; leaked {
		t.Error("mutating the returned map changed the shared one")
	}
}

// The raw secret is returned once. Losing the id with it leaves a key that can
// never be revoked or rotated by anything but a human in the dashboard.
func TestKeyIDFromToken(t *testing.T) {
	for token, want := range map[string]string{
		"sk-orq-01HZXW2K7Y8Q9M0N1P2R3S4T5V-abcdef": "01HZXW2K7Y8Q9M0N1P2R3S4T5V",
		"sk-orq-id-secret-with-dashes":             "",
		"sk-orq-noseparator":                       "",
		"sk-other-01HZ-secret":                     "",
		"":                                         "",
		// A router token: same prefix, a JWT after it, and base64url has dashes.
		"sk-orq-eyJhbGciOiJIUzI1NiJ9.eyJ3-x.sig": "",
	} {
		if got := KeyIDFromToken(token); got != want {
			t.Errorf("%q: key id = %q, want %q", token, got, want)
		}
	}
}

// A key with no expiry is one nobody ever revisits, so there is no longer a
// request shape that omits one: NewAPIKeyRequest refuses a zero expiry, and the
// body always carries the field.
func TestCreateAPIKeyBodyCarriesExpiry(t *testing.T) {
	at := time.Date(2026, 11, 19, 8, 30, 0, 0, time.FixedZone("CET", 3600))
	body, _ := createAPIKeyBody(APIKeyRequest{name: "k", userID: "u", access: GatewayAccess(), expiresAt: at})
	// "expiration": the live endpoint rejects the spec's "expires_at" outright
	// with `unknown field`, which fails the whole mint.
	if got := body["expiration"]; got != "2026-11-19T07:30:00Z" {
		t.Errorf("expiration = %v, want the UTC RFC3339 form", got)
	}
	if _, wrong := body["expires_at"]; wrong {
		t.Error("sent expires_at, which the live API rejects")
	}

}

// The reason the fields are unexported. As a plain struct, APIKeyRequest{Name:
// "x"} asked for a workspace-wide, never-expiring, all-permissions key owned by
// a service account — the widest key this API issues, from the shortest literal
// anyone would write. The constructor makes the two dangerous omissions
// unrepresentable rather than documented.
func TestNewAPIKeyRequestRefusesTheDangerousOmissions(t *testing.T) {
	at := time.Now().Add(time.Hour)
	for name, tc := range map[string]struct {
		keyName   string
		access    map[string]string
		expiresAt time.Time
	}{
		"no access map": {"k", nil, at},
		"empty access":  {"k", map[string]string{}, at},
		"no expiry":     {"k", GatewayAccess(), time.Time{}},
		"no name":       {"  ", GatewayAccess(), at},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAPIKeyRequest(tc.keyName, tc.access, tc.expiresAt); err == nil {
				t.Error("accepted, so the permissive default is still one field away")
			}
		})
	}

	req, err := NewAPIKeyRequest("orq-cli gateway laptop", GatewayAccess(), at, WithUser("user_1"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := createAPIKeyBody(req)
	if body["source"] != "router" || body["permission_mode"] != "restricted" {
		t.Errorf("a constructed request must still restrict: %v", body)
	}
	if owner := body["owner"].(map[string]any); owner["user_id"] != "user_1" {
		t.Errorf("WithUser did not reach the body: %v", owner)
	}
}

// The live endpoint answers flat; the proto service nests under api_key; and
// the token carries the id regardless. All three have to resolve, or the key id
// is lost at the one moment it is recoverable.
func TestCreateAPIKeyReadsTheKeyIDFromEitherShape(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"flat, as the live API answers": {`{"token":"sk-orq-TOK-secret","id":"FLAT"}`, "FLAT"},
		"nested, as the proto service":  {`{"token":"sk-orq-TOK-secret","api_key":{"id":"NESTED"}}`, "NESTED"},
		"neither, parsed from token":    {`{"token":"sk-orq-01HZXW2K7Y8Q9M0N1P2R3S4T5V-secret"}`, "01HZXW2K7Y8Q9M0N1P2R3S4T5V"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, keyID, _, err := NewClient(srv.URL).CreateAPIKey("t", APIKeyRequest{name: "k", userID: "u", access: GatewayAccess(), expiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if keyID != tc.want {
				t.Errorf("key id = %q, want %q", keyID, tc.want)
			}
		})
	}
}

// "" was the old return for a 200 that named no workspace, and setup reads a
// blank workspace as "no mismatch" — so a renamed field would turn both the
// reuse guard and verification into no-ops that still report success.
func TestKeyWorkspaceRejectsAResponseWithNoWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "named", body: `{"settings":{"key":"acme"}}`, want: "acme"},
		{name: "trimmed", body: `{"settings":{"key":"  acme  "}}`, want: "acme"},
		{name: "empty key", body: `{"settings":{"key":""}}`, wantErr: true},
		{name: "field renamed", body: `{"settings":{"slug":"acme"}}`, wantErr: true},
		{name: "empty body", body: `{}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			got, err := NewClient(srv.URL).KeyWorkspace("k")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("workspace = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("KeyWorkspace: %v", err)
			}
			if got != tc.want {
				t.Errorf("workspace = %q, want %q", got, tc.want)
			}
		})
	}
}

// These three predicates decide whether setup discards the saved key. Measured
// against api.orq.ai: a revoked gateway key gets 401 from workspace-settings, a
// live one belonging to another workspace gets 200, a gateway key gets 403 from
// /v2/api-keys/{id} ("This API key type cannot access this endpoint"), and a
// session token asking for a key held in another workspace gets 404. Widening
// Unauthorized back over 403 makes every run mint a replacement for a working
// credential the moment a route stops serving gateway keys.
func TestAPIErrorPredicatesMatchTheStatusesSetupBranchesOn(t *testing.T) {
	for _, tc := range []struct {
		status                      int
		unauth, forbidden, notFound bool
	}{
		{status: http.StatusUnauthorized, unauth: true},
		{status: http.StatusForbidden, forbidden: true},
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusInternalServerError},
	} {
		err := error(&APIError{Status: tc.status, Msg: "x"})
		if got := Unauthorized(err); got != tc.unauth {
			t.Errorf("Unauthorized(%d) = %v, want %v", tc.status, got, tc.unauth)
		}
		if got := Forbidden(err); got != tc.forbidden {
			t.Errorf("Forbidden(%d) = %v, want %v", tc.status, got, tc.forbidden)
		}
		if got := NotFound(err); got != tc.notFound {
			t.Errorf("NotFound(%d) = %v, want %v", tc.status, got, tc.notFound)
		}
	}
	for _, err := range []error{fmt.Errorf("dial tcp: connection refused"), nil} {
		if Unauthorized(err) || Forbidden(err) || NotFound(err) {
			t.Errorf("%v must not read as any API refusal", err)
		}
	}
}
