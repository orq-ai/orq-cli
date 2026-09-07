package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusReportsInstalledSourceCommit(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	gen := filepath.Join(home, ".orq", "snapshot", "gen-test")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(filepath.Join(gen, "SOURCE.json"), []byte("{\"commit\":\""+commit+"\"}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(&Manifest{Fingerprint: "fingerprint", Generation: gen}); err != nil {
		t.Fatal(err)
	}

	status, err := ReadStatus()
	if err != nil || status == nil {
		t.Fatalf("ReadStatus() = %+v, %v", status, err)
	}
	if status.Version != commit {
		t.Errorf("Version = %q, want %q", status.Version, commit)
	}
}

func TestReadStatusFallsBackToFingerprintWithoutSourceCommit(t *testing.T) {
	for name, source := range map[string]string{
		"missing":   "",
		"malformed": "{not-json}",
		"blank":     "{\"commit\":\"\"}",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			setHome(t, home)
			gen := filepath.Join(home, ".orq", "snapshot", "gen-test")
			if err := os.MkdirAll(gen, 0o755); err != nil {
				t.Fatal(err)
			}
			if source != "" {
				if err := os.WriteFile(filepath.Join(gen, "SOURCE.json"), []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := SaveManifest(&Manifest{Fingerprint: "bundle-fingerprint", Generation: gen}); err != nil {
				t.Fatal(err)
			}

			status, err := ReadStatus()
			if err != nil || status == nil {
				t.Fatalf("ReadStatus() = %+v, %v", status, err)
			}
			if status.Version != "bundle-fingerprint" {
				t.Errorf("Version = %q, want fingerprint fallback", status.Version)
			}
		})
	}
}
