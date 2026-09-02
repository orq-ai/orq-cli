package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// State is the per-profile state that is ours rather than bartolo's: the
// gateway key `orq setup` mints, its id and expiry, the workspace it was
// minted for, and the host a session was authenticated against.
//
// It shares credentials.json with bartolo's profiles but lives under its own
// `state` key, because a `profiles.<name>` entry means something to bartolo: a
// profile in force that carries no api_key fails every request instead of
// falling back to ORQ_API_KEY (bartolo >=0.8, apikey.lookupKey). A session
// login has exactly that shape — no API key, but a gateway key and a workspace
// — so storing this state as a profile made bartolo reject this CLI's own
// primary credential. A profile now exists only when it holds an API key.
const stateSection = "state"

// StateFields are the fields that moved out of the profile. `server` is not
// here: a profile that keeps an api_key keeps its server too, because bartolo
// resolves that one itself. Only a profile being removed hands its server over
// (see migrateProfileState).
var StateFields = []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "workspace"}

func stateKey(profile, field string) string {
	return stateSection + "." + profile + "." + field
}

// StateValue reads a field for the active profile.
func StateValue(field string) string { return StateValueOf(ActiveProfile(), field) }

// StateValueOf reads a field for a named profile. The pre-migration location
// is the fallback, so a value is never lost to a migration that has not run
// yet — the field is read the same way whether or not this process migrated.
func StateValueOf(profile, field string) string {
	if bartolocli.Creds == nil || profile == "" {
		return ""
	}
	if v := strings.TrimSpace(bartolocli.Creds.GetString(stateKey(profile, field))); v != "" {
		return v
	}
	return strings.TrimSpace(bartolocli.Creds.GetString("profiles." + profile + "." + field))
}

// SetStateValue records a field in memory. Callers persist with their existing
// credentials write; this only decides where the value goes.
func SetStateValue(profile, field, value string) {
	if bartolocli.Creds == nil || profile == "" {
		return
	}
	bartolocli.Creds.Set(stateKey(profile, field), value)
}

// StateProfiles lists the profiles that have state of ours but no bartolo
// profile, so `auth list-profiles` still shows a session-only login after its
// keyless profile is gone.
func StateProfiles() []string {
	if bartolocli.Creds == nil {
		return nil
	}
	names := make([]string, 0)
	for name := range bartolocli.Creds.GetStringMap(stateSection) {
		// Clearing a field writes an empty string rather than deleting it (viper
		// cannot delete), so an entry emptied by logout is not a profile.
		for _, value := range StateOf(name) {
			if strings.TrimSpace(value) != "" {
				names = append(names, name)
				break
			}
		}
	}
	return names
}

// StateOf returns every recorded field for a profile, for callers that render
// rather than look one up.
func StateOf(profile string) map[string]string {
	if bartolocli.Creds == nil || profile == "" {
		return nil
	}
	return bartolocli.Creds.GetStringMapString(stateSection + "." + profile)
}

// MigrateProfileState moves our fields out of `profiles.<name>` and removes
// the profiles that are left holding no API key — the ones bartolo would fail
// every request on. It rewrites credentials.json directly and then reloads the
// process-wide handle, because viper (which backs bartolo's credentials file)
// can set a key but cannot delete one.
//
// It is a no-op once done, so it can run on every invocation: needsMigration
// answers from the already-loaded file, and nothing touches the disk until
// there is something to move.
func MigrateProfileState(configDir string) error {
	if bartolocli.Creds == nil || !needsMigration() {
		return nil
	}

	path := filepath.Join(configDir, "credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		// No file means the in-memory profiles came from somewhere else (a
		// test, or a caller that seeded Creds); there is nothing to rewrite.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}

	removed := migrateProfileState(doc)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteSecretFile(path, out); err != nil {
		return err
	}

	reloaded, err := bartolocli.NewCredentialsFile(configDir)
	if err != nil {
		return err
	}
	bartolocli.Creds = reloaded

	return dropRemovedSelection(configDir, removed)
}

// migrateProfileState is the pure half: it edits doc in place and reports the
// profiles it removed.
func migrateProfileState(doc map[string]any) []string {
	profiles, _ := doc["profiles"].(map[string]any)
	if len(profiles) == 0 {
		return nil
	}
	state, _ := doc[stateSection].(map[string]any)
	if state == nil {
		state = map[string]any{}
	}

	var removed []string
	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok {
			continue
		}
		entry, _ := state[name].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}

		fields := StateFields
		// A profile with no API key is going away, so its server binding has
		// to travel with the rest of its state or `orq --profile acme` stops
		// finding acme's host.
		if stringField(profile, "api_key") == "" {
			fields = append(append([]string{}, StateFields...), "server")
		}
		for _, field := range fields {
			if v := stringField(profile, field); v != "" && stringField(entry, field) == "" {
				entry[field] = v
			}
			delete(profile, field)
		}
		if len(entry) > 0 {
			state[name] = entry
		}

		if stringField(profile, "api_key") == "" {
			delete(profiles, name)
			removed = append(removed, name)
		}
	}

	if len(state) > 0 {
		doc[stateSection] = state
	}
	return removed
}

// needsMigration answers from the loaded credentials: anything of ours still
// sitting in a profile, or a profile with no API key for bartolo to trip over.
func needsMigration() bool {
	for _, value := range bartolocli.Creds.GetStringMap("profiles") {
		profile, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if stringField(profile, "api_key") == "" {
			return true
		}
		for _, field := range StateFields {
			if stringField(profile, field) != "" {
				return true
			}
		}
	}
	return false
}

// dropRemovedSelection clears a persisted profile selection that names a
// profile this migration deleted. Left behind, bartolo resolves it as in force
// and fails every request with "profile is not configured" — the same dead end
// under a different message.
func dropRemovedSelection(configDir string, removed []string) error {
	if len(removed) == 0 {
		return nil
	}
	selected := strings.TrimSpace(viper.GetString("profile-selected"))
	if selected == "" {
		return nil
	}
	var gone bool
	for _, name := range removed {
		if strings.EqualFold(name, selected) {
			gone = true
			break
		}
	}
	if !gone {
		return nil
	}
	viper.Set("profile-selected", "")

	path := filepath.Join(configDir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	delete(doc, "profile-selected")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecretFile(path, out)
}

func stringField(m map[string]any, field string) string {
	if m == nil {
		return ""
	}
	v, _ := m[field].(string)
	return strings.TrimSpace(v)
}

// WriteSecretFile replaces a secret-bearing file through a temp file in the
// same directory, so a crash or a concurrent reader never sees a half-written
// credentials.json — truncating the real file in place can lose every stored
// credential, not only the one being written. This is what bartolo's own
// credentials writer does; it is unexported there, hence the copy.
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
