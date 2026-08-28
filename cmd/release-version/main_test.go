package main

import "testing"

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
