// Command release-version resolves the CLI version used by the release
// workflows. Keeping the arithmetic here makes it runnable and testable
// without a live GitHub Actions shell.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
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
)

func main() {
	var in input
	var tagsFile, commitsFile string
	flag.StringVar(&in.Version, "version", "", "bare VERSION semver")
	flag.StringVar(&in.API, "api-version", "", "current orq API app_version")
	flag.StringVar(&in.ReleasedAPI, "released-api-version", "", "orq API app_version at the last stable tag")
	flag.StringVar(&in.Channel, "channel", "stable", "release channel: stable or rc")
	flag.StringVar(&tagsFile, "tags-file", "", "file containing one local tag per line")
	flag.StringVar(&commitsFile, "commits-file", "", "file containing commit subjects and bodies")
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
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
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
		major, minor, _ := fields(in.Version)
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
	major, minor, patch := fields(base)
	for tags[fmt.Sprintf("v%d.%d.%d", major, minor, patch)] {
		patch++
	}
	version := fmt.Sprintf("%d.%d.%d", major, minor, patch)
	return result{Version: version, Tag: "v" + version, PreviousTag: "v" + in.Version, Bump: bump}, nil
}

func readLines(body string) []string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
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

func fields(version string) (int, int, int) {
	major, minor, patch := parseVersionFields(version)
	return major, minor, patch
}

func commitVersionBump(commits string) string {
	bump := "patch"
	for _, line := range readLines(commits) {
		if majorCommit.MatchString(line) || strings.HasPrefix(line, "BREAKING CHANGE:") {
			return "major"
		}
		if featureCommit.MatchString(line) {
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
	major, minor, patch := fields(version)
	switch bump {
	case "major":
		return fmt.Sprintf("%d.0.0", major+1)
	case "minor":
		return fmt.Sprintf("%d.%d.0", major, minor+1)
	default:
		return fmt.Sprintf("%d.%d.%d", major, minor, patch+1)
	}
}
