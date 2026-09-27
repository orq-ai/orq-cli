package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// bartolo renders a failed response as "HTTP <status>:\n<body>", prefixed by
// "error calling operation: " for a generated command.
var apiErrorPattern = regexp.MustCompile(`(?s)^(?:error calling operation: )?HTTP (\d{3}):\n(.*)$`)

// staleAuthRemedy is the missing-key remedy bartolo's apikey handler prints. It
// names `auth setup`, which this CLI removed.
const staleAuthRemedy = "configure a profile with `auth setup`"

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
	if strings.Contains(text, staleAuthRemedy) {
		return &apiError{text: strings.Replace(text, staleAuthRemedy, "run `orq auth login`", 1), err: err}
	}
	m := apiErrorPattern.FindStringSubmatch(text)
	if m == nil {
		return err
	}
	status, _ := strconv.Atoi(m[1])
	out := fmt.Sprintf("HTTP %d %s: %s", status, http.StatusText(status), apiErrorMessage(strings.TrimSpace(m[2])))
	if fix := apiErrorFix(status, text); fix != "" {
		out += "\n" + fix
	}
	return &apiError{text: out, err: err}
}

// apiErrorMessage is the body's own message when it is JSON that carries one,
// followed by its doc link if it has one. A body with details (validation
// errors) is kept whole: those details say what to change.
func apiErrorMessage(body string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(body), &fields) != nil {
		return body
	}
	message, _ := fields["message"].(string)
	if message == "" {
		message, _ = fields["error"].(string)
	}
	if message == "" {
		return body
	}
	for _, key := range []string{"details", "errors"} {
		if !emptyJSON(fields[key]) {
			return message + "\n" + body
		}
	}
	if doc, _ := fields["doc_url"].(string); doc != "" {
		message += "\nDocs: " + doc
	}
	return message
}

func emptyJSON(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// apiErrorFix is the one next step for a status. 404 is left to
// NotFoundScopeHint, which knows the project the read was scoped to.
func apiErrorFix(status int, text string) string {
	switch {
	case status == http.StatusUnauthorized:
		return "The credential was rejected. Check which one is in use with `orq status`, then `orq auth login` or replace the key."
	case status == http.StatusForbidden && strings.Contains(strings.ToLower(text), "out of scope"):
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
