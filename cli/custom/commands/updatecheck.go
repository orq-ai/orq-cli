package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"orq/cli/custom/skills"
)

// The npm registry is the source of truth for "latest": every release is
// published there (npm/cli/package.json's five platform packages), and the tag
// tracks the same version line the binary reports, so the two can never
// disagree about what is current. Nothing about this machine is sent - the
// dist-tags endpoint is a plain GET of a public document.
const (
	npmPackage   = "@orq-ai/cli"
	distTagsURL  = "https://registry.npmjs.org/-/package/" + npmPackage + "/dist-tags"
	installerURL = "https://cli.orq.ai/install.sh"
	installerCmd = "curl -fsSL " + installerURL + " | sh"

	updateCheckTTL     = 24 * time.Hour
	updateCheckTimeout = 2 * time.Second

	// The notice is a nudge, not a policy: three sightings in a day is enough
	// for anyone who is going to act on it, and a fourth is just noise on
	// someone's every command.
	noticeWindow  = 24 * time.Hour
	noticesPerDay = 3
)

// installMethod is how this binary arrived, which decides how it can be
// replaced. Shared by the notice and by `orq update`, so the two can never
// disagree about what the user should run.
type installMethod string

const (
	methodNPM       installMethod = "npm"
	methodInstaller installMethod = "installer"
	methodUnknown   installMethod = "unknown"
)

// updateCacheFile records the last check so the registry is asked at most once a
// day and a stale "update available" cannot outlive the update that fixed it:
// CurrentAtCheck invalidates the entry as soon as the running version changes,
// through whichever install method the user updated.
type updateCacheFile struct {
	Version        int       `json:"version"`
	CheckedAt      time.Time `json:"checked_at"`
	Latest         string    `json:"latest"`
	CurrentAtCheck string    `json:"current_at_check"`
	// ShownAt are the times the notice was actually printed, newest last and
	// capped at noticesPerDay: the display budget, which is separate from the
	// fetch TTL above. Lives in the same file so both halves are invalidated
	// together when the running version changes.
	ShownAt []time.Time `json:"shown_at,omitempty"`
}

// Overridable for tests: the real endpoint and the real home directory are not
// reachable from a unit test.
var (
	updateDistTagsURL = distTagsURL
	updateHomeDir     = os.UserHomeDir
	osExecutable      = os.Executable
)

// MaybePrintUpdateNotice prints the "newer version available" notice on stderr
// BEFORE the command runs, from the cached answer only. Before, because a line
// after several screens of output is a line nobody reads; from cache only,
// because a registry round trip in front of every command is latency the user
// did not ask for. The cache is refreshed by RefreshUpdateCache after the
// command, so a cold cache costs one silent run, not a slow one.
//
// At most noticesPerDay printings per noticeWindow: the notice used to print on
// every human run, which is how a helpful nudge became wallpaper.
func MaybePrintUpdateNotice(cmd *cobra.Command) {
	if updateCheckDisabled(cmd) {
		return
	}
	current := currentVersion(cmd)
	if _, ok := parseSemver(current); !ok {
		return // dev build, or a version we cannot reason about
	}
	cache := readUpdateCache(current)
	if cache == nil || !updateAvailable(current, cache.Latest) {
		return
	}
	shown := recentNotices(cache.ShownAt)
	if len(shown) >= noticesPerDay {
		return
	}
	printUpdateNotice(current, cache.Latest)
	cache.ShownAt = append(shown, time.Now().UTC())
	writeUpdateCache(cache)
}

// RefreshUpdateCache asks the registry for the latest version when the cached
// answer has expired, and only writes it: whatever it learns is for the next
// run to print. Runs after the command so the round trip never delays it, and
// every failure path is silent - an update check must never turn a working
// command into a failure, nor delay it beyond updateCheckTimeout.
func RefreshUpdateCache(cmd *cobra.Command) {
	if updateCheckDisabled(cmd) {
		return
	}
	current := currentVersion(cmd)
	if _, ok := parseSemver(current); !ok {
		return
	}
	if readUpdateCache(current) != nil {
		return // still fresh
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	latest, err := fetchLatestVersion(ctx, current)
	if err != nil {
		return
	}
	storeUpdateCheck(current, latest)
}

// storeUpdateCheck records a fresh registry answer. The display budget survives
// a re-check of the same version - otherwise a daily refresh would hand out
// three fresh notices every day. A version change drops it, so the first run
// after an update is heard.
func storeUpdateCheck(current, latest string) {
	var shown []time.Time
	if prev, ok := loadUpdateCache(); ok && prev.CurrentAtCheck == current {
		shown = recentNotices(prev.ShownAt)
	}
	writeUpdateCache(&updateCacheFile{
		Version:        1,
		CheckedAt:      time.Now().UTC(),
		Latest:         latest,
		CurrentAtCheck: current,
		ShownAt:        shown,
	})
}

// recentNotices drops printings that have aged out of the window, so the budget
// is a rolling one rather than a counter that has to be reset by something.
func recentNotices(shown []time.Time) []time.Time {
	out := make([]time.Time, 0, len(shown))
	for _, t := range shown {
		if time.Since(t) < noticeWindow {
			out = append(out, t)
		}
	}
	return out
}

func printUpdateNotice(current, latest string) {
	if !updateAvailable(current, latest) {
		return
	}
	fmt.Fprintf(bartolocli.Stderr, "\nUpdate available: %s -> %s\n  Run: %s\n", current, latest, updateHint())
}

// updateCheckDisabled reports whether this run must stay silent: an explicit
// opt-out, CI, a machine-readable format, or anything that is not a person at a
// terminal. The notice is for humans; it must never land in captured output.
func updateCheckDisabled(cmd *cobra.Command) bool {
	if os.Getenv("ORQ_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return true
	}
	if cmd.Name() == "update" {
		// `orq update` has just said everything the notice would, from fresher
		// data, and after a successful one this process still reports the
		// version it replaced.
		return true
	}
	return !wantsHumanView(cmd)
}

// updateHint is the command to print in the notice: `orq update` when this
// binary is on an install method that command can act on, and the raw installer
// one-liner otherwise, since telling someone to run a command that will refuse
// is worse than telling them nothing.
func updateHint() string {
	if method, _ := detectInstallMethod(); method == methodUnknown {
		return installerCmd
	}
	return "orq update"
}

// detectInstallMethod classifies the running binary by where it lives, and returns
// the resolved path so an error can name it. npm's launcher shim execs the
// platform binary out of node_modules, so that path component is the npm
// marker; the installer writes into $ORQ_CLI_INSTALL_DIR (default ~/.orq/bin).
// Anything else - a hand-copied binary, a `go build` output, a distro package -
// is unknown, and updating it is not ours to do.
// ponytail: two install methods, no registry until a third exists.
//
// Variable so tests can pin an install method: a test binary lives in neither place.
var detectInstallMethod = func() (installMethod, string) {
	path, err := osExecutable()
	if err != nil {
		return methodUnknown, ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	sep := string(os.PathSeparator)
	if strings.Contains(path, sep+"node_modules"+sep) {
		return methodNPM, path
	}
	if dir := installerDir(); dir != "" && skills.SameDir(filepath.Dir(path), dir) {
		return methodInstaller, path
	}
	return methodUnknown, path
}

// installerDir mirrors install.sh's own resolution order, so a custom
// --install-dir install is still recognised as ours.
func installerDir() string {
	if dir := strings.TrimSpace(os.Getenv("ORQ_CLI_INSTALL_DIR")); dir != "" {
		return dir
	}
	home, err := updateHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orq", "bin")
}

func currentVersion(cmd *cobra.Command) string {
	return strings.TrimSpace(cmd.Root().Version)
}

func updateCachePath() (string, error) {
	home, err := updateHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orq", "update-check.json"), nil
}

// loadUpdateCache returns the cache file as written, without judging whether it
// is still usable. Missing or corrupt file is a cold cache, never an error.
func loadUpdateCache() (*updateCacheFile, bool) {
	path, err := updateCachePath()
	if err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cache updateCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil || cache.Version != 1 {
		return nil, false
	}
	return &cache, true
}

// readUpdateCache returns the cached check when it is still valid for the
// running version, or nil when the caller should check again.
func readUpdateCache(current string) *updateCacheFile {
	cache, ok := loadUpdateCache()
	if !ok {
		return nil
	}
	if cache.CurrentAtCheck != current || time.Since(cache.CheckedAt) >= updateCheckTTL {
		return nil
	}
	return cache
}

func writeUpdateCache(cache *updateCacheFile) {
	path, err := updateCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*.json")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

func fetchLatestVersion(ctx context.Context, current string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateDistTagsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dist-tags: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	var tags map[string]string
	if err := json.Unmarshal(body, &tags); err != nil {
		return "", err
	}
	tag := "latest"
	if v, ok := parseSemver(current); ok && v.isPre {
		tag = "rc"
	}
	latest, ok := tags[tag]
	if !ok || latest == "" {
		return "", fmt.Errorf("dist-tags: no %q tag", tag)
	}
	return latest, nil
}

// updateAvailable is deliberately conservative: anything unparsable, and a
// local build ahead of the published one, count as up to date. A wrong nag is
// worse than a missed one.
func updateAvailable(current, latest string) bool {
	c, okc := parseSemver(current)
	l, okl := parseSemver(latest)
	if !okc || !okl {
		return false
	}
	return compareSemver(l, c) > 0
}

type semver struct {
	nums  [3]int
	pre   int
	isPre bool
}

// parseSemver handles the shapes this project ships: 4.13.22 and
// 4.14.0-rc.48. Anything else - "dev", a date, a git describe - is rejected,
// which is what keeps dev builds silent.
func parseSemver(v string) (semver, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return semver{}, false
	}
	base, pre, hasPre := strings.Cut(v, "-")
	fields := strings.Split(base, ".")
	if len(fields) != 3 {
		return semver{}, false
	}
	var out semver
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out.nums[i] = n
	}
	if hasPre {
		out.isPre = true
		// "rc.48" -> 48; an unnumbered prerelease sorts as 0, which only ever
		// makes us quieter.
		if _, n, ok := strings.Cut(pre, "."); ok {
			if parsed, err := strconv.Atoi(n); err == nil {
				out.pre = parsed
			}
		}
	}
	return out, true
}

// compareSemver orders by numeric fields, then puts a release ahead of a
// prerelease of the same numbers (4.14.0 > 4.14.0-rc.48), then by prerelease
// number. Numeric, not string: 0.10.0 must beat 0.9.0.
func compareSemver(a, b semver) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			return sign(a.nums[i] - b.nums[i])
		}
	}
	if a.isPre != b.isPre {
		if a.isPre {
			return -1
		}
		return 1
	}
	return sign(a.pre - b.pre)
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}
