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
)

// updateChannel is how this binary arrived, which decides how it can be
// replaced. Shared by the notice and by `orq update`, so the two can never
// disagree about what the user should run.
type updateChannel string

const (
	channelNPM       updateChannel = "npm"
	channelInstaller updateChannel = "installer"
	channelUnknown   updateChannel = "unknown"
)

// updateCacheFile records the last check so the notice appears at most once a
// day and a stale "update available" cannot outlive the update that fixed it:
// CurrentAtCheck invalidates the entry as soon as the running version changes,
// through whichever channel the user updated.
type updateCacheFile struct {
	Version        int       `json:"version"`
	CheckedAt      time.Time `json:"checked_at"`
	Latest         string    `json:"latest"`
	CurrentAtCheck string    `json:"current_at_check"`
}

// Overridable for tests: the real endpoint and the real home directory are not
// reachable from a unit test.
var (
	updateDistTagsURL = distTagsURL
	updateHomeDir     = os.UserHomeDir
	osExecutable      = os.Executable
)

// MaybePrintUpdateNotice prints a one-line "newer version available" notice on
// stderr, at most once per 24h, and only for a person watching a terminal.
// Every failure path is silent: an update check must never turn a working
// command into a failure, nor delay it beyond updateCheckTimeout.
func MaybePrintUpdateNotice(cmd *cobra.Command) {
	if updateCheckDisabled(cmd) {
		return
	}
	current := currentVersion(cmd)
	if _, ok := parseSemver(current); !ok {
		return // dev build, or a version we cannot reason about
	}
	if fresh := readUpdateCache(current); fresh != nil {
		return // checked within the TTL; the notice already had its shot
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	latest, err := fetchLatestVersion(ctx, current)
	if err != nil {
		return
	}
	writeUpdateCache(current, latest)
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
	return !wantsHumanView(cmd)
}

// updateHint is the command to print in the notice: `orq update` when this
// binary is on a channel that command can act on, and the raw installer
// one-liner otherwise, since telling someone to run a command that will refuse
// is worse than telling them nothing.
func updateHint() string {
	if channel, _ := detectChannel(); channel == channelUnknown {
		return installerCmd
	}
	return "orq update"
}

// detectChannel classifies the running binary by where it lives, and returns
// the resolved path so an error can name it. npm's launcher shim execs the
// platform binary out of node_modules, so that path component is the npm
// marker; the installer writes into $ORQ_CLI_INSTALL_DIR (default ~/.orq/bin).
// Anything else - a hand-copied binary, a `go build` output, a distro package -
// is unknown, and updating it is not ours to do.
// ponytail: two channels, no registry until a third exists.
//
// Variable so tests can pin a channel: a test binary lives in neither place.
var detectChannel = func() (updateChannel, string) {
	path, err := osExecutable()
	if err != nil {
		return channelUnknown, ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	sep := string(os.PathSeparator)
	if strings.Contains(path, sep+"node_modules"+sep) {
		return channelNPM, path
	}
	if dir := installerDir(); dir != "" && sameDir(filepath.Dir(path), dir) {
		return channelInstaller, path
	}
	return channelUnknown, path
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

// sameDir compares two directories through symlinks, so ~/.orq/bin still
// matches when part of the path is a link (a symlinked $HOME is the common one
// on macOS and in containers).
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// currentVersion is the version cobra was built with, which the release
// pipeline stamps; an unstamped build reports "dev".
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

// readUpdateCache returns the cached check when it is still valid for the
// running version, or nil when the caller should check again. Missing or
// corrupt file is a cold cache, never an error.
func readUpdateCache(current string) *updateCacheFile {
	path, err := updateCachePath()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cache updateCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil || cache.Version != 1 {
		return nil
	}
	if cache.CurrentAtCheck != current || time.Since(cache.CheckedAt) >= updateCheckTTL {
		return nil
	}
	return &cache
}

func writeUpdateCache(current, latest string) {
	path, err := updateCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(updateCacheFile{
		Version:        1,
		CheckedAt:      time.Now().UTC(),
		Latest:         latest,
		CurrentAtCheck: current,
	})
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

// fetchLatestVersion reads the dist-tag matching the running version's line, so
// an rc build is compared against rc and a stable build against stable - else
// every rc user would be told to "update" to an older stable release.
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
