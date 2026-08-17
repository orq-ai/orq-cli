package auth

import "testing"

// A service-account key can only be created by a workspace admin, so asking for
// one unconditionally locks every non-admin member out of `orq setup`. When the
// caller knows who is logged in, the key must be attributed to them.
func TestCreateAPIKeyPrefersUserOwnership(t *testing.T) {
	body, scoped := createAPIKeyBody("orq-cli laptop", "", "user_123")
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
	body, _ = createAPIKeyBody("orq-cli laptop", "", "  ")
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
		body, scoped := createAPIKeyBody("k", tc.projectID, "user_1")
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
