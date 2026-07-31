package launch

// AgentContext is everything an agent's Resolve needs, with injectable seams
// for tests (Getenv, Fetch, ExecProbe).
type AgentContext struct {
	Creds  *Credentials
	Getenv func(string) string
	Flags  GatewayFlags
	Fetch  ModelFetcher // nil → FetchEnabledModels

	// ExecProbe runs a helper command and returns its stdout (codex model
	// catalog). In sandbox mode it is docker-exec-aware.
	ExecProbe func(binary string, args ...string) (string, error)
}

// TempDir is a host directory holding config the agent reads. EnvVar (if set)
// points the agent at it; in sandbox mode the dir is docker-cp'd to MountAt.
type TempDir struct {
	HostPath string
	EnvVar   string
	MountAt  string
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
	NpmPackage  string // sandbox image install target
	AllowModels bool
	Prompt      *PromptMapping
	Resolve     func(*AgentContext) (*LaunchPlan, error)
}

// Agents returns the registry, ordered for help output.
func Agents() []AgentDef {
	return []AgentDef{
		claudeAgent(),
		codexAgent(),
		opencodeAgent(),
		kiloAgent(),
		kimiAgent(),
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
