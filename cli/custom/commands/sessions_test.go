package commands

import (
	"bytes"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// The table names hosts you cannot reach without switching server, and nothing
// else in the output says how. The named URL comes from the row rather than its
// HOST cell, which is a file name a ported host cannot round-trip through.
func TestSessionListHintNamesAUsableOtherLogin(t *testing.T) {
	active := auth.SessionListEntry{
		Host: "my.orq.ai", Server: "https://my.orq.ai", Status: auth.SessionStatusOK, Active: true,
	}
	// Built through SessionHost so the fixture cannot pair a HOST cell with a
	// server that would never produce it — the round-trip this hint avoids.
	const ported = "https://self.example.com:8080"
	elsewhere := auth.SessionListEntry{
		Host: auth.SessionHost(ported), Server: ported, Status: auth.SessionStatusNeedsRefresh,
	}
	for _, tc := range []struct {
		name string
		rows []auth.SessionListEntry
		want string // the server the hint must name, or "" for no hint
	}{
		{"one login is nowhere to switch to", []auth.SessionListEntry{active}, ""},
		{"a refreshable login elsewhere", []auth.SessionListEntry{active, elsewhere}, ported},
		// Pointed at a host you never logged into: no row is active, so the
		// one login there is is still somewhere to go.
		{"the only login is elsewhere", []auth.SessionListEntry{elsewhere}, ported},
		{"a broken login is not somewhere to go", []auth.SessionListEntry{active, {
			Host: "aim.orq.ai", Server: "https://aim.orq.ai", Status: auth.SessionStatusInvalid,
		}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			previous := bartolocli.Stdout
			bartolocli.Stdout = &out
			t.Cleanup(func() { bartolocli.Stdout = previous })
			// The phrasing depends on the resolved source; this test is about
			// which row gets named, so pin the source that recommends
			// `server set` and leave the rest to TestSwitchHint…
			prevServer, prevSource := auth.Server(), auth.ServerSource()
			auth.SetServer(prevServer, "config")
			t.Cleanup(func() { auth.SetServer(prevServer, prevSource) })

			printSessionList(tc.rows, "orq")

			got := out.String()
			if tc.want == "" {
				if strings.Contains(got, "server set") || strings.Contains(got, "--server") {
					t.Fatalf("hint on nothing to switch to:\n%s", got)
				}
				return
			}
			if !strings.Contains(got, "orq server set "+tc.want) {
				t.Errorf("hint does not name %s:\n%s", tc.want, got)
			}
			// The one-off form is half the answer: a user who does not want a
			// new default still needs to be told it exists.
			if !strings.Contains(got, "--server") {
				t.Errorf("hint drops the per-call form:\n%s", got)
			}
		})
	}
}

// `server set` writes the lowest-precedence source there is, so recommending it
// to someone on ORQ_SERVER or a profile-bound server names a command that
// changes nothing. Each source gets the instruction that would work.
func TestSwitchHintNamesWhatOutranksTheCurrentHost(t *testing.T) {
	const target = "https://aim.orq.ai"
	for _, tc := range []struct {
		source   string
		want     string
		unwanted string
	}{
		{"config", "orq server set " + target, ""},
		{"default", "orq server set " + target, ""},
		{"env", "ORQ_SERVER", "server set"},
		{"profile", "profile in force", "server set"},
		{"flag", "--server " + target, "server set"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got := switchHint("orq", target, tc.source)
			if !strings.Contains(got, tc.want) {
				t.Errorf("source %q: %q does not mention %q", tc.source, got, tc.want)
			}
			if tc.unwanted != "" && strings.Contains(got, tc.unwanted) {
				t.Errorf("source %q: %q recommends %q, which it outranks", tc.source, got, tc.unwanted)
			}
			// Every phrasing keeps the escape hatch that always works.
			if !strings.Contains(got, "--server "+target) {
				t.Errorf("source %q: %q drops the per-call form", tc.source, got)
			}
		})
	}
}

func TestUsableSessionStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{auth.SessionStatusOK, true},
		{auth.SessionStatusNeedsRefresh, true},
		{auth.SessionStatusInvalid, false},
		{auth.SessionStatusUnreadable, false},
	} {
		if got := usableSessionStatus(tc.status); got != tc.want {
			t.Errorf("usableSessionStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestUsableSessionCount(t *testing.T) {
	rows := []auth.SessionListEntry{
		{Status: auth.SessionStatusOK},
		{Status: auth.SessionStatusNeedsRefresh},
		{Status: auth.SessionStatusInvalid},
		{Status: auth.SessionStatusUnreadable},
	}
	if got := usableSessionCount(rows); got != 2 {
		t.Fatalf("usableSessionCount() = %d, want 2", got)
	}
}
