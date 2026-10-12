package launch

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

// The gateway groups every request carrying the same X-ORQ-THREAD-ID into one
// thread, and records each X-ORQ-METADATA-<key> header as metadata.<key> on the
// trace. Agents that send no conversation id of their own need the first, or
// each model call of a session lands as its own unrelated trace.
const (
	threadIDHeader       = "X-ORQ-THREAD-ID"
	metadataHeaderPrefix = "X-ORQ-METADATA-"
)

// Letters, digits and single inner hyphens: the part of a header name every
// proxy between the agent and the gateway passes through unchanged. Underscores
// are left out because nginx drops headers containing them by default.
var metadataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// RequestHeader is one header launch asks an agent to send on its model calls.
type RequestHeader struct {
	Name  string
	Value string
}

// validHeaderValue accepts printable ASCII without the characters any agent's
// config gives a meaning, so a value that works for one agent works for all.
// Commas: gemini-cli reads its headers from one comma-separated env var and
// splits a value at a comma followed by "name:". Braces: opencode expands
// {env:VAR} and {file:path} anywhere in its config. Dollar: pi expands $NAME
// in header values. A leading "!": pi runs the rest as a shell command at
// request time. Each would otherwise send a variable, a file or a command's
// output to the gateway.
func validHeaderValue(flag, value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("flag %s: value must be non-empty with no leading or trailing spaces", flag)
	}
	if strings.HasPrefix(value, "!") {
		return fmt.Errorf("flag %s: value must not start with '!'", flag)
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("flag %s: value must be printable ASCII", flag)
		}
		switch r {
		case ',', '{', '}', '$':
			return fmt.Errorf("flag %s: value must not contain %q", flag, r)
		}
	}
	return nil
}

func setThreadID(flags *GatewayFlags, value string) error {
	if err := validHeaderValue("--thread-id", value); err != nil {
		return err
	}
	flags.ThreadID = value
	return nil
}

func addMetadata(flags *GatewayFlags, pair string) error {
	key, value, ok := strings.Cut(pair, "=")
	if !ok {
		return fmt.Errorf("flag --metadata expects key=value, got %q", pair)
	}
	if !metadataKeyPattern.MatchString(key) {
		return fmt.Errorf("flag --metadata: key %q must be letters, digits and inner hyphens", key)
	}
	if err := validHeaderValue("--metadata", value); err != nil {
		return err
	}
	for _, existing := range flags.Metadata {
		// Header names are case-insensitive, so SUITE and suite are one key.
		if strings.EqualFold(existing.Key, key) {
			return fmt.Errorf("flag --metadata: key %q given twice", key)
		}
	}
	flags.Metadata = append(flags.Metadata, MetadataPair{Key: key, Value: value})
	return nil
}

// assignThreadID gives the session a thread id when the agent needs one and
// the caller did not pass --thread-id. It runs once, before Resolve, so the id
// the agent is handed is the id launch reports, dry run included.
func assignThreadID(def *AgentDef, flags *GatewayFlags) {
	if def.RequestHeaders && !def.OwnThread && flags.ThreadID == "" {
		flags.ThreadID = newThreadID()
	}
}

// threadHeaders is the header set for one launch: the thread id when there is
// one, then each --metadata pair in flag order.
func threadHeaders(flags GatewayFlags) []RequestHeader {
	var headers []RequestHeader
	if flags.ThreadID != "" {
		headers = append(headers, RequestHeader{Name: threadIDHeader, Value: flags.ThreadID})
	}
	for _, m := range flags.Metadata {
		headers = append(headers, RequestHeader{Name: metadataHeaderPrefix + m.Key, Value: m.Value})
	}
	return headers
}

// mergeHeaderEnv adds launch's headers to a header list the user may already
// export, each entry "Name: value": comma-separated for gemini-cli's
// GEMINI_CLI_CUSTOM_HEADERS, newline-separated for claude's
// ANTHROPIC_CUSTOM_HEADERS. The user's entries are kept except those naming a
// header launch sets: launch's replace them outright rather than relying on
// which duplicate the agent happens to keep. Kept entries are carried over
// byte for byte, spacing included.
func mergeHeaderEnv(existing string, delim rune, headers []RequestHeader) string {
	var entries []string
	for _, entry := range splitHeaderEnv(existing, delim) {
		name, _, _ := strings.Cut(entry, ":")
		if !setsHeader(headers, strings.TrimSpace(name)) {
			entries = append(entries, entry)
		}
	}
	join := string(delim)
	if delim == ',' {
		join = ", "
	}
	out := strings.Join(entries, string(delim))
	for _, h := range headers {
		if out != "" {
			out += join
		}
		out += h.Name + ": " + h.Value
	}
	return out
}

// splitHeaderEnv splits a "Name: value" list at delim, returning each entry's
// raw text. In a comma list a piece holding no colon is the tail of the
// previous value (a value containing a comma), which is how gemini-cli reads
// it too.
func splitHeaderEnv(list string, delim rune) []string {
	var entries []string
	for _, piece := range strings.Split(list, string(delim)) {
		if strings.TrimSpace(piece) == "" {
			continue
		}
		if delim == ',' && !strings.Contains(piece, ":") && len(entries) > 0 {
			entries[len(entries)-1] += string(delim) + piece
			continue
		}
		entries = append(entries, piece)
	}
	return entries
}

// headerMap is headers keyed by name, the shape the JSON and TOML configs
// take. Names are unique: addMetadata refuses a repeated key.
func headerMap(headers []RequestHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	byName := make(map[string]string, len(headers))
	for _, h := range headers {
		byName[h.Name] = h.Value
	}
	return byName
}

func setsHeader(headers []RequestHeader, name string) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// newThreadID returns a random RFC 4122 version 4 UUID.
func newThreadID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
