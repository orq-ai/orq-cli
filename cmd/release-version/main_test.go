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

func TestResolveRCBaseAndFreeRCNumber(t *testing.T) {
	tests := []struct {
		name, version, api, released, tags, wantVersion, wantPrevious, wantBump string
	}{
		{
			name:    "same line and free number",
			version: "5.0.0", api: "4.16.0", released: "4.15.0",
			tags: "v5.0.0\nv5.1.0-rc.1\nv5.1.0-rc.2\n", wantVersion: "5.1.0-rc.3", wantPrevious: "v5.1.0-rc.2", wantBump: "minor",
		},
		{
			name:    "previews a patch release",
			version: "5.1.3", api: "4.15.0", released: "4.15.0",
			tags: "v5.1.3\n", wantVersion: "5.1.4-rc.1", wantPrevious: "v5.1.3", wantBump: "patch",
		},
		{
			name:    "lagging VERSION previews a patch above the highest release",
			version: "5.0.0", api: "4.15.0", released: "4.15.0",
			tags: "v5.0.0\nv5.1.0\n", wantVersion: "5.1.1-rc.1", wantPrevious: "v5.0.0", wantBump: "patch",
		},
		{
			name:    "lagging VERSION previews a minor above the highest release",
			version: "4.14.3", api: "4.16.0", released: "4.15.0",
			tags: "v5.0.0\n", wantVersion: "5.1.0-rc.1", wantBump: "minor",
		},
		{
			name:    "lagging VERSION previews a major above the highest release",
			version: "4.14.3", api: "5.0.0", released: "4.15.0",
			tags: "v5.0.0\n", wantVersion: "6.0.0-rc.1", wantBump: "major",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(input{Version: tt.version, API: tt.api, ReleasedAPI: tt.released, Tags: tt.tags, Channel: "rc"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.wantVersion || got.PreviousTag != tt.wantPrevious || (tt.wantBump != "" && got.Bump != tt.wantBump) || !got.Prerelease {
				t.Fatalf("got %#v, want version=%s previous=%s bump=%s rc", got, tt.wantVersion, tt.wantPrevious, tt.wantBump)
			}
		})
	}
}

func TestResolveRCIsIndependentOfStableVERSIONAdvance(t *testing.T) {
	tests := []struct {
		name, firstVersion, secondVersion, tags, want string
	}{
		{name: "stable VERSION advance", firstVersion: "5.0.0", secondVersion: "5.1.0", tags: "v5.0.0\n", want: "5.1.0-rc.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := resolve(input{Version: tt.firstVersion, API: "4.16.0", ReleasedAPI: "4.15.0", Tags: tt.tags, Channel: "rc"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := resolve(input{Version: tt.secondVersion, API: "4.16.0", ReleasedAPI: "4.15.0", Tags: tt.tags, Channel: "rc"})
			if err != nil {
				t.Fatal(err)
			}
			if first.Version != second.Version || first.Version != tt.want {
				t.Fatalf("got %q before stable VERSION advance and %q after, want %q", first.Version, second.Version, tt.want)
			}
		})
	}
}

func TestVerifyRejectsTakenBelowFloorAndMalformedVersions(t *testing.T) {
	tests := []struct {
		name, version, wantErr string
		in                     input
	}{
		{
			name:    "taken tag",
			version: "5.1.0",
			wantErr: "already taken",
			in:      input{Channel: "stable", Tags: "v5.1.0\n"},
		},
		{
			name:    "below floor",
			version: "4.99.99",
			wantErr: "does not sort above",
			in:      input{Channel: "stable", Tags: "v5.0.0\n"},
		},
		{
			name:    "rc below the stable line",
			version: "5.0.0-rc.1",
			wantErr: "does not sort above",
			in:      input{Channel: "rc", Tags: "v5.0.0\nv5.1.0\n"},
		},
		{
			name:    "malformed",
			version: "5.1-rc.1",
			wantErr: "rc version must have the form",
			in:      input{Channel: "rc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := verify(tt.in, tt.version)
			if err == nil {
				t.Fatalf("verify(%q, %#v) succeeded", tt.version, tt.in)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("verify(%q) said %q, want it to mention %q", tt.version, err, tt.wantErr)
			}
		})
	}
}

func TestVerifyAcceptsResolvedVersions(t *testing.T) {
	tests := []struct {
		name string
		in   input
	}{
		{
			name: "stable",
			in: input{Version: "5.0.0", API: "4.16.0", ReleasedAPI: "4.15.0",
				Commits: "", Tags: "v5.0.0\n", Channel: "stable"},
		},
		{
			name: "rc",
			in: input{Version: "5.0.0", API: "4.16.0", ReleasedAPI: "4.15.0",
				Commits: "", Tags: "v5.0.0\n", Channel: "rc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolve(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := verify(tt.in, resolved.Version)
			if err != nil {
				t.Fatal(err)
			}
			if verified.Version != resolved.Version || verified.Tag != resolved.Tag || verified.Prerelease != resolved.Prerelease {
				t.Fatalf("got %#v, want version=%s tag=%s prerelease=%t", verified, resolved.Version, resolved.Tag, resolved.Prerelease)
			}
		})
	}
}

func TestResolveRejectsInvalidInput(t *testing.T) {
	for _, in := range []input{
		{Version: "5.01.0", API: "4.15.0", Channel: "stable"},
		{Version: "5.0.0", API: "", Channel: "stable"},
		{Version: "5.0.0", API: "4.x", Channel: "stable"},
		{Version: "5.0.0", API: "4.15.0-01", Channel: "stable"},
		{Version: "5.0.0", API: "4.15.0", ReleasedAPI: "4.14.0-01", Channel: "stable"},
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
