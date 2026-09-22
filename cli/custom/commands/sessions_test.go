package commands

import (
	"bytes"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// The table names hosts you cannot reach without switching server, and nothing
// else in the output says how, so the hint is part of the listing's contract.
func TestSessionListHintsAtSwitchingServerOnlyWithSomewhereToGo(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []auth.SessionListEntry
		want bool
	}{
		{"one login is nowhere to switch to", []auth.SessionListEntry{
			{Host: "my.orq.ai", Status: auth.SessionStatusOK, Active: true},
		}, false},
		{"a second host is", []auth.SessionListEntry{
			{Host: "my.orq.ai", Status: auth.SessionStatusOK, Active: true},
			{Host: "aim.orq.ai", Status: auth.SessionStatusNeedsRefresh},
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			previous := bartolocli.Stdout
			bartolocli.Stdout = &out
			t.Cleanup(func() { bartolocli.Stdout = previous })

			printSessionList(tc.rows, "orq")

			if got := strings.Contains(out.String(), "orq server set"); got != tc.want {
				t.Errorf("hint present = %v, want %v in:\n%s", got, tc.want, out.String())
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
