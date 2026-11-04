package launch

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The gateway groups every request carrying the same X-ORQ-THREAD-ID into one
// thread, and records each X-ORQ-METADATA-<key> header as metadata.<key> on the
// trace, with the key in lowercase.
const (
	threadIDHeader       = "X-ORQ-THREAD-ID"
	metadataHeaderPrefix = "X-ORQ-METADATA-"

	claudeHeadersEnv = "ANTHROPIC_CUSTOM_HEADERS"
	geminiHeadersEnv = "GEMINI_CLI_CUSTOM_HEADERS"
)

// The gateway's own limits. Past them it drops or truncates without an error,
// so a run would lose its tags with nothing to say so; the CLI refuses instead.
const (
	maxMetadataKeys        = 20
	maxMetadataKeyLength   = 64
	maxMetadataValueLength = 256
)

// Letters, digits and single inner hyphens: the part of a header name every
// proxy between the agent and the gateway passes through unchanged. Underscores
// are left out because nginx drops headers containing them by default.
var metadataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// reservedMetadataKeys are the gateway's well-known metadata headers
// (wellKnownMetadataHeaders in orquesta-web's metadata_headers.go). Caller
// metadata overrides them, so a key from this list would rewrite the session id
// a trace groups on or the framework it is attributed to.
var reservedMetadataKeys = []string{
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"http-referer",
	"originator",
	"repo",
	"session-id",
	"thread-id",
	"user-agent",
	"x-app",
	"x-billing-invoke-origin",
	"x-claude-code-agent-id",
	"x-claude-code-agent-type",
	"x-claude-code-parent-agent-id",
	"x-claude-code-prompt-id",
	"x-claude-code-request-class",
	"x-claude-code-session-id",
	"x-client-app",
	"x-client-type",
	"x-codex-beta-features",
	"x-cursor-conversation-id",
	"x-git-remote",
	"x-opencode-client",
	"x-opencode-session",
	"x-opencode-session-id",
	"x-openrouter-title",
	"x-pplx-integration",
	"x-repository",
	"x-session-id",
	"x-task-id",
	"x-title",
}

// RequestHeader is one header launch asks an agent to send on its model calls.
type RequestHeader struct {
	Name  string
	Value string
}

// checkHeaderValue accepts printable ASCII without the characters an agent's
// config gives a meaning, so a value that works for one agent works for all.
// Commas: gemini-cli reads its headers from one comma-separated env var and
// splits at a comma when the text after it looks like a header. Braces: opencode
// expands {env:VAR} and {file:path} anywhere in its config, which would send a
// variable or a file to the gateway.
func checkHeaderValue(flag, value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("flag %s: value must be non-empty with no leading or trailing spaces", flag)
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("flag %s: value must be printable ASCII", flag)
		}
		switch r {
		case ',', '{', '}':
			return fmt.Errorf("flag %s: value must not contain %q", flag, r)
		}
	}
	return nil
}

func setMCPAuth(flags *GatewayFlags, value string) error {
	switch value {
	case "oauth":
		flags.MCPEnvAuth = false
	case "env":
		flags.MCPEnvAuth = true
	default:
		return fmt.Errorf("flag --mcp-auth expects oauth or env, got %q", value)
	}
	return nil
}

func setThreadID(flags *GatewayFlags, value string) error {
	if err := checkHeaderValue("--thread-id", value); err != nil {
		return err
	}
	// takeValue already refuses this for the two-argument form.
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("flag --thread-id: value must not start with '-'")
	}
	flags.ThreadID = value
	return nil
}

func addMetadata(flags *GatewayFlags, pair string) error {
	// Neither error repeats the argument: a mistyped pair can be a value that
	// belongs nowhere near a CI log.
	key, value, ok := strings.Cut(pair, "=")
	if !ok {
		return fmt.Errorf("flag --metadata expects key=value")
	}
	if !metadataKeyPattern.MatchString(key) || len(key) > maxMetadataKeyLength {
		return fmt.Errorf("flag --metadata: a key must be at most %d letters, digits and inner hyphens", maxMetadataKeyLength)
	}
	if slices.Contains(reservedMetadataKeys, strings.ToLower(key)) {
		return fmt.Errorf("flag --metadata: key %q is reserved, the gateway reads it for its own grouping and attribution", key)
	}
	if err := checkHeaderValue("--metadata", value); err != nil {
		return err
	}
	if len(value) > maxMetadataValueLength {
		return fmt.Errorf("flag --metadata: value for %q is longer than %d characters", key, maxMetadataValueLength)
	}
	// Header names are case-insensitive, so SUITE and suite are one key.
	name := metadataHeaderPrefix + key
	if setsHeader(flags.Metadata, name) {
		return fmt.Errorf("flag --metadata: key %q given twice", key)
	}
	if len(flags.Metadata) == maxMetadataKeys {
		return fmt.Errorf("flag --metadata: at most %d keys", maxMetadataKeys)
	}
	flags.Metadata = append(flags.Metadata, RequestHeader{Name: name, Value: value})
	return nil
}

// checkSessionFlags runs after the scan, because --gateway and --no-gateway are
// last-wins: `--thread-id x --no-gateway` has to fail whichever comes first, and
// `--no-gateway --thread-id x --gateway` is fine. A flag the agent cannot honour
// is an error rather than passthrough: handed to the agent it may be ignored,
// and the run fails much later with no orq tools or no tags. Someone who means
// the agent's own flag of that name writes it after --.
func checkSessionFlags(flags GatewayFlags, opts ParseArgvOptions) error {
	if flags.MCPEnvAuth {
		if !opts.AllowMCPAuth {
			return fmt.Errorf("--mcp-auth env is not supported for this agent; the agent's own flag of that name goes after --")
		}
		if !flags.MCP {
			return fmt.Errorf("--mcp-auth env needs the orq MCP server; drop --no-mcp")
		}
	}
	if flags.ThreadID != "" || len(flags.Metadata) > 0 {
		if !opts.AllowRequestHeaders {
			return fmt.Errorf("--thread-id and --metadata are not supported for this agent; the agent's own flag of that name goes after --")
		}
		// AllowTrace first: Router defaults to false for every agent without
		// that flag, where the calls still go through the gateway.
		if opts.AllowTrace && !flags.Router {
			return fmt.Errorf("--thread-id and --metadata need gateway routing; drop --no-gateway")
		}
	}
	return nil
}

// threadHeaders is the header set for one launch: the thread id when there is
// one, then each --metadata pair in flag order.
func threadHeaders(flags GatewayFlags) []RequestHeader {
	var headers []RequestHeader
	if flags.ThreadID != "" {
		headers = append(headers, RequestHeader{Name: threadIDHeader, Value: flags.ThreadID})
	}
	return append(headers, flags.Metadata...)
}

// generatedGeminiThreadID returns a new thread id when the caller set none, and
// nothing otherwise. gemini-cli 0.58.0 sends no session id, so without one each
// of its model calls is a lone trace. An id the caller exported is left alone:
// adding one here would make mergeHeaderEnv replace it. A blank one holds no id,
// and the gateway ignores it. The gateway answers X-ORQ-THREAD-ID under a factory
// work unit token with a 400 (middlewares/thread.go), so a gemini launch under
// such a token fails.
func generatedGeminiThreadID(flags GatewayFlags, exported string) string {
	if flags.ThreadID != "" {
		return ""
	}
	for _, entry := range splitHeaderEnv(exported, ',') {
		name, value, _ := strings.Cut(entry, ":")
		if strings.EqualFold(strings.TrimSpace(name), threadIDHeader) && strings.TrimSpace(value) != "" {
			return ""
		}
	}
	return rand.Text()
}

// mergeHeaderEnv adds launch's headers to a header list the user may already
// export, each entry "Name: value": comma-separated for gemini-cli,
// newline-separated for claude. The user's entries are kept except those naming
// a header launch sets: launch's replace them outright rather than relying on
// which duplicate the agent happens to keep. Kept entries are carried over byte
// for byte, spacing included.
func mergeHeaderEnv(existing string, delim rune, headers []RequestHeader) string {
	var entries []string
	for _, entry := range splitHeaderEnv(existing, delim) {
		name, _, _ := strings.Cut(entry, ":")
		if !setsHeader(headers, strings.TrimSpace(name)) {
			entries = append(entries, entry)
		}
	}
	for _, h := range headers {
		entries = append(entries, h.Name+": "+h.Value)
	}
	return strings.Join(entries, string(delim))
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

// redactHeaderList is what --dry-run prints for a header list: thread and
// metadata entries as they are, anything else as a bare placeholder. A merged list
// can hold the user's own Authorization header, which only ever reached the agent's
// environment before and must not start appearing on stdout. Not even the name
// survives: in a malformed entry, or after a comma in a value, the text before a
// colon can be part of the secret. A tag holding the delimiter goes too, because
// splitHeaderEnv folds a following piece with no colon into it, and launch never
// writes a tag like that. One line, because the dry run prints one KEY=value per
// line.
func redactHeaderList(list string, delim rune) string {
	entries := splitHeaderEnv(list, delim)
	for i, entry := range entries {
		name, _, _ := strings.Cut(entry, ":")
		name = strings.TrimSpace(name)
		isTag := strings.EqualFold(name, threadIDHeader) || strings.HasPrefix(strings.ToUpper(name), metadataHeaderPrefix)
		if isTag && !strings.ContainsRune(entry, delim) {
			entries[i] = strings.TrimSpace(entry)
			continue
		}
		entries[i] = "<redacted>"
	}
	return strings.Join(entries, ", ")
}

// headerMap is headers keyed by name, the shape the JSON configs take. Names
// are unique: addMetadata refuses a repeated key.
func headerMap(headers []RequestHeader) map[string]string {
	byName := make(map[string]string, len(headers))
	for _, h := range headers {
		byName[h.Name] = h.Value
	}
	return byName
}

func setsHeader(headers []RequestHeader, name string) bool {
	return slices.ContainsFunc(headers, func(h RequestHeader) bool {
		return strings.EqualFold(h.Name, name)
	})
}
