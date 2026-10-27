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
// appended after it (for example traces conversation's active-project hint). A JSON
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
	case status == http.StatusUnauthorized && strings.Contains(strings.ToLower(body), "not authorized"):
		return forbiddenGuidance()
	case status == http.StatusUnauthorized:
		return "The credential was rejected. `orq doctor` shows which one is in use; run `orq auth login` or replace the key."
	case status == http.StatusForbidden && explicitAPIKey && strings.Contains(strings.ToLower(body), "out of scope"):
		return "The API key is limited to other projects. Use a key that covers this one; `orq doctor` shows the key in use."
	case status == http.StatusForbidden:
		return forbiddenGuidance()
	case status == http.StatusTooManyRequests:
		return "Rate limited. Wait a moment and retry."
	case status >= 500:
		return "The orq.ai API failed on its side. Retry; if it keeps failing, run `orq doctor` and report the output."
	}
	return ""
}

// forbiddenGuidance explains a 403 the API gave no usable detail for. The API
// says only that the caller "is not authorized"; what the CLI can add is which
// credential it sent, where that credential is configured, and the scope it
// carries, all read locally with no extra request.
func forbiddenGuidance() string {
	var b strings.Builder
	b.WriteString("The credential in use has no access to this resource.\n")
	session, _ := auth.ReadSession()
	cred := describeCredential(session)
	if cred == nil {
		b.WriteString("No credential was found to compare against; run `orq auth login`.")
		return b.String()
	}
	fmt.Fprintf(&b, "\nCredential sent: %s\n", credentialOrigin(cred.Source))
	fmt.Fprintf(&b, "Reaches:         %s\n", credentialReach(cred))
	b.WriteString("\nThe API accepted the credential but refused this request. Usual causes:\n")
	b.WriteString("  - the resource lives in a different workspace or project than the credential\n")
	b.WriteString("  - the key is scoped to specific projects, or lacks the role this action needs\n")
	b.WriteString("  - the member behind the login lost access (role change or removal)\n")
	b.WriteString("\nNext steps:\n")
	b.WriteString("  1. `orq doctor` shows the credential, workspace and project in force.\n")
	if cred.Source == "session" {
		b.WriteString("  2. `orq workspace use` / `orq projects use <key>` switch to the one that owns the resource.\n")
		b.WriteString("  3. `orq auth login` signs in again if your access changed.")
	} else {
		b.WriteString("  2. Use a key for the workspace and project that owns the resource, or unset it to fall back to your login.\n")
		b.WriteString("  3. Ask a workspace admin to check the key's role and project scope.")
	}
	return b.String()
}

// credentialOrigin says where a credential source is configured.
func credentialOrigin(source string) string {
	switch {
	case source == "session":
		return "your `orq auth login` session (stored under ~/.orq)"
	case strings.HasPrefix(source, "profile "):
		return source + " (the profile's api_key in the orq config file)"
	default:
		return source + " (environment variable; check your shell, ~/.orq/env or a .env file)"
	}
}

func credentialReach(c *IdentityCredential) string {
	workspace := "workspace unknown"
	if c.WorkspaceID != "" {
		workspace = "workspace " + c.WorkspaceID
	}
	switch c.Scope {
	case scopeProject:
		return workspace + ", project " + c.ProjectID + " only"
	case scopeAllProjects:
		return workspace + ", all projects"
	}
	return workspace + ", project scope not readable from the key"
}
