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

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	apiPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	majorCommit    = regexp.MustCompile(`^[a-z]+(?:\([^)]*\))?!:`)
	featureCommit  = regexp.MustCompile(`^feat(?:\([^)]*\))?:`)
	breakingFooter = regexp.MustCompile(`^BREAKING[ -]CHANGE:`)
)

func main() {
	var in input
	var tagsFile, commitsFile string
	flag.StringVar(&in.Version, "version", "", "bare VERSION semver")
	flag.StringVar(&in.API, "api-version", "", "current orq API app_version")
	flag.StringVar(&in.ReleasedAPI, "released-api-version", "", "orq API app_version at the last stable tag")
	flag.StringVar(&in.Channel, "channel", "stable", "release channel: stable or rc")
	flag.StringVar(&tagsFile, "tags-file", "", "file containing one local tag per line")
	flag.StringVar(&commitsFile, "commits-file", "", "file of NUL-separated commit records, subject on the first line")
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

	out, err := resolve(in)
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
		major, minor, _ := parseVersionFields(in.Version)
		prefix := fmt.Sprintf("%d.%d.0-rc.", major, minor+1)
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

	base := in.Version
	if tags["v"+base] {
		base = applyBump(base, bump)
	}
	major, minor, patch := parseVersionFields(base)
	for tags[fmt.Sprintf("v%d.%d.%d", major, minor, patch)] {
		patch++
	}
	version := fmt.Sprintf("%d.%d.%d", major, minor, patch)
	// A stale or mis-merged VERSION resolves a number below what is already
	// published, and that number becomes npm `latest` and /releases/latest -
	// a downgrade `orq update` then refuses to walk anyone back out of. Fail
	// the release instead.
	if highest, ok := highestStable(tags); ok && !higher(version, highest) {
		return result{}, fmt.Errorf("resolved %s does not sort above the highest released %s: check VERSION", version, highest)
	}
	return result{Version: version, Tag: "v" + version, PreviousTag: "v" + in.Version, Bump: bump}, nil
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
