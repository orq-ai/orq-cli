package custom

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fake OS secret-store tools. They keep each secret in a file under
// $ORQ_FAKE_STORE and log every call to $ORQ_FAKE_STORE/calls, so a subprocess
// test can drive the real store code path without touching the real keychain.
// A file named "locked" in that directory makes every read fail.
const fakeSecurityTool = `#!/bin/sh
PATH=/usr/bin:/bin
echo "$*" >> "$ORQ_FAKE_STORE/calls"
case "$1" in
-i)
  while read -r line; do
    set -- $line
    s=$(printf '%s' "$4" | tr -d '"')
    a=$(printf '%s' "$6" | tr -d '"')
    printf '%s' "$8" > "$ORQ_FAKE_STORE/$s@$a"
  done
  ;;
find-generic-password)
  s=$3; a=$5
  if [ -e "$ORQ_FAKE_STORE/locked" ]; then
    echo "User interaction is not allowed." >&2
    exit 51
  fi
  [ -e "$ORQ_FAKE_STORE/$s@$a" ] || exit 44
  cat "$ORQ_FAKE_STORE/$s@$a"
  ;;
delete-generic-password)
  s=$3; a=$5
  [ -e "$ORQ_FAKE_STORE/$s@$a" ] || exit 44
  rm "$ORQ_FAKE_STORE/$s@$a"
  ;;
esac
`

const fakeSecretTool = `#!/bin/sh
PATH=/usr/bin:/bin
echo "$*" >> "$ORQ_FAKE_STORE/calls"
if [ -e "$ORQ_FAKE_STORE/locked" ]; then
  echo "Cannot autolaunch D-Bus without X11 \$DISPLAY" >&2
  exit 1
fi
case "$1" in
store)
  # store --label L service S account A
  cat > "$ORQ_FAKE_STORE/$5@$7"
  ;;
lookup)
  # lookup service S account A
  [ -e "$ORQ_FAKE_STORE/$3@$5" ] || exit 1
  printf '%s' "$(cat "$ORQ_FAKE_STORE/$3@$5")"
  ;;
clear)
  rm -f "$ORQ_FAKE_STORE/$3@$5"
  ;;
esac
`

// TestProfileKeyStoreRoundTrip runs the built binary against fake secret-store
// tools: the key moves out of credentials.json into the store, doctor says
// where it lives, a locked store is reported as unreadable (not missing), and
// ORQ_CREDENTIAL_STORE=file keeps the key in the file without touching a tool.
func TestProfileKeyStoreRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake store tools are shell scripts")
	}
	bin := buildOrqBinary(t)

	type env struct {
		home, store string
		vars        []string
	}
	setup := func(t *testing.T, extra ...string) env {
		t.Helper()
		home, store, fakeBin := t.TempDir(), t.TempDir(), t.TempDir()
		for name, body := range map[string]string{"security": fakeSecurityTool, "secret-tool": fakeSecretTool} {
			if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(home, "key.txt"), []byte("sk-orq-synthetic-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		vars := append([]string{
			"HOME=" + home, "USERPROFILE=" + home, "PATH=" + fakeBin,
			"ORQ_FAKE_STORE=" + store, "ORQ_NO_UPDATE_CHECK=1",
		}, extra...)
		return env{home: home, store: store, vars: vars}
	}
	run := func(t *testing.T, e env, args ...string) (stdout, stderr string, err error) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Dir = e.vars, e.home
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err = cmd.Run()
		return out.String(), errb.String(), err
	}
	addProfile := func(t *testing.T, e env) {
		t.Helper()
		if _, stderr, err := run(t, e, "--no-input", "auth", "profile", "add", "ci", "--api-key-file", filepath.Join(e.home, "key.txt")); err != nil {
			t.Fatalf("auth profile add: %v\n%s", err, stderr)
		}
	}
	readProfile := func(t *testing.T, e env) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(e.home, ".orq", "credentials.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Profiles map[string]map[string]any `json:"profiles"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Profiles["ci"]
	}
	doctorAuth := func(t *testing.T, e env) map[string]any {
		t.Helper()
		// A failing check exits non-zero; the report is what matters here.
		stdout, _, _ := run(t, e, "--profile", "ci", "--server", "http://127.0.0.1:1", "-o", "json", "doctor")
		var report struct {
			Auth map[string]any `json:"auth"`
		}
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("doctor output is not JSON: %v\n%s", err, stdout)
		}
		return report.Auth
	}
	wantMarker := "macOS Keychain"
	if runtime.GOOS == "linux" {
		wantMarker = "Linux secret-tool (libsecret)"
	}

	t.Run("stored", func(t *testing.T) {
		e := setup(t)
		addProfile(t, e)
		profile := readProfile(t, e)
		if profile["api_key_store"] != wantMarker {
			t.Fatalf("api_key_store = %v, want %q", profile["api_key_store"], wantMarker)
		}
		if _, has := profile["api_key"]; has {
			t.Errorf("api_key is still in credentials.json: %v", profile)
		}
		auth := doctorAuth(t, e)
		if auth["api_key_store"] != wantMarker || auth["status"] != "authenticated" {
			t.Errorf("doctor auth = %v", auth)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		e := setup(t)
		addProfile(t, e)
		if err := os.WriteFile(filepath.Join(e.store, "locked"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		auth := doctorAuth(t, e)
		if auth["status"] != "profile-store-unreadable" || auth["store_error"] == nil {
			t.Errorf("doctor auth = %v", auth)
		}
		_, stderr, err := run(t, e, "--profile", "ci", "--server", "http://127.0.0.1:1", "models", "list")
		if err == nil {
			t.Fatal("models list succeeded with a locked store")
		}
		for _, want := range []string{"readable", "still stored"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, "orq auth login") {
			t.Errorf("stderr sends the user to login:\n%s", stderr)
		}
		if profile := readProfile(t, e); profile["api_key_store"] != wantMarker {
			t.Errorf("marker overwritten: %v", profile)
		}
	})

	t.Run("forced_file", func(t *testing.T) {
		e := setup(t, "ORQ_CREDENTIAL_STORE=file")
		addProfile(t, e)
		profile := readProfile(t, e)
		if profile["api_key"] != "sk-orq-synthetic-key" {
			t.Errorf("api_key = %v, want the plaintext key", profile["api_key"])
		}
		if _, has := profile["api_key_store"]; has {
			t.Errorf("marker written with the file store forced: %v", profile)
		}
		if calls, err := os.ReadFile(filepath.Join(e.store, "calls")); err == nil && len(calls) > 0 {
			t.Errorf("a store tool ran with the file store forced:\n%s", calls)
		}
	})
}
