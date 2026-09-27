package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"orq/cli/custom/auth"
)

// bartolo renders a failed response as "HTTP <status>:\n<body>". A generated
// command prefixes it with "error calling operation: "; a custom command may
// wrap it in its own context, which is kept.
var apiErrorPattern = regexp.MustCompile(`(?s)HTTP (\d{3}):\n(.*)$`)

const operationPrefix = "error calling operation: "

// staleAuthRemedy is the missing-key remedy bartolo's apikey handler prints. It
// names `auth setup`, which this CLI removed; the `--profile` variants too.
var staleAuthRemedy = regexp.MustCompile("configure a profile with `auth setup[^`]*`")

// apiError keeps the original error for errors.As / exit-code mapping and
// replaces only what a person reads.
type apiError struct {
	text string
	err  error
}

func (e *apiError) Error() string { return e.text }
func (e *apiError) Unwrap() error { return e.err }

// ExplainAPIError turns bartolo's raw "HTTP 403:\n{json}" into the API's own
// message plus the next step for that status. Errors of any other shape pass
// through untouched. The status stays in the text as "HTTP <code>" so callers
// that match on it (NotFoundScopeHint) keep working.
func ExplainAPIError(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if staleAuthRemedy.MatchString(text) {
		return &apiError{text: staleAuthRemedy.ReplaceAllString(text, "run `orq auth login`"), err: err}
	}
	loc := apiErrorPattern.FindStringSubmatchIndex(text)
	if loc == nil {
		return err
	}
	status, _ := strconv.Atoi(text[loc[2]:loc[3]])
	body := strings.TrimSpace(text[loc[4]:loc[5]])
	prefix := strings.TrimPrefix(text[:loc[0]], operationPrefix)
	// A non-JSON body (a proxy's error page) is its own best message.
	msg := body
	if json.Valid([]byte(body)) {
		msg = auth.DescribeAPIError(status, []byte(body))
	}
	out := fmt.Sprintf("%sHTTP %d %s", prefix, status, http.StatusText(status))
	if msg != "" {
		out += ": " + msg
	}
	if fix := apiErrorFix(status, body); fix != "" {
		out += "\n" + fix
	}
	return &apiError{text: out, err: err}
}

// apiErrorFix is the one next step for a status. 404 is left to
// NotFoundScopeHint, which knows the project the read was scoped to.
func apiErrorFix(status int, body string) string {
	switch {
	case status == http.StatusUnauthorized:
		return "The credential was rejected. `orq status` shows which one is in use; run `orq auth login` or replace the key."
	case status == http.StatusForbidden && explicitAPIKey && strings.Contains(strings.ToLower(body), "out of scope"):
		return "The API key is limited to other projects. Use a key that covers this one; `orq status` shows the key in use."
	case status == http.StatusForbidden:
		return "The credential in use has no access to this. `orq status` shows which one it is."
	case status == http.StatusTooManyRequests:
		return "Rate limited. Wait a moment and retry."
	case status >= 500:
		return "The orq.ai API failed on its side. Retry; if it keeps failing, run `orq doctor` and report the output."
	}
	return ""
}
