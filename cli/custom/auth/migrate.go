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
var ownedFields = []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "workspace"}

// MigrateLayout brings an older ~/.orq up to the current layout in one pass:
// session files are named by host, and everything this CLI records about a
// login lives in that login's file rather than in bartolo's credentials.json.
// It returns an error rather than warning: a migration that did not run leaves
// a keyless profile bartolo fails every request on.
//
// Idempotent and cheap once done — both halves check before touching disk.
func MigrateLayout(configDir string) error {
	renamed, err := migrateSessionFiles()
	if err != nil {
		return err
	}
	return migrateCredentials(configDir, renamed)
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
		if err := os.Rename(legacy, filepath.Join(dir, "session.json")); err != nil {
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
		if err != nil || s == nil || s.APIBaseURL == "" {
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
			loser := deprecatedName(p)
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
	var removed []string

	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok || !ownedProfile(profile) {
			continue
		}
		fields := collectOwned(profile)
		for _, f := range ownedFields {
			delete(profile, f)
		}
		if stringField(profile, "api_key") == "" {
			// A session login's profile: its server travels with the fields,
			// and the entry itself has no reason to exist.
			fields["server"] = stringField(profile, "server")
			delete(profiles, name)
			removed = append(removed, name)
			if err := attachToSession(name, fields, renamed); err != nil {
				return err
			}
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
	return dropRemovedSelection(configDir, removed)
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
	s, err := readSessionFile(path)
	if err != nil || s == nil {
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
	set(&s.GatewayWorkspace, fields["workspace"])
	if !changed {
		return nil
	}
	return saveSessionTo(path, s)
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

// dropRemovedSelection clears a persisted `auth profile use` that names a
// profile this migration deleted; left behind, bartolo resolves it as in force
// and fails every request with "profile is not configured". profile-decided is
// kept, or bartolo re-adopts `default` on the next run.
func dropRemovedSelection(configDir string, removed []string) error {
	selected := strings.TrimSpace(viper.GetString("profile-selected"))
	if selected == "" {
		return nil
	}
	gone := false
	for _, name := range removed {
		if strings.EqualFold(name, selected) {
			gone = true
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
