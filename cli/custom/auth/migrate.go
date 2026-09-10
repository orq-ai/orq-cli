package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// ownedFields are the fields only this CLI ever wrote into a bartolo profile.
// Their presence is what marks a profile as ours to migrate; a keyless profile
// without any of them belongs to someone else and is left alone.
var ownedFields = []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "gateway_key_project", "workspace"}

// MigrateLayout brings an older ~/.orq up to the current layout in one pass:
// session files are named by host, and everything this CLI records about a
// login lives in that login's file rather than in bartolo's credentials.json.
// It returns an error rather than warning: a migration that did not run leaves
// a keyless profile bartolo fails every request on.
//
// Sessions are always under $HOME/.orq/sessions (sessionsDir, pre-existing
// behaviour); configDir only governs where credentials.json and config.json
// are read and rewritten, so `--config-directory` never relocates sessions.
//
// Idempotent and cheap once done — every step checks before touching disk.
func MigrateLayout(configDir string) error {
	renamed, err := migrateSessionFiles()
	if err != nil {
		return err
	}
	if err := migrateCredentials(configDir, renamed); err != nil {
		return err
	}
	if err := migrateSecretsToStore(); err != nil {
		return err
	}
	// Runs unconditionally, not only when migrateCredentials just deleted a
	// profile: a prior interrupted run (or one whose migrateCredentials wrote
	// credentials.json but then died before clearing the selection) can leave
	// profile-selected naming a profile that is already gone, with nothing
	// left in this run's state to say so.
	return reconcileSelectedProfile(configDir)
}

// migrateSessionFiles renames sessions/<name>.json to sessions/<host>.json and
// reports old name → host. The pre-multi-profile ~/.orq/session.json joins in.
// Two files for one host: the newest by mtime wins, the other is kept as
// <name>.json.deprecated so nothing a user might still want is deleted.
func migrateSessionFiles() (map[string]string, error) {
	dir := sessionsDir()
	renamed := map[string]string{}

	if legacy := legacySessionFilePath(); fileExists(legacy) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		// Kept as a .json name (not the bare-append uniquePath uses for
		// .deprecated targets) so, if it collided, it still gets picked up
		// and renamed to its real host name by the scan just below.
		target := uniqueJSONPath(filepath.Join(dir, "session.json"))
		if err := os.Rename(legacy, target); err != nil {
			return nil, err
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return renamed, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	// Group every session file by the host it resolves to, so a clash between
	// two names is decided once, from all the candidates, rather than by
	// whichever pair happens to collide first in directory order.
	byHost := map[string][]string{}
	for _, name := range names {
		path := filepath.Join(dir, name)
		s, err := readSessionFile(path)
		if err != nil {
			// Renaming a file we cannot even parse would gain nothing, so an
			// unreadable candidate is not worth failing the command over — and
			// this runs before EVERY command, so failing here would take
			// `doctor` and `auth login` down with it: the two commands someone
			// with a broken ~/.orq reaches for. Say what was skipped and move
			// on. attachToSession stays fail-closed, because that is the one
			// place a field's last copy is about to be deleted.
			fmt.Fprintf(bartolocli.Stderr, "skipped %s: not a readable session file (%v). Delete it or move it aside.\n", path, err)
			continue
		}
		if s == nil || s.APIBaseURL == "" {
			continue // not a session of ours; leave it where it is
		}
		host := SessionHost(s.APIBaseURL)
		renamed[strings.TrimSuffix(name, ".json")] = host
		byHost[host] = append(byHost[host], path)
	}

	for host, paths := range byHost {
		target := sessionPathFor(host)
		if len(paths) == 1 && paths[0] == target {
			continue // already named for its host
		}
		targetIsCandidate := false
		for _, path := range paths {
			if path == target {
				targetIsCandidate = true
				break
			}
		}
		// A file at the destination that was not grouped above is unreadable or
		// lacks an API base URL. It may still contain the user's only session;
		// never let os.Rename replace it silently on platforms where rename
		// overwrites an existing file. Leave this host's files where they are
		// rather than erroring: an unmigrated login is recoverable, a CLI that
		// refuses to run is not.
		if fileExists(target) && !targetIsCandidate {
			fmt.Fprintf(bartolocli.Stderr,
				"left %s alone: it is not a session this CLI can read, so the login in %s keeps its current name. Delete or move the unreadable file, then re-run.\n",
				target, strings.Join(paths, ", "))
			for _, p := range paths {
				delete(renamed, strings.TrimSuffix(filepath.Base(p), ".json"))
			}
			continue
		}
		winner := paths[0]
		for _, p := range paths[1:] {
			if newerThan(p, winner) {
				winner = p
			}
		}
		for _, p := range paths {
			if p == winner {
				continue
			}
			loser := uniquePath(deprecatedName(p))
			if err := os.Rename(p, loser); err != nil {
				return nil, err
			}
			fmt.Fprintf(bartolocli.Stderr, "kept the newer login for %s; the other is at %s\n", host, loser)
		}
		if winner != target {
			if err := os.Rename(winner, target); err != nil {
				return nil, err
			}
		}
	}
	return renamed, nil
}

// migrateCredentials moves our fields out of credentials.json — both the
// pre-#63 shape (inside profiles.<name>) and #63's `state.<name>` — onto the
// session of the host they belong to, and deletes a profile of ours that is
// left with no api_key. The file is rewritten only when something moved.
func migrateCredentials(configDir string, renamed map[string]string) error {
	if bartolocli.Creds == nil || !credentialsNeedMigration() {
		return nil
	}
	path := filepath.Join(configDir, "credentials.json")
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

	profiles, _ := doc["profiles"].(map[string]any)
	state, _ := doc["state"].(map[string]any)

	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok || !ownedProfile(profile) {
			continue
		}
		fields := collectOwned(profile)
		for _, f := range ownedFields {
			delete(profile, f)
		}
		apiKeyProfile := stringField(profile, "api_key") != ""
		// Gateway metadata belongs to the browser login even when an older CLI
		// happened to store it beside a real API-key profile. A lone workspace
		// on a brought-key profile has no such provenance and is intentionally
		// dropped.
		if !apiKeyProfile || hasGatewayMetadata(fields) {
			// A session login's profile: its server travels with the fields,
			// and an API-key profile's server identifies the matching login.
			fields["server"] = stringField(profile, "server")
			if err := attachToSession(name, fields, renamed); err != nil {
				return err
			}
		}
		if !apiKeyProfile {
			// The entry itself has no reason to exist. attachToSession erroring
			// above means its fields are about to have no other copy, so bail
			// before credentials.json is rewritten.
			delete(profiles, name)
		}
		// An API-key profile keeps its key, type and server; the workspace we
		// recorded next to it is dropped — a brought key has no known workspace.
	}
	for name, value := range state {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if err := attachToSession(name, stringMap(entry), renamed); err != nil {
			return err
		}
		// #63 deleted the companion keyless profile itself when it wrote
		// state.<name>, so this should not be reachable — but a state entry
		// is proof of ownership for a same-named keyless profile, so if one
		// is still there, it goes too rather than surviving keyless forever.
		if key, profile, ok := profileEntryFold(profiles, name); ok && stringField(profile, "api_key") == "" {
			delete(profiles, key)
		}
	}
	delete(doc, "state")
	if len(profiles) == 0 {
		delete(doc, "profiles")
	}

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
	return nil
}

// migrateSecretsToStore moves an existing login's secrets out of its session
// file and into the secure store, so upgrading does not cost a re-login. It
// runs on every command, like every other step here, and is a no-op once done.
//
// Only the session for the server this invocation resolved: a user with logins
// to prod and staging migrates each the first time they use it, rather than
// paying N keychain writes on a command that touches one of them.
//
// Fail-closed, the same way attachToSession is: the store is written before the
// file is rewritten, and a store that will not take the blob leaves the file
// byte-identical, with the secrets' only copy still in it.
func migrateSecretsToStore() error {
	store, err := resolveStore()
	if err != nil {
		// ORQ_CREDENTIAL_STORE=keychain with no keychain. The user asked for a
		// hard error over a downgrade, and every save would fail anyway.
		return err
	}
	if _, inline := store.(fileStore); inline {
		return nil // opted out, or no store on this platform: nothing to do
	}
	s, err := ReadSession()
	if err != nil || s == nil || s.SecretStore != "" {
		// Absent, too broken to rewrite safely, or already migrated. A broken
		// session is doctor's to report, not migration's to act on — and this
		// runs before every command, so failing here would take down the
		// commands someone with a broken ~/.orq reaches for.
		return nil
	}
	blob, err := json.Marshal(s.SessionSecrets)
	if err != nil {
		return err
	}
	if err := store.Set(secretAccount(SessionHost(s.APIBaseURL)), string(blob)); err != nil {
		// Warn, but do not fail the command: a locked keyring is not a reason
		// for `orq version` to stop working, and the session file still holds
		// the secrets, so the login is untouched.
		warnFallbackOnce(err)
		return nil
	}
	// SaveSession re-resolves the store and re-writes the item, which is one
	// redundant Set — and the reason the file's Version and SecretStore are
	// never set here: only the write that lands owns them.
	if err := SaveSession(s); err != nil {
		return err
	}
	fmt.Fprintf(bartolocli.Stderr, "moved the credentials for %s into %s.\n", SessionHost(s.APIBaseURL), store.Name())
	return nil
}

// attachToSession writes gateway fields onto the session for the host a
// profile named: its own server, else the session that used to carry its
// name, else the hosted default. No session there means nothing to attach the
// key to, and it is dropped — a gateway key without its login is dead weight.
func attachToSession(profileName string, fields map[string]string, renamed map[string]string) error {
	host := ""
	if server := strings.TrimSpace(fields["server"]); server != "" {
		host = SessionHost(server)
	} else if h, ok := renamed[profileName]; ok {
		host = h
	} else if fileExists(sessionPathFor(profileName)) {
		host = profileName
	} else {
		host = SessionHost(DefaultAPIBaseURL)
	}
	path := sessionPathFor(host)
	s, err := readSessionFileWithSecrets(path)
	if err != nil {
		// The session exists but is unreadable (truncated by a full disk, an
		// interrupted write, …): the caller is about to delete this field's
		// only other copy, so surfacing this as success would lose the key
		// for good. Fail closed instead.
		return fmt.Errorf("reading session for %s: %w", host, err)
	}
	if s == nil {
		// No session for this host — the documented drop: a gateway key with
		// nowhere to attach is dead weight, not a failure. Say so out loud:
		// logout never revoked the key server-side, so the id being dropped
		// here is the last local record of a credential that still works.
		reportDroppedGatewayKey(host, fields["gateway_key"], fields["gateway_key_id"],
			"there is no login left to attach it to")
		return nil
	}
	changed := false
	set := func(dst *string, v string) {
		if v != "" && *dst == "" {
			*dst = v
			changed = true
		}
	}
	set(&s.GatewayKey, fields["gateway_key"])
	set(&s.GatewayKeyID, fields["gateway_key_id"])
	set(&s.GatewayKeyExpiresAt, fields["gateway_key_expires_at"])
	set(&s.GatewayProject, fields["gateway_key_project"])
	set(&s.GatewayWorkspace, fields["workspace"])
	if !changed {
		return nil
	}
	return saveSessionTo(path, s)
}

// reportDroppedGatewayKey names a gateway key being discarded and how to revoke
// it. Anything quieter leaves a live key in the workspace with nothing on disk
// left to identify it by. why completes "dropped the gateway key for <host>:
// <why>."
func reportDroppedGatewayKey(host, key, id, why string) {
	id = strings.TrimSpace(id)
	if id == "" && strings.TrimSpace(key) == "" {
		return
	}
	if id == "" {
		fmt.Fprintf(bartolocli.Stderr,
			"dropped a gateway key for %s: %s, and no key id was recorded. "+
				"It still works; revoke it from the API keys page.\n", host, why)
		return
	}
	fmt.Fprintf(bartolocli.Stderr,
		"dropped the gateway key for %s: %s. "+
			"It still works; revoke it with: orq api-keys delete %s\n", host, why, id)
}

// reportUnreadableGatewayKey is the same warning for a session whose contents
// could not be read at all. It cannot name a key or say one existed, and the
// write that follows removes the file, so it reports the possibility rather
// than staying silent about a credential that may still be live.
func reportUnreadableGatewayKey(host, path string) {
	fmt.Fprintf(bartolocli.Stderr,
		"could not read the previous session for %s (%s). Any gateway key it held is now "+
			"unreferenced; it still works, so revoke it from the API keys page.\n", host, path)
}

// reportSupersededExportedKey warns that the account that just signed in is not
// the account whose key is still exported. ownExportedKey compares the exported
// ORQ_API_KEY against the session's GatewayKey, so a key belonging to the
// previous user now reads as foreign and outranks this login: commands would
// authenticate as them. Pre-existing and outside RES-1529 to fix, but silence
// here is the same failure this carry-over exists to prevent.
func reportSupersededExportedKey(previous *Session) {
	if previous == nil || strings.TrimSpace(previous.GatewayKey) == "" {
		return
	}
	who := ""
	if previous.User != nil {
		who = strings.TrimSpace(previous.User.Email)
	}
	if who == "" {
		who = "the previous user"
	}
	fmt.Fprintf(bartolocli.Stderr,
		"the API key orq setup exported for %s is still in this environment and takes "+
			"precedence over this login. Run 'orq setup' to replace it.\n", who)
}

// profileEntryFold looks a profile up the way bartolo does: sanitizeProfileName
// lowercases, so `profiles.Default` and a `state.default` beside it are one
// profile as far as every reader is concerned. Matching case-sensitively here
// leaves the keyless husk behind, and bartolo then fails every request on it.
func profileEntryFold(profiles map[string]any, name string) (string, map[string]any, bool) {
	for key, value := range profiles {
		if !strings.EqualFold(key, name) {
			continue
		}
		profile, ok := value.(map[string]any)
		return key, profile, ok
	}
	return "", nil, false
}

func credentialsNeedMigration() bool {
	if len(bartolocli.Creds.GetStringMap("state")) > 0 {
		return true
	}
	for _, value := range bartolocli.Creds.GetStringMap("profiles") {
		if profile, ok := value.(map[string]any); ok && ownedProfile(profile) {
			return true
		}
	}
	return false
}

func ownedProfile(profile map[string]any) bool {
	for _, f := range ownedFields {
		if stringField(profile, f) != "" {
			return true
		}
	}
	return false
}

func collectOwned(profile map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range ownedFields {
		out[f] = stringField(profile, f)
	}
	return out
}

func hasGatewayMetadata(fields map[string]string) bool {
	for _, field := range []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "gateway_key_project"} {
		if strings.TrimSpace(fields[field]) != "" {
			return true
		}
	}
	return false
}

func stringMap(m map[string]any) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = stringField(m, k)
	}
	return out
}

func stringField(m map[string]any, field string) string {
	v, _ := m[field].(string)
	return strings.TrimSpace(v)
}

// reconcileSelectedProfile clears a persisted `auth profile use` that names a
// profile which no longer exists; left behind, bartolo resolves it as in
// force and fails every request with "profile is not configured".
// profile-decided is kept, or bartolo re-adopts `default` on the next run.
//
// This checks against bartolocli.Creds's current profile set rather than
// against a list of names this run happened to remove: a profile can be gone
// for reasons outside this invocation (a previous run that wrote
// credentials.json but was interrupted before it got here), and the
// selection left dangling by that run is otherwise unrecoverable — every
// later run's credentialsNeedMigration is false, so migrateCredentials never
// runs again to notice.
func reconcileSelectedProfile(configDir string) error {
	selected := strings.TrimSpace(viper.GetString("profile-selected"))
	if selected == "" || bartolocli.Creds == nil {
		return nil
	}
	for name := range bartolocli.Creds.GetStringMap("profiles") {
		if strings.EqualFold(name, selected) {
			return nil // still there
		}
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

func readSessionFile(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// readSessionFileWithSecrets is readSessionFile plus the externalized secrets,
// for a caller that is about to write the session straight back. saveSessionTo
// re-stores whatever SessionSecrets holds, so handing it a session whose
// secrets were never loaded would replace the store item with an empty blob and
// log the user out. Anything short of a complete session is an error here, so
// the caller fails closed rather than saving a hollowed-out one.
//
// migrateSessionFiles deliberately keeps using the metadata-only read: it only
// renames files, and making a locked keychain print "not a readable session
// file" before every command would be a worse bug than the one this prevents.
func readSessionFileWithSecrets(path string) (*Session, error) {
	s, err := readSessionFile(path)
	if err != nil || s == nil {
		return s, err
	}
	found, err := loadExternalSecrets(s, sessionHostForPath(path))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("credentials for %s are no longer in %s", sessionHostForPath(path), s.SecretStore)
	}
	return s, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func newerThan(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && ai.ModTime().After(bi.ModTime())
}

func deprecatedName(path string) string { return path + ".deprecated" }

// uniquePath returns path if nothing is there, else the first path.1, path.2,
// … that is free. Every `.deprecated` rename goes through it rather than
// os.Rename directly, so a name already taken — by a file left over from an
// earlier interrupted run — is never silently clobbered. The legacy fold-in
// has uniqueJSONPath for the same reason, and the winner's promotion to the
// host name is guarded by the fileExists check at its call site instead.
func uniquePath(path string) string {
	if !fileExists(path) {
		return path
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s.%d", path, i)
		if !fileExists(candidate) {
			return candidate
		}
	}
}

// uniqueJSONPath is uniquePath for a name that must keep its .json suffix so
// it stays eligible for the session-file scan that follows the legacy
// fold-in, rather than falling out of migration entirely under a name like
// "session.json.1".
func uniqueJSONPath(path string) string {
	if !fileExists(path) {
		return path
	}
	base := strings.TrimSuffix(path, ".json")
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d.json", base, i)
		if !fileExists(candidate) {
			return candidate
		}
	}
}
