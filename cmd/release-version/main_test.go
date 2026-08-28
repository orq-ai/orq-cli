package main

import (
	"os"
	"strings"
	"testing"
)

func TestResolveStable(t *testing.T) {
	tests := []struct {
		name, version, api, released, commits, tags, wantVersion, wantBump string
	}{
		{"bootstrap", "5.0.0", "4.15.0", "", "", "v4.15.0\n", "5.0.0", "patch"},
		{"api minor", "5.0.0", "4.16.0", "4.15.0", "", "v5.0.0\n", "5.1.0", "minor"},
		{"api major", "5.0.0", "5.0.0", "4.15.0", "", "v5.0.0\n", "6.0.0", "major"},
		{"feature wins", "5.0.0", "4.15.1", "4.15.0", "feat(cli): add thing\n", "v5.0.0\n", "5.1.0", "minor"},
		{"free patch", "5.0.0", "4.15.0", "4.15.0", "", "v5.0.0\nv5.0.1\n", "5.0.2", "patch"},
		{"breaking footer", "5.0.0", "4.15.0", "4.15.0", "docs: note\nBREAKING CHANGE: note\n", "v5.0.0\n", "6.0.0", "major"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(input{Version: tt.version, API: tt.api, ReleasedAPI: tt.released, Commits: tt.commits, Tags: tt.tags, Channel: "stable"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.wantVersion || got.Bump != tt.wantBump || got.Prerelease {
				t.Fatalf("got %#v, want version=%s bump=%s stable", got, tt.wantVersion, tt.wantBump)
			}
		})
	}
}

func TestResolveRCUsesNextMinorAndFreeRCNumber(t *testing.T) {
	got, err := resolve(input{
		Version: "5.0.0", API: "4.15.0", ReleasedAPI: "4.15.0",
		Commits: "feat: ignored for rc identity\n", Tags: "v5.1.0-rc.1\nv5.1.0-rc.2\n", Channel: "rc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "5.1.0-rc.3" || got.PreviousTag != "v5.1.0-rc.2" || got.Bump != "minor" || !got.Prerelease {
		t.Fatalf("got %#v", got)
	}
}

func TestResolveRejectsInvalidInput(t *testing.T) {
	for _, in := range []input{
		{Version: "5.01.0", API: "4.15.0", Channel: "stable"},
		{Version: "5.0.0", API: "", Channel: "stable"},
		{Version: "5.0.0", API: "4.x", Channel: "stable"},
		{Version: "5.0.0", API: "4.15.0", Channel: "nightly"},
	} {
		if _, err := resolve(in); err == nil {
			t.Fatalf("resolve(%#v) succeeded", in)
		}
	}
}

// The real tag set this line started on top of, as the bootstrap cases see it.
const legacyTags = "v0.0.1\nv0.0.2\nv2.0.0\nv4.12.3\nv4.13.22\nv4.14.0-rc.55\n"

func TestResolveOverLegacyTags(t *testing.T) {
	tests := []struct{ name, tags, commits, api, released, wantVersion string }{
		{"first release publishes VERSION as written", legacyTags, "", "4.13.22", "", "5.0.0"},
		{"patch once tagged", legacyTags + "v5.0.0\n", "fix: thing\x00", "4.13.22", "4.13.22", "5.0.1"},
		{"minor once tagged", legacyTags + "v5.0.0\n", "feat: thing\x00", "4.13.22", "4.13.22", "5.1.0"},
		{"major once tagged", legacyTags + "v5.0.0\n", "feat!: thing\x00", "4.13.22", "4.13.22", "6.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(input{Version: "5.0.0", API: tt.api, ReleasedAPI: tt.released, Commits: tt.commits, Tags: tt.tags, Channel: "stable"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.wantVersion {
				t.Fatalf("got %q, want %q", got.Version, tt.wantVersion)
			}
		})
	}
}

// A wrapped sentence or a quoted convention in a commit body must not cut a
// major, and a real footer must still cut one.
func TestCommitBumpReadsTheSubjectAndFooterOnly(t *testing.T) {
	tests := []struct{ name, commits, want string }{
		{"subject breaking", "feat(auth)!: drop the flag\x00", "major"},
		{"footer breaking", "fix: thing\n\nBREAKING CHANGE: the flag is gone\x00", "major"},
		{"hyphenated footer", "fix: thing\n\nBREAKING-CHANGE: the flag is gone\x00", "major"},
		{"wrapped prose in a body", "docs: rewrite the guide\n\nEvery pipeline is now a\nfeat: in them, which is a wrapped sentence.\x00", "patch"},
		{"body quoting the convention", "docs: explain versioning\n\n    feat!: / BREAKING CHANGE:  -> major\x00", "patch"},
		{"feature subject", "feat(cli): add thing\x00fix: other\x00", "minor"},
		{"nothing notable", "chore: tidy\x00", "patch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitVersionBump(tt.commits); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A VERSION left below what is already published would resolve a downgrade,
// and `orq update` refuses downgrades, so nobody would be walked back out of it.
func TestResolveRefusesToPublishBelowTheHighestRelease(t *testing.T) {
	_, err := resolve(input{Version: "4.14.3", API: "4.15.0", ReleasedAPI: "4.15.0", Tags: legacyTags + "v5.0.0\n", Channel: "stable"})
	if err == nil {
		t.Fatal("resolve accepted a version below the highest release")
	}
}

// VERSION is hand-edited to force a number, which is when a "v5.1.0" or a "5.1"
// gets typed. Catch it on the PR, not at the release.
func TestRepoVERSIONResolves(t *testing.T) {
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(input{Version: strings.TrimSpace(string(raw)), API: "4.13.22", Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
}
