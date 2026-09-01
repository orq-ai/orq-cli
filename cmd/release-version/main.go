// Command release-version resolves the CLI version used by the release
// workflows. Keeping the arithmetic here makes it runnable and testable
// without a live GitHub Actions shell.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type input struct {
	Version     string
	API         string
	ReleasedAPI string
	Commits     string
	Tags        string
	Channel     string
}

type result struct {
	Version     string
	Tag         string
	PreviousTag string
	Bump        string
	Prerelease  bool
}

const preIdent = `(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	rcPattern      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$`)
	// The prerelease part follows semver strictly - no leading-zero numeric
	// identifiers - so a malformed app_version is caught here rather than in
	// release-build.sh, after the tag and the release are already immutable.
	apiPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-` + preIdent + `(\.` + preIdent + `)*)?$`)
	majorCommit    = regexp.MustCompile(`^[a-z]+(?:\([^)]*\))?!:`)
	featureCommit  = regexp.MustCompile(`^feat(?:\([^)]*\))?:`)
	breakingFooter = regexp.MustCompile(`^BREAKING[ -]CHANGE:`)
)

func main() {
	var in input
	var tagsFile, commitsFile, verifyVersion string
	flag.StringVar(&in.Version, "version", "", "bare VERSION semver")
	flag.StringVar(&in.API, "api-version", "", "current orq API app_version")
	flag.StringVar(&in.ReleasedAPI, "released-api-version", "", "orq API app_version at the last stable tag")
	flag.StringVar(&in.Channel, "channel", "stable", "release channel: stable or rc")
	flag.StringVar(&tagsFile, "tags-file", "", "file containing one local tag per line")
	flag.StringVar(&commitsFile, "commits-file", "", "file of NUL-separated commit records, subject on the first line")
	flag.StringVar(&verifyVersion, "verify", "", "verify a resolved release version")
	flag.Parse()

	var err error
	if tagsFile != "" {
		in.Tags, err = readFile(tagsFile)
		if err != nil {
			fatal(err)
		}
	}
	if commitsFile != "" {
		in.Commits, err = readFile(commitsFile)
		if err != nil {
			fatal(err)
		}
	}

	var out result
	if verifyVersion != "" {
		out, err = verify(in, verifyVersion)
	} else {
		out, err = resolve(in)
	}
	if err != nil {
		fatal(err)
	}
	fmt.Printf("version=%s\ntag=%s\nprevious_tag=%s\nbump=%s\nprerelease=%t\n", out.Version, out.Tag, out.PreviousTag, out.Bump, out.Prerelease)
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-version:", err)
	os.Exit(1)
}

func resolve(in input) (result, error) {
	if !versionPattern.MatchString(in.Version) {
		return result{}, fmt.Errorf("VERSION must be a bare major.minor.patch semver, got %q", in.Version)
	}
	if strings.TrimSpace(in.API) == "" || in.API == "null" {
		return result{}, errors.New("current app_version is missing")
	}
	if !apiPattern.MatchString(in.API) {
		return result{}, fmt.Errorf("current app_version is not semver: %q", in.API)
	}
	if strings.TrimSpace(in.ReleasedAPI) != "" && in.ReleasedAPI != "null" && !apiPattern.MatchString(in.ReleasedAPI) {
		return result{}, fmt.Errorf("released app_version is not semver: %q", in.ReleasedAPI)
	}
	if in.Channel != "stable" && in.Channel != "rc" {
		return result{}, fmt.Errorf("unknown release channel %q", in.Channel)
	}

	bump := versionBump(in.API, in.ReleasedAPI)
	commitBump := commitVersionBump(in.Commits)
	if rank(commitBump) > rank(bump) {
		bump = commitBump
	}
	tags := tagSet(in.Tags)

	if in.Channel == "rc" {
		// An rc previews the next stable release, so its base is that release's
		// number - not a further bump on top of it, which would name a version
		// the stable line never cuts.
		rcBase := stableVersionTarget(in.Version, bump, tags)
		if err := checkFloor(rcBase, tags); err != nil {
			return result{}, err
		}
		major, minor, patch := parseVersionFields(rcBase)
		prefix := fmt.Sprintf("%d.%d.%d-rc.", major, minor, patch)
		n := 1
		for tags[fmt.Sprintf("v%s%d", prefix, n)] {
			n++
		}
		previous := ""
		if n > 1 {
			previous = fmt.Sprintf("v%s%d", prefix, n-1)
		} else if tags["v"+in.Version] {
			previous = "v" + in.Version
		}
		version := fmt.Sprintf("%s%d", prefix, n)
		return result{Version: version, Tag: "v" + version, PreviousTag: previous, Bump: bump, Prerelease: true}, nil
	}

	version := stableVersionTarget(in.Version, bump, tags)
	major, minor, patch := parseVersionFields(version)
	version = fmt.Sprintf("%d.%d.%d", major, minor, patch)
	if err := checkFloor(version, tags); err != nil {
		return result{}, err
	}
	return result{Version: version, Tag: "v" + version, PreviousTag: "v" + in.Version, Bump: bump}, nil
}

// checkFloor is the floor rule from CHANGELOG.md#versioning, in one place for
// every caller.
func checkFloor(version string, tags map[string]bool) error {
	highest, ok := highestStable(tags)
	if !ok || higher(version, highest) {
		return nil
	}
	return fmt.Errorf("resolved %s does not sort above the highest released %s: check VERSION", version, highest)
}

func stableVersionTarget(version, bump string, tags map[string]bool) string {
	base := version
	if tags["v"+base] {
		base = applyBump(base, bump)
	}
	major, minor, patch := parseVersionFields(base)
	for tags[fmt.Sprintf("v%d.%d.%d", major, minor, patch)] {
		patch++
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch)
}

func verify(in input, version string) (result, error) {
	switch in.Channel {
	case "stable":
		if !versionPattern.MatchString(version) {
			return result{}, fmt.Errorf("version must be a bare major.minor.patch semver, got %q", version)
		}
	case "rc":
		if !rcPattern.MatchString(version) {
			return result{}, fmt.Errorf("rc version must have the form major.minor.patch-rc.N, got %q", version)
		}
	default:
		return result{}, fmt.Errorf("unknown release channel %q", in.Channel)
	}

	tags := tagSet(in.Tags)
	tag := "v" + version
	if tags[tag] {
		return result{}, fmt.Errorf("tag %s is already taken", tag)
	}
	// An rc previews the release named by its base, so the base is what has to
	// clear the floor: v5.0.0-rc.1 published while v5.1.0 is out would put the
	// npm `rc` dist-tag below `latest`.
	base := version
	if i := strings.Index(base, "-rc."); i >= 0 {
		base = base[:i]
	}
	if err := checkFloor(base, tags); err != nil {
		return result{}, fmt.Errorf("%s: %w", version, err)
	}
	return result{Version: version, Tag: tag, Prerelease: in.Channel == "rc"}, nil
}

// highestStable is the largest x.y.z tag on this line. Pre-release tags are
// excluded: v5.1.0-rc.1 sorts below the v5.1.0 it previews, so counting it
// would make the release it exists for look like a downgrade.
func highestStable(tags map[string]bool) (string, bool) {
	best := ""
	for tag := range tags {
		version := strings.TrimPrefix(tag, "v")
		if !versionPattern.MatchString(version) {
			continue
		}
		if best == "" || higher(version, best) {
			best = version
		}
	}
	return best, best != ""
}

func higher(a, b string) bool {
	aMajor, aMinor, aPatch := parseVersionFields(a)
	bMajor, bMinor, bPatch := parseVersionFields(b)
	switch {
	case aMajor != bMajor:
		return aMajor > bMajor
	case aMinor != bMinor:
		return aMinor > bMinor
	default:
		return aPatch > bPatch
	}
}

func readLines(body string) []string {
	return strings.Split(body, "\n")
}

func tagSet(body string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range readLines(body) {
		if tag := strings.TrimSpace(line); tag != "" {
			set[tag] = true
		}
	}
	return set
}

func versionBump(current, released string) string {
	if strings.TrimSpace(released) == "" || released == "null" || current == released {
		return "patch"
	}
	curMajor, curMinor, _ := parseVersionFields(current)
	relMajor, relMinor, _ := parseVersionFields(released)
	if curMajor != relMajor {
		return "major"
	}
	if curMinor != relMinor {
		return "minor"
	}
	return "patch"
}

func parseVersionFields(version string) (int, int, int) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(strings.SplitN(parts[2], "-", 2)[0])
	return major, minor, patch
}

// commitVersionBump reads NUL-separated commit records, each one a subject line
// followed by its body. The `type!:` marker is honoured on the subject only, and
// the body only through a footer: a wrapped sentence, or a PR description
// quoting the convention, must not be able to cut a major nobody can walk back.
func commitVersionBump(commits string) string {
	bump := "patch"
	for _, record := range strings.Split(commits, "\x00") {
		lines := readLines(strings.TrimLeft(record, "\n"))
		if len(lines) == 0 {
			continue
		}
		subject := lines[0]
		if majorCommit.MatchString(subject) {
			return "major"
		}
		for _, line := range lines[1:] {
			if breakingFooter.MatchString(line) {
				return "major"
			}
		}
		if featureCommit.MatchString(subject) {
			bump = "minor"
		}
	}
	return bump
}

func rank(bump string) int {
	switch bump {
	case "major":
		return 3
	case "minor":
		return 2
	default:
		return 1
	}
}

func applyBump(version, bump string) string {
	major, minor, patch := parseVersionFields(version)
	switch bump {
	case "major":
		return fmt.Sprintf("%d.0.0", major+1)
	case "minor":
		return fmt.Sprintf("%d.%d.0", major, minor+1)
	default:
		return fmt.Sprintf("%d.%d.%d", major, minor, patch+1)
	}
}
