package commands

import (
	"testing"

	"orq/cli/custom/auth"
)

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
