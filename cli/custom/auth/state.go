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
// primary credential. Migration restores this CLI's invariant that its bartolo
// profile exists only when it holds an API key before the next command.
const stateSection = "state"

// StateFields are the fields that moved out of the profile. `server` is not
// here: a profile that keeps an api_key keeps its server too, because bartolo
// resolves that one itself. Keyless session state records its server here at
// runtime, and migration moves it here when removing the profile.
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

// StateProfiles lists profile names with non-empty state. Callers that combine
// these names with bartolo profiles are responsible for deduplicating them.
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
// our profiles that are left holding no API key — the ones bartolo would fail
// every request on. It rewrites credentials.json directly and then reloads the
// process-wide handle, because viper (which backs bartolo's credentials file)
// can set a key but cannot delete one.
//
// It is a no-op once migration and selection repair are done, so it can run on
// every invocation: needsMigration answers from the already-loaded file, and
// the disk changes only when state must move or selection must be repaired.
func MigrateProfileState(configDir string) error {
	if bartolocli.Creds == nil {
		return nil
	}
	if needsMigration() {
		path := filepath.Join(configDir, "credentials.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			// No file means the in-memory profiles came from somewhere else (a
			// test, or a caller that seeded Creds); there is nothing to rewrite.
			if errors.Is(err, fs.ErrNotExist) {
				return repairProfileSelection(configDir)
			}
			return err
		}
		doc := map[string]any{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}

		migrateProfileState(doc)

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
	}

	return repairProfileSelection(configDir)
}

// migrateProfileState is the pure half: it edits doc in place.
func migrateProfileState(doc map[string]any) {
	profiles, _ := doc["profiles"].(map[string]any)
	if len(profiles) == 0 {
		return
	}
	state, _ := doc[stateSection].(map[string]any)
	if state == nil {
		state = map[string]any{}
	}

	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok {
			continue
		}
		stateName, entry := mapEntryEqualFold(state, name)
		if stateName == "" {
			stateName = name
		}
		if entry == nil {
			entry = map[string]any{}
		}

		// Only a profile carrying a field of ours is one this CLI wrote. A
		// keyless profile someone else left here is theirs: bartolo will
		// reject it if it is ever selected, which is the honest answer, and
		// deleting a credentials entry we do not own is not.
		ours := ownedProfile(profile, entry)

		fields := StateFields
		// A profile with no API key is going away, so its server binding has
		// to travel with the rest of its state or `orq --profile acme` stops
		// finding acme's host.
		if ours && stringField(profile, "api_key") == "" {
			fields = append(append([]string{}, StateFields...), "server")
		}
		if ours {
			for _, field := range fields {
				if hasValue(entry, field) {
					delete(profile, field)
					continue
				}
				if hasValue(profile, field) {
					entry[field] = profile[field]
					delete(profile, field)
				}
			}
		}
		if len(entry) > 0 {
			state[stateName] = entry
		}

		if ours && stringField(profile, "api_key") == "" {
			delete(profiles, name)
		}
	}

	if len(state) > 0 {
		doc[stateSection] = state
	}
}

// ownedProfile reports whether a profile carries a field only this CLI writes,
// or is the keyless profile husk proved ours by its sibling state entry.
func ownedProfile(profile, entry map[string]any) bool {
	keyless := stringField(profile, "api_key") == ""
	if keyless && len(entry) > 0 {
		return true
	}
	// A keyless profile that still names an auth `type` is also ours: bartolo's
	// own writers always store an api_key beside the type, and versions before
	// this split left exactly that husk behind on logout — an empty api_key, a
	// type, and a blank workspace, with nothing under `state` to prove it.
	if keyless && hasValue(profile, "type") {
		return true
	}
	for _, field := range StateFields {
		// A workspace alone does not prove ownership of a keyless profile:
		// other tools can record one while configuring their own credentials.
		if hasValue(profile, field) && (!keyless || field != "workspace") {
			return true
		}
	}
	return false
}

// needsMigration answers from the loaded credentials: a profile of ours still
// holding fields that belong under `state`.
func needsMigration() bool {
	profiles := bartolocli.Creds.GetStringMap("profiles")
	state := bartolocli.Creds.GetStringMap(stateSection)
	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok {
			continue
		}
		_, entry := mapEntryEqualFold(state, name)
		if ownedProfile(profile, entry) {
			return true
		}
	}
	return false
}

func mapEntryEqualFold(m map[string]any, name string) (string, map[string]any) {
	if entry, ok := m[name].(map[string]any); ok {
		return name, entry
	}
	for key, value := range m {
		if !strings.EqualFold(key, name) {
			continue
		}
		entry, _ := value.(map[string]any)
		return key, entry
	}
	return "", nil
}

// repairProfileSelection clears a persisted selection naming no configured
// profile. Left behind, bartolo v0.9.0 resolves it as in force and fails every
// request with `profile "name" is not configured` — the same dead end under a
// different message.
func repairProfileSelection(configDir string) error {
	selected := strings.TrimSpace(viper.GetString("profile-selected"))
	if selected == "" {
		return nil
	}
	if bartolocli.ProfileExists(selected) {
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

func hasValue(m map[string]any, field string) bool {
	if m == nil {
		return false
	}
	v, ok := m[field]
	if !ok {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return v != nil
}

// WriteSecretFile replaces a secret-bearing file through a temp file in the
// same directory, so a crash or a concurrent reader never sees a half-written
// credentials.json — truncating the real file in place can lose every stored
// credential, not only the one being written. This matches bartolo v0.9.0's
// own credentials writer; it is unexported there, hence the copy.
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
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
