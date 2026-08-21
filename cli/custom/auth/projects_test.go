package auth

import (
	"testing"
	"time"
)

// A service-account key can only be created by a workspace admin, so asking for
// one unconditionally locks every non-admin member out of `orq setup`. When the
// caller knows who is logged in, the key must be attributed to them.
func TestCreateAPIKeyPrefersUserOwnership(t *testing.T) {
	body, scoped := createAPIKeyBody(APIKeyRequest{Name: "orq-cli laptop", UserID: "user_123"})
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
	body, _ = createAPIKeyBody(APIKeyRequest{Name: "orq-cli laptop", UserID: "  "})
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
		body, scoped := createAPIKeyBody(APIKeyRequest{Name: "k", ProjectID: tc.projectID, UserID: "user_1"})
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
	body, _ := createAPIKeyBody(APIKeyRequest{Name: "k", UserID: "user_1", Access: map[string]string{"chat_completions": "write", "model": "read"}})
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

	// No map means the caller could not read the catalog; the legacy shape is
	// the deliberate fallback, and it must not carry an empty access map.
	body, _ = createAPIKeyBody(APIKeyRequest{Name: "k", UserID: "user_1"})
	if body["permission_mode"] != "all" {
		t.Errorf("permission_mode = %v, want all", body["permission_mode"])
	}
	if _, present := body["access"]; present {
		t.Error("an all-permissions key must not carry an access map")
	}
}

// The catalog is read at runtime, so a domain added to the gateway group is
// picked up without a release — and one added anywhere else is not.
func TestGatewayAccessGrantsOnlyGatewayDomains(t *testing.T) {
	domains := []Domain{
		{ID: "chat_completions", Group: "DOMAIN_GROUP_GATEWAY", Writable: true},
		{ID: "responses", Group: "3", Readable: true, Writable: true},
		{ID: "model", Group: "DOMAIN_GROUP_GATEWAY", Readable: true},
		{ID: "mcp_gateway", Group: "DOMAIN_GROUP_GATEWAY", Writable: true},
		{ID: "member", Group: "DOMAIN_GROUP_WORKSPACE_ADMIN", Readable: true, Writable: true},
		{ID: "prompt", Group: "DOMAIN_GROUP_PLATFORM", Readable: true, Writable: true},
		{ID: "future", Group: "DOMAIN_GROUP_SOMETHING_NEW", Writable: true},
	}
	got := GatewayAccess(domains)
	want := map[string]string{"chat_completions": "write", "responses": "write", "model": "read"}
	if len(got) != len(want) {
		t.Fatalf("access = %v, want %v", got, want)
	}
	for id, level := range want {
		if got[id] != level {
			t.Errorf("%s = %q, want %q", id, got[id], level)
		}
	}
}

// group arrives as the enum name from one encoder and its integer value from
// another; anything else must fail closed rather than grant.
func TestDomainGroupAcceptsBothEncodings(t *testing.T) {
	for raw, want := range map[string]bool{
		`"DOMAIN_GROUP_GATEWAY"`:  true,
		`3`:                       true,
		`"DOMAIN_GROUP_PLATFORM"`: false,
		`2`:                       false,
		`null`:                    false,
	} {
		var g domainGroup
		if err := g.UnmarshalJSON([]byte(raw)); err != nil && raw != "null" {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := g.isGateway(); got != want {
			t.Errorf("%s: isGateway = %v, want %v", raw, got, want)
		}
	}
}

// The raw secret is returned once. Losing the id with it leaves a key that can
// never be revoked or rotated by anything but a human in the dashboard.
func TestKeyIDFromToken(t *testing.T) {
	for token, want := range map[string]string{
		"sk-orq-01HZXW2K7Y8Q9M0N1P2R3S4T5V-abcdef": "01HZXW2K7Y8Q9M0N1P2R3S4T5V",
		"sk-orq-id-secret-with-dashes":             "id",
		"sk-orq-noseparator":                       "",
		"sk-other-01HZ-secret":                     "",
		"":                                         "",
	} {
		if got := keyIDFromToken(token); got != want {
			t.Errorf("%q: key id = %q, want %q", token, got, want)
		}
	}
}

// A key with no expiry is one nobody ever revisits. Absent ExpiresAt still has
// to mean "never expires", though, because that is the only safe shape when the
// caller has no lifetime policy to apply.
func TestCreateAPIKeyBodyCarriesExpiry(t *testing.T) {
	at := time.Date(2026, 11, 19, 8, 30, 0, 0, time.FixedZone("CET", 3600))
	body, _ := createAPIKeyBody(APIKeyRequest{Name: "k", UserID: "u", ExpiresAt: at})
	if got := body["expires_at"]; got != "2026-11-19T07:30:00Z" {
		t.Errorf("expires_at = %v, want the UTC RFC3339 form", got)
	}

	body, _ = createAPIKeyBody(APIKeyRequest{Name: "k", UserID: "u"})
	if _, present := body["expires_at"]; present {
		t.Error("a zero ExpiresAt must not send an expiry")
	}
}
