package launch

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
	Cleanup  func()
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
	Prompt        *PromptMapping
	Resolve       func(*AgentContext) (*LaunchPlan, error)
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
