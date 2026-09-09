package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func TestUpdateAvailable(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"4.13.18", "4.13.22", true},
		{"4.13.22", "4.13.22", false},
		{"4.13.22", "4.13.18", false}, // local ahead of published: no downgrade nag
		{"0.9.0", "0.10.0", true},     // numeric compare, not string order
		{"0.10.0", "0.9.0", false},
		{"v4.13.18", "v4.13.22", true},
		{"4.14.0-rc.47", "4.14.0-rc.48", true},
		{"4.14.0-rc.48", "4.14.0-rc.48", false},
		{"4.14.0-rc.48", "4.14.0", true},  // release beats its own prerelease
		{"4.14.0", "4.14.0-rc.48", false}, // and never the other way round
		{"dev", "4.13.22", false},
		{"4.13.22", "garbage", false},
		{"", "4.13.22", false},
	}
	for _, c := range cases {
		if got := updateAvailable(c.current, c.latest); got != c.want {
			t.Errorf("updateAvailable(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// updateTestEnv points the check at a fake registry and a scratch HOME, and
// makes the process look like a person at a terminal.
func updateTestEnv(t *testing.T, tags map[string]string) (stderr *bytes.Buffer, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(tags)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	origURL, origHome, origHuman, origErr := updateDistTagsURL, updateHomeDir, humanOutput, bartolocli.Stderr
	t.Cleanup(func() {
		updateDistTagsURL, updateHomeDir, humanOutput, bartolocli.Stderr = origURL, origHome, origHuman, origErr
	})
	updateDistTagsURL = srv.URL
	updateHomeDir = func() (string, error) { return home, nil }
	humanOutput = func() bool { return true }
	stderr = &bytes.Buffer{}
	bartolocli.Stderr = stderr

	t.Setenv("ORQ_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	return stderr, hits
}

func updateTestCmd(version string) *cobra.Command {
	root := &cobra.Command{Use: "orq", Version: version}
	return root
}

// runUpdateCheck is one whole invocation in the order run.go performs it: the
// notice before the command, the cache refresh after it.
func runUpdateCheck(cmd *cobra.Command) {
	MaybePrintUpdateNotice(cmd)
	RefreshUpdateCache(cmd)
}

func TestMaybePrintUpdateNoticeIsCacheOnlyAndCappedPerDay(t *testing.T) {
	stderr, hits := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	cmd := updateTestCmd("4.13.18")

	// A cold cache costs a silent run, not a slow one: nothing is printed until
	// the answer fetched after the command is available to the next run.
	runUpdateCheck(cmd)
	if got := stderr.String(); got != "" {
		t.Fatalf("first run printed %q, want silence on a cold cache", got)
	}

	for i := 1; i <= noticesPerDay; i++ {
		stderr.Reset()
		runUpdateCheck(cmd)
		if got := stderr.String(); !strings.Contains(got, "4.13.18 -> 4.13.22") {
			t.Fatalf("run %d printed %q, want the notice served from cache", i, got)
		}
	}

	stderr.Reset()
	runUpdateCheck(cmd)
	if got := stderr.String(); got != "" {
		t.Errorf("run %d printed %q, want silence after %d notices in the window", noticesPerDay+2, got, noticesPerDay)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("registry hits = %d, want 1 (cache must serve the later runs)", got)
	}
}

// The budget is per 24h, not per cache entry: a daily refresh of the same
// version must not hand out three more notices.
func TestUpdateNoticeBudgetSurvivesCacheRefresh(t *testing.T) {
	stderr, _ := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	cmd := updateTestCmd("4.13.18")
	runUpdateCheck(cmd)
	for i := 0; i < noticesPerDay; i++ {
		runUpdateCheck(cmd)
	}

	// Age the check past the TTL, leaving the printings inside their window.
	cache, ok := loadUpdateCache()
	if !ok {
		t.Fatal("no cache written")
	}
	cache.CheckedAt = time.Now().Add(-25 * time.Hour).UTC()
	writeUpdateCache(cache)

	stderr.Reset()
	runUpdateCheck(cmd) // refetches, prints nothing (stale cache)
	runUpdateCheck(cmd)
	if got := stderr.String(); got != "" {
		t.Errorf("printed %q after a re-check, want the daily budget to survive it", got)
	}
}

// A printing older than the window no longer counts against the budget.
func TestUpdateNoticeBudgetRollsOver(t *testing.T) {
	stderr, _ := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	cmd := updateTestCmd("4.13.18")
	runUpdateCheck(cmd)
	for i := 0; i < noticesPerDay; i++ {
		runUpdateCheck(cmd)
	}

	cache, ok := loadUpdateCache()
	if !ok {
		t.Fatal("no cache written")
	}
	for i := range cache.ShownAt {
		cache.ShownAt[i] = time.Now().Add(-25 * time.Hour).UTC()
	}
	writeUpdateCache(cache)

	stderr.Reset()
	runUpdateCheck(cmd)
	if got := stderr.String(); !strings.Contains(got, "4.13.18 -> 4.13.22") {
		t.Errorf("printed %q, want the notice again once the old ones aged out", got)
	}
}

func TestMaybePrintUpdateNoticeCachedUpToDateStaysSilent(t *testing.T) {
	stderr, hits := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	cmd := updateTestCmd("4.13.22")

	runUpdateCheck(cmd)
	stderr.Reset()
	runUpdateCheck(cmd)
	if got := stderr.String(); got != "" {
		t.Errorf("cached run printed %q for an up-to-date binary, want silence", got)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("registry hits = %d, want 1", got)
	}
}

func TestMaybePrintUpdateNoticeSilentWhenUpToDate(t *testing.T) {
	stderr, _ := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	runUpdateCheck(updateTestCmd("4.13.22"))
	if got := stderr.String(); got != "" {
		t.Errorf("printed %q for an up-to-date binary, want silence", got)
	}
}

func TestMaybePrintUpdateNoticeSuppressed(t *testing.T) {
	cases := []struct {
		name  string
		apply func(t *testing.T)
	}{
		{"opt-out", func(t *testing.T) { t.Setenv("ORQ_NO_UPDATE_CHECK", "1") }},
		{"ci", func(t *testing.T) { t.Setenv("CI", "true") }},
		{"non-tty", func(t *testing.T) { humanOutput = func() bool { return false } }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stderr, hits := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
			c.apply(t)
			runUpdateCheck(updateTestCmd("4.13.18"))
			if got := stderr.String(); got != "" {
				t.Errorf("printed %q, want silence", got)
			}
			if got := hits.Load(); got != 0 {
				t.Errorf("registry hits = %d, want 0: a suppressed check must not phone home", got)
			}
		})
	}
}

func TestMaybePrintUpdateNoticeDevBuildStaysSilent(t *testing.T) {
	stderr, hits := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	runUpdateCheck(updateTestCmd("dev"))
	if got := stderr.String(); got != "" {
		t.Errorf("printed %q for a dev build, want silence", got)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("registry hits = %d, want 0 for a dev build", got)
	}
}

func TestMaybePrintUpdateNoticeRCFollowsRCTag(t *testing.T) {
	stderr, _ := updateTestEnv(t, map[string]string{"latest": "4.13.22", "rc": "4.14.0-rc.48"})
	runUpdateCheck(updateTestCmd("4.14.0-rc.47"))
	runUpdateCheck(updateTestCmd("4.14.0-rc.47"))
	if got := stderr.String(); !strings.Contains(got, "4.14.0-rc.47 -> 4.14.0-rc.48") {
		t.Errorf("rc build compared against the wrong tag: %q", got)
	}
}

func TestMaybePrintUpdateNoticeSurvivesRegistryFailure(t *testing.T) {
	stderr, _ := updateTestEnv(t, nil)
	updateDistTagsURL = "http://127.0.0.1:1/dist-tags" // nothing listens here
	runUpdateCheck(updateTestCmd("4.13.18"))
	if got := stderr.String(); got != "" {
		t.Errorf("printed %q on a failed check, want silence", got)
	}
}

func TestUpdateCacheInvalidatedByVersionChange(t *testing.T) {
	stderr, hits := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	runUpdateCheck(updateTestCmd("4.13.18"))
	stderr.Reset()

	// The user updated: the cache entry for the old version must not be reused,
	// or a fixed "update available" would survive its own fix.
	runUpdateCheck(updateTestCmd("4.13.22"))
	if got := stderr.String(); got != "" {
		t.Errorf("printed %q after updating, want silence", got)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("registry hits = %d, want 2 (a version change invalidates the cache)", got)
	}
}

func TestReadUpdateCache(t *testing.T) {
	home := t.TempDir()
	orig := updateHomeDir
	t.Cleanup(func() { updateHomeDir = orig })
	updateHomeDir = func() (string, error) { return home, nil }
	path := filepath.Join(home, ".orq", "update-check.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	write := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(t, "{not json")
	if readUpdateCache("4.13.18") != nil {
		t.Error("corrupt cache must read as cold, not as a hit")
	}

	stale, _ := json.Marshal(updateCacheFile{Version: 1, CheckedAt: time.Now().Add(-25 * time.Hour), Latest: "4.13.22", CurrentAtCheck: "4.13.18"})
	write(t, string(stale))
	if readUpdateCache("4.13.18") != nil {
		t.Error("cache older than the TTL must read as cold")
	}

	fresh, _ := json.Marshal(updateCacheFile{Version: 1, CheckedAt: time.Now(), Latest: "4.13.22", CurrentAtCheck: "4.13.18"})
	write(t, string(fresh))
	if readUpdateCache("4.13.18") == nil {
		t.Error("fresh cache must be a hit")
	}
}

func TestWriteUpdateCacheIsPrivate(t *testing.T) {
	home := t.TempDir()
	orig := updateHomeDir
	t.Cleanup(func() { updateHomeDir = orig })
	updateHomeDir = func() (string, error) { return home, nil }

	storeUpdateCheck("4.13.18", "4.13.22")
	info, err := os.Stat(filepath.Join(home, ".orq", "update-check.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".orq"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("atomic write left temp files behind: %v", entries)
	}
}
