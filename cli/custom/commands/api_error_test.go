package commands

import (
	"errors"
	"strings"
	"testing"
)

func TestExplainAPIError(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
		absent   []string
		apiKey   bool
	}{
		{
			name:   "message and doc link replace the raw body, with a fix",
			in:     "error calling operation: HTTP 401:\n" + `{"code":"authentication_required","message":"Token invalid.","request_id":"r1","doc_url":"https://docs.orq.ai/errors/x","details":{}}`,
			want:   []string{"HTTP 401 Unauthorized: Token invalid.", "docs: https://docs.orq.ai/errors/x", "request_id: r1", "orq auth login"},
			absent: []string{"error calling operation", `"code"`},
		},
		{
			name:   "project scope 403 says the key is the limit",
			apiKey: true,
			in:     "error calling operation: HTTP 403:\n" + `{"code":7,"message":"Project out of scope for this API key.","details":[]}`,
			want:   []string{"HTTP 403 Forbidden: Project out of scope", "limited to other projects"},
		},
		{
			name:   "the same 403 under a session does not blame an API key",
			in:     "error calling operation: HTTP 403:\n" + `{"code":7,"message":"Project out of scope for this API key.","details":[]}`,
			want:   []string{"no access to this"},
			absent: []string{"limited to other projects"},
		},
		{
			name: "validation details are kept whole",
			in:   "error calling operation: HTTP 400:\n" + `{"message":"Invalid body","details":{"issues":[{"path":["name"],"message":"Required"}]}}`,
			want: []string{"HTTP 400 Bad Request: Invalid body", "name: Required"},
		},
		{
			name: "a non-JSON body passes through",
			in:   "error calling operation: HTTP 502:\nbad gateway",
			want: []string{"HTTP 502 Bad Gateway: bad gateway", "failed on its side"},
		},
		{
			name: "404 keeps the status NotFoundScopeHint matches on",
			in:   "error calling operation: HTTP 404:\n" + `{"message":"trace not found"}`,
			want: []string{"HTTP 404 Not Found: trace not found"},
		},
		{
			name: "context a command wrapped around the status is kept",
			in:   "fetch trace t1: HTTP 404:\n" + `{"message":"trace not found"}`,
			want: []string{"fetch trace t1: HTTP 404 Not Found: trace not found"},
		},
		{
			name:   "the --profile variant of the stale remedy is replaced too",
			in:     "missing API key; configure a profile with `auth setup --profile dev` or set ORQ_API_KEY",
			want:   []string{"run `orq auth login` or set ORQ_API_KEY"},
			absent: []string{"auth setup"},
		},
		{
			name:   "the stale auth setup remedy names orq auth login",
			in:     "error calling operation: request failed: missing API key; configure a profile with `auth setup` or set ORQ_API_KEY",
			want:   []string{"run `orq auth login` or set ORQ_API_KEY"},
			absent: []string{"auth setup"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := explicitAPIKey
			explicitAPIKey = tc.apiKey
			t.Cleanup(func() { explicitAPIKey = prev })
			orig := errors.New(tc.in)
			got := ExplainAPIError(orig)
			if !errors.Is(got, orig) {
				t.Errorf("rewritten error no longer wraps the original")
			}
			for _, w := range tc.want {
				if !strings.Contains(got.Error(), w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got.Error(), a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}
	if ExplainAPIError(nil) != nil {
		t.Error("nil error was not passed through")
	}
	plain := errors.New("unknown profile")
	if ExplainAPIError(plain) != plain {
		t.Error("an unrelated error was rewritten")
	}
}
