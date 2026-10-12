package launch

import (
	"fmt"
	"strings"
)

// AgentContext is everything an agent's Resolve needs, with injectable seams
// for tests (Getenv, Fetch, ExecProbe).
type AgentContext struct {
	Creds  *Credentials
	Getenv func(string) string
	Flags  GatewayFlags
	Fetch  ModelFetcher // nil → FetchEnabledModels

	// ExecProbe runs a helper command and returns its stdout (codex model
	// catalog).
	ExecProbe func(binary string, args ...string) (string, error)
}

// TempDir is a host directory holding config the agent reads. Declaring it lets
// the dry-run report it and Cleanup remove it.
type TempDir struct {
	HostPath string
}

// LaunchPlan is a fully resolved agent invocation.
type LaunchPlan struct {
	PreArgs  []string          // inserted before user args (codex -c overrides)
	Env      map[string]string // exact env; empty-string values are preserved
	TempDirs []TempDir
	Warnings []string
	// Notes describe what a real run would do that this one did not. Only
	// --dry-run fills them in, and only printDryRun prints them.
	Notes   []string
	Cleanup func()
}

// AddCleanup chains a cleanup onto whatever the plan already has. Assigning
// Cleanup directly drops the previous one, which is how a temp directory gets
// left behind.
func (p *LaunchPlan) AddCleanup(fn func()) {
	if fn == nil {
		return
	}
	prev := p.Cleanup
	p.Cleanup = func() {
		fn()
		if prev != nil {
			prev()
		}
	}
}

// AgentDef describes one launchable coding agent.
type AgentDef struct {
	Name        string
	Binary      string
	Label       string
	InstallHint string
	AllowModels bool
	// FetchesModels reports whether Resolve reads the gateway catalogue, and
	// therefore whether --no-fetch-models does anything. claude speaks the
	// anthropic-native endpoint and resolves its model from env/defaults, so
	// advertising the flag for it promised a knob that did nothing.
	FetchesModels bool
	// RequestHeaders reports whether launch can add orq gateway headers to this
	// agent's model calls, and therefore whether --thread-id and --metadata are
	// launcher flags for it. copilot is left out: its BYOK provider sends no
	// custom headers (github/copilot-cli#3399), and a flag that silently does
	// nothing is worse than none.
	RequestHeaders bool
	// OwnThread reports that the agent already sends a conversation id the
	// gateway groups on (claude: x-claude-code-session-id, codex: its thread
	// id). Launch then generates no thread id for it, and sends
	// X-ORQ-THREAD-ID only for an explicit --thread-id. The others need one
	// generated, or every model call of a session is its own trace.
	OwnThread bool
	// MCPEnvAuth reports whether the agent's MCP config can reference
	// ORQ_API_KEY by name, and therefore whether --mcp-auth env is offered.
	MCPEnvAuth bool
	// HelpRoute and HelpModel name the router surface an agent speaks in
	// `orq launch <agent> --help`, for the two that are not on the shared
	// OpenAI-compatible one (claude, gemini). Build HelpRoute with helpRoute
	// rather than spelling the path out, so the help cannot go on naming a
	// route the agent no longer resolves.
	HelpRoute string
	HelpModel string
	Prompt    *PromptMapping
	Resolve   func(*AgentContext) (*LaunchPlan, error)
}

// helpRoute names a native API surface on the orq.ai AI Router, deriving the
// path from the gateway URL the agent actually resolves to.
func helpRoute(api, gatewayURL string) string {
	return fmt.Sprintf("the orq.ai AI Router, %s-native API (%s)", api, strings.TrimPrefix(gatewayURL, DefaultGatewayAPIBaseURL))
}

// Agents returns the registry, ordered for help output.
func Agents() []AgentDef {
	return []AgentDef{
		claudeAgent(),
		codexAgent(),
		opencodeAgent(),
		kiloAgent(),
		kimiAgent(),
		piAgent(),
		copilotAgent(),
		geminiAgent(),
	}
}

// FindAgent looks up an agent by name.
func FindAgent(name string) *AgentDef {
	for _, def := range Agents() {
		if def.Name == name {
			return &def
		}
	}
	return nil
}
