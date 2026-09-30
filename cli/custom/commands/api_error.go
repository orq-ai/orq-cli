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
// message plus the next step for that status. It also replaces bartolo's stale
// missing-key remedy; other errors pass through untouched. The status stays in
// the text as "HTTP <code>" so NotFoundScopeHint keeps working.
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
	body, suffix := splitAPIErrorBody(text[loc[4]:loc[5]])
	prefix := strings.TrimPrefix(text[:loc[0]], operationPrefix)
	msg := auth.DescribeAPIError(status, []byte(body))
	statusLabel := fmt.Sprintf("HTTP %d", status)
	if reason := http.StatusText(status); reason != "" {
		statusLabel += " " + reason
	}
	out := prefix + statusLabel
	if msg != "" {
		out += ": " + msg
	}
	out += suffix
	if fix := apiErrorFix(status, body); fix != "" {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += fix
	}
	return &apiError{text: out, err: err}
}

// splitAPIErrorBody separates a JSON response from context a custom command
// appended after it (for example traces thread's active-project hint). A JSON
// decoder exposes the byte offset of the first complete value; plaintext stays
// whole and is bounded by DescribeAPIError.
func splitAPIErrorBody(raw string) (body, suffix string) {
	trimmed := strings.TrimSpace(raw)
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return trimmed, ""
	}
	offset := decoder.InputOffset()
	body = strings.TrimSpace(trimmed[:offset])
	suffix = trimmed[offset:]
	if strings.TrimSpace(suffix) == "" {
		suffix = ""
	}
	return body, suffix
}

// apiErrorFix is the one next step for a status. 404 is left to
// NotFoundScopeHint, which knows the project the read was scoped to.
func apiErrorFix(status int, body string) string {
	switch {
	case status == http.StatusUnauthorized:
		return "The credential was rejected. `orq doctor` shows which one is in use; run `orq auth login` or replace the key."
	case status == http.StatusForbidden && explicitAPIKey && strings.Contains(strings.ToLower(body), "out of scope"):
		return "The API key is limited to other projects. Use a key that covers this one; `orq doctor` shows the key in use."
	case status == http.StatusForbidden:
		return "The credential in use has no access to this. `orq doctor` shows which one it is."
	case status == http.StatusTooManyRequests:
		return "Rate limited. Wait a moment and retry."
	case status >= 500:
		return "The orq.ai API failed on its side. Retry; if it keeps failing, run `orq doctor` and report the output."
	}
	return ""
}
