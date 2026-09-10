package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// fakeStore is an in-memory secretStore. No CI runner reaches a real keychain
// (see the RES-1269 research, §12), so this is what exercises the externalized
// path: the version-2 layout, the read branch, migration, the fallback and
// ClearSession. The counters exist so a test can assert the store was *not*
// consulted, which is the whole content of the `=file` opt-out.
type fakeStore struct {
	label   string
	items   map[string]string
	getErr  error
	setErr  error
	delErr  error
	gets    int
	sets    int
	deletes int
}

func newFakeStore() *fakeStore {
	return &fakeStore{label: "fake keychain", items: map[string]string{}}
}

func (f *fakeStore) Name() string { return f.label }

func (f *fakeStore) Get(account string) (string, error) {
	f.gets++
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.items[account], nil // missing item: ("", nil), like the real thing
}

func (f *fakeStore) Set(account, secret string) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	f.items[account] = secret
	return nil
}

func (f *fakeStore) Delete(account string) error {
	f.deletes++
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.items, account)
	return nil
}

// useStore swaps the package's store resolution for a fake and resets the
// once-per-process fallback warning, so warning assertions do not depend on
// which test ran first in the binary.
func useStore(t *testing.T, store secretStore, err error) {
	t.Helper()
	prev := resolveStore
	resolveStore = func() (secretStore, error) { return store, err }
	resetFallbackWarning()
	t.Cleanup(func() {
		resolveStore = prev
		resetFallbackWarning()
	})
}

// captureStderr redirects the stream migration and the fallback warning write to.
func captureStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })
	return &out
}

func readSessionBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(SessionFilePath())
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	return raw
}

// The headline of the whole change: after a save, every secret is in the store
// and none of them is in the file, and a read puts them back so no caller above
// ReadSession can tell the difference.
func TestSaveSessionPutsTheSecretsInTheStoreAndNotInTheFile(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	useStore(t, store, nil)

	in := validSession("prod")
	// Values no JSON key is a substring of, so "the file still holds this" is
	// not satisfied by the `bootstrapToken` key name itself.
	in.BootstrapToken = StoredAccessToken{Token: "boot-jwt", ExpiresAt: "2099-01-01T00:00:00Z"}
	in.WorkspaceTokens["prod"] = StoredAccessToken{Token: "tok-prod", ExpiresAt: "2099-01-01T00:00:00Z"}
	in.GatewayKey = "sk-orq-MINTED"
	in.GatewayKeyID = "key_abc"
	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	raw := readSessionBytes(t)
	for _, secret := range []string{"refresh-abc", "boot-jwt", "tok-prod", "sk-orq-MINTED"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("session file still contains the secret %q:\n%s", secret, raw)
		}
	}

	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("session file is not JSON: %v", err)
	}
	if onDisk["version"] != float64(sessionVersionExternal) {
		t.Errorf("version on disk = %v, want %d", onDisk["version"], sessionVersionExternal)
	}
	if onDisk["secretStore"] != store.Name() {
		t.Errorf("secretStore on disk = %v, want %q", onDisk["secretStore"], store.Name())
	}
	// The gateway key's metadata stays readable without the store, so doctor's
	// expiry check and logout's revoke hint work with the keychain locked.
	if onDisk["gatewayKeyId"] != "key_abc" {
		t.Errorf("gatewayKeyId = %v, want it left in the file", onDisk["gatewayKeyId"])
	}

	got, err := ReadSession()
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got == nil {
		t.Fatal("ReadSession lost the session")
	}
	if got.RefreshToken != "refresh-abc" || got.BootstrapToken.Token != "boot-jwt" {
		t.Errorf("round trip lost the login tokens: %+v", got.SessionSecrets)
	}
	if got.WorkspaceTokens["prod"].Token != "tok-prod" {
		t.Errorf("round trip lost the workspace token: %+v", got.WorkspaceTokens)
	}
	if got.GatewayKey != "sk-orq-MINTED" {
		t.Errorf("round trip lost the gateway key: %q", got.GatewayKey)
	}
	if store.items[secretAccount(sessionHostForPath(SessionFilePath()))] == "" {
		t.Errorf("no item under the expected account; store holds %v", store.items)
	}
}

// A store that will not take the blob must not cost the user their login: the
// secrets go back into the file, in the layout every earlier release wrote, and
// the reason is said once.
func TestSaveSessionFallsBackInlineWhenTheStoreRefusesAndWarnsOnce(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	store.setErr = errors.New("keychain is locked")
	useStore(t, store, nil)
	out := captureStderr(t)

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("second SaveSession: %v", err)
	}

	var onDisk map[string]any
	if err := json.Unmarshal(readSessionBytes(t), &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["version"] != float64(sessionVersionInline) {
		t.Errorf("version on disk = %v, want %d after a fallback", onDisk["version"], sessionVersionInline)
	}
	if _, present := onDisk["secretStore"]; present {
		t.Errorf("secretStore marker written for a fallback save: %v", onDisk["secretStore"])
	}
	if onDisk["refreshToken"] != "refresh-abc" {
		t.Errorf("refreshToken = %v, want it written inline so the user stays logged in", onDisk["refreshToken"])
	}

	got, err := ReadSession()
	if err != nil || got == nil || got.RefreshToken != "refresh-abc" {
		t.Fatalf("a fallback save must still read back: (%+v, %v)", got, err)
	}
	if n := strings.Count(out.String(), "could not store session credentials"); n != 1 {
		t.Errorf("fallback warning printed %d times, want exactly 1:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), CredentialStoreEnvVar+"=file") {
		t.Errorf("the warning does not name the escape hatch:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "keychain is locked") {
		t.Errorf("the warning does not name the cause:\n%s", out.String())
	}
}

// A version-2 file whose item is gone is a specific, nameable failure — not the
// generic "malformed", which is what sends someone to `rm -rf ~/.orq`.
func TestReadSessionNamesMissingAndUnreadableSecretsSeparately(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	useStore(t, store, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	account := secretAccount(sessionHostForPath(SessionFilePath()))
	delete(store.items, account)
	got := InspectSession()
	if got.Status != StatusInvalid || got.Code != "session_secrets_missing" {
		t.Errorf("a vanished item = (%s, %s), want (invalid, session_secrets_missing)", got.Status, got.Code)
	}

	store.getErr = errors.New("interaction not allowed")
	got = InspectSession()
	if got.Status != StatusUnreadable || got.Code != "session_secrets_unreadable" {
		t.Errorf("a broken store = (%s, %s), want (unreadable, session_secrets_unreadable)", got.Status, got.Code)
	}
	if !strings.Contains(got.Message, "interaction not allowed") {
		t.Errorf("message = %q, want the store's reason in it", got.Message)
	}
}

// validateSession must accept both layouts, or a file one code path calls a
// login another calls broken.
func TestValidateSessionAcceptsBothLayoutsAndNothingElse(t *testing.T) {
	for version, wantOK := range map[int]bool{0: false, 1: true, 2: true, 3: false} {
		s := validSession("prod")
		s.Version = version
		err := validateSession(s)
		if wantOK != (err == nil) {
			t.Errorf("version %d: validateSession = %v, want ok=%v", version, err, wantOK)
		}
		if !wantOK && err != nil && !strings.Contains(err.Error(), "unsupported session version") {
			t.Errorf("version %d: error = %q, want it to name the version", version, err)
		}
	}
}

// The listing validates every row and reports needs-refresh from the bootstrap
// token's expiry, both of which move into the store.
func TestListSessionsReadsExternalizedSecrets(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	useStore(t, store, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != SessionStatusOK {
		t.Fatalf("rows = %+v, want one ok row", rows)
	}

	delete(store.items, secretAccount(rows[0].Host))
	rows, err = ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != SessionStatusInvalid {
		t.Fatalf("rows = %+v, want the row whose secrets are gone reported invalid", rows)
	}
}

func TestClearSessionRemovesTheStoreItemAndTheFile(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	useStore(t, store, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if len(store.items) != 0 {
		t.Errorf("store still holds %v after a logout", store.items)
	}
	if _, err := os.Stat(SessionFilePath()); !os.IsNotExist(err) {
		t.Errorf("session file survived the logout: %v", err)
	}

	// A store that cannot answer must not stop a logout: the file is what every
	// read path starts from, so removing it is the part that has to happen.
	store.delErr = errors.New("keychain is locked")
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession with a broken store: %v", err)
	}
	if _, err := os.Stat(SessionFilePath()); !os.IsNotExist(err) {
		t.Errorf("session file survived a logout with a broken store: %v", err)
	}
}

func TestCredentialStoreEnvVarDecidesWhichStoreIsUsed(t *testing.T) {
	fake := newFakeStore()
	available := func() (secretStore, error) { return fake, nil }
	unavailable := func() (secretStore, error) {
		s := unavailableStore{reason: "no keyring daemon"}
		return s, s.err()
	}

	t.Run("file opts out without touching the store", func(t *testing.T) {
		t.Setenv(CredentialStoreEnvVar, "file")
		store, err := storeFor(available)
		if err != nil {
			t.Fatalf("storeFor: %v", err)
		}
		if _, ok := store.(fileStore); !ok {
			t.Errorf("store = %T, want fileStore", store)
		}
		if fake.gets+fake.sets+fake.deletes != 0 {
			t.Errorf("the opt-out reached the store: %+v", fake)
		}
	})

	t.Run("keychain is a hard error when there is none", func(t *testing.T) {
		t.Setenv(CredentialStoreEnvVar, "keychain")
		if _, err := storeFor(unavailable); !errors.Is(err, errNoKeychain) {
			t.Errorf("err = %v, want errNoKeychain rather than a silent downgrade", err)
		}
		if store, err := storeFor(available); err != nil {
			t.Errorf("storeFor with a usable store = (%v, %v), want the store", store, err)
		}
	})

	for _, value := range []string{"", "auto", "AUTO", "  auto  "} {
		t.Run("auto falls back silently ("+value+")", func(t *testing.T) {
			t.Setenv(CredentialStoreEnvVar, value)
			store, err := storeFor(unavailable)
			if err != nil {
				t.Fatalf("auto must not error: %v", err)
			}
			if _, ok := store.(fileStore); !ok {
				t.Errorf("store = %T, want fileStore", store)
			}
		})
	}
}

// A platform with no keychain at all — Windows, a BSD — resolves to the file
// store under `auto` and writes exactly the layout that shipped before this
// change, silently: that is the documented state of the platform, not a
// degradation to warn about on every login.
func TestWithNoPlatformStoreEverySaveStaysInline(t *testing.T) {
	isolateHome(t)
	t.Setenv(CredentialStoreEnvVar, "auto")
	prev := resolveStore
	resolveStore = func() (secretStore, error) {
		return storeFor(func() (secretStore, error) {
			store := unavailableStore{reason: "no OS keychain support on plan9"}
			return store, store.err()
		})
	}
	resetFallbackWarning()
	t.Cleanup(func() {
		resolveStore = prev
		resetFallbackWarning()
	})
	out := captureStderr(t)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(readSessionBytes(t), &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["version"] != float64(sessionVersionInline) {
		t.Errorf("version = %v, want %d", onDisk["version"], sessionVersionInline)
	}
	if _, present := onDisk["secretStore"]; present {
		t.Errorf("secretStore = %v, want the key absent", onDisk["secretStore"])
	}
	if onDisk["refreshToken"] != "refresh-abc" {
		t.Errorf("refreshToken = %v, want it inline exactly as before", onDisk["refreshToken"])
	}
	if out.Len() != 0 {
		t.Errorf("an unavailable store warned on a platform that never had one:\n%s", out.String())
	}
}

// SaveSession owns Version now, which is why client.go no longer sets it.
func TestSaveSessionStampsTheVersionItself(t *testing.T) {
	isolateHome(t)
	unversioned := validSession("prod")
	unversioned.Version = 0
	if err := SaveSession(unversioned); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := ReadSession()
	if err != nil || got == nil {
		t.Fatalf("ReadSession: (%+v, %v)", got, err)
	}
	if got.Version != sessionVersionInline {
		t.Errorf("version = %d, want %d", got.Version, sessionVersionInline)
	}
}

// Opting out after having opted in has to work, or a user who sets
// ORQ_CREDENTIAL_STORE=file to debug something is locked out.
func TestOptingOutRewritesAnExternalizedSessionInline(t *testing.T) {
	isolateHome(t)
	store := newFakeStore()
	useStore(t, store, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	external, err := ReadSession()
	if err != nil || external == nil {
		t.Fatalf("ReadSession: (%+v, %v)", external, err)
	}

	resolveStore = func() (secretStore, error) { return fileStore{}, nil }
	if err := SaveSession(external); err != nil {
		t.Fatalf("SaveSession with the file store: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(readSessionBytes(t), &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["version"] != float64(sessionVersionInline) || onDisk["refreshToken"] != "refresh-abc" {
		t.Errorf("opting out left the file at %v with refreshToken %v", onDisk["version"], onDisk["refreshToken"])
	}
	if _, present := onDisk["secretStore"]; present {
		t.Errorf("the secretStore marker survived an opt-out: %v", onDisk["secretStore"])
	}
	got, err := ReadSession()
	if err != nil || got == nil || got.RefreshToken != "refresh-abc" {
		t.Fatalf("the opted-out session does not read back: (%+v, %v)", got, err)
	}
}
