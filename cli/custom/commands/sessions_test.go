package commands

import (
	"bytes"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// The table names hosts you cannot reach without switching server, and nothing
// else in the output says how. The named URL comes from the row rather than its
// HOST cell, which is a file name a ported host cannot round-trip through.
func TestSessionListHintNamesAUsableOtherLogin(t *testing.T) {
	active := auth.SessionListEntry{
		Host: "my.orq.ai", Server: "https://my.orq.ai", Status: auth.SessionStatusOK, Active: true,
	}
	for _, tc := range []struct {
		name string
		rows []auth.SessionListEntry
		want string // the server the hint must name, or "" for no hint
	}{
		{"one login is nowhere to switch to", []auth.SessionListEntry{active}, ""},
		{"a refreshable login elsewhere", []auth.SessionListEntry{active, {
			Host: "self_8080", Server: "https://self.example.com:8080", Status: auth.SessionStatusNeedsRefresh,
		}}, "https://self.example.com:8080"},
		{"a broken login is not somewhere to go", []auth.SessionListEntry{active, {
			Host: "aim.orq.ai", Server: "https://aim.orq.ai", Status: auth.SessionStatusInvalid,
		}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			previous := bartolocli.Stdout
			bartolocli.Stdout = &out
			t.Cleanup(func() { bartolocli.Stdout = previous })

			printSessionList(tc.rows, &cobra.Command{Use: "orq"})

			got := out.String()
			if tc.want == "" {
				if strings.Contains(got, "server set") {
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
