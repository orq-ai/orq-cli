package commands

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

const (
	capGateway = "gateway"
	capMCP     = "mcp"
	capTracing = "tracing"
)

var connectCapabilities = []string{capGateway, capMCP, capTracing}

// partitionConnectArgs splits positional args into agent IDs and capability
// names. The registry owns the agent namespace, so the two sets cannot collide.
func partitionConnectArgs(args []string) (agents, caps []string, err error) {
	known := map[string]bool{}
	for _, id := range agentIDs() {
		known[id] = true
	}
	isCap := map[string]bool{}
	for _, c := range connectCapabilities {
		isCap[c] = true
	}
	seen := map[string]bool{}
	for _, a := range args {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		switch {
		case known[a]:
			agents = append(agents, a)
		case isCap[a]:
			caps = append(caps, a)
		default:
			return nil, nil, fmt.Errorf("%q is neither an agent (%s) nor a capability (%s)",
				a, strings.Join(agentIDs(), ", "), strings.Join(connectCapabilities, ", "))
		}
	}
	return agents, caps, nil
}

func detectedAgents() []string {
	var out []string
	for _, spec := range agentRegistry() {
		if spec.detect() {
			out = append(out, spec.ID)
		}
	}
	return out
}

func NewConnectCommand() *cobra.Command {
	opts := setupOptions{}
	dryRun := false

	cmd := &cobra.Command{
		Use:   "connect [agent...] [capability...]",
		Short: "Wire coding agents to orq permanently",
		Long: bartolocli.Markdown(`Registers orq with the coding agents on this machine. Capabilities: ` +
			"`gateway`" + ` (route the agent's model calls through orq), ` + "`mcp`" + ` (workspace ` +
			`read/write tools). No agents named means every detected agent; no capabilities ` +
			`means both.

Reuses the credential from ` + "`orq setup`" + ` — it never creates keys or edits your shell. ` +
			`Undo with ` + "`orq disconnect`" + `. For a single throwaway session, use ` + "`orq launch`" + `.

Agents: ` + strings.Join(agentIDs(), ", ") + `.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(cmd, &opts, args, dryRun)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key for this run instead of the saved one")
	f.BoolVar(&opts.global, "global", false, "Write agent config to the home directory instead of this project")
	f.BoolVar(&opts.local, "local", false, "Write agent config into this project even when inference would pick $HOME")
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked")
	f.BoolVar(&dryRun, "dry-run", false, "Show the files that would change, write nothing")
	return cmd
}

func runConnect(cmd *cobra.Command, opts *setupOptions, args []string, dryRun bool) error {
	if err := resolveScope(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)

	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	caps = dropUnavailableCaps(rep, caps)
	if len(caps) == 0 && capsWereOnlyTracing(args) {
		return nil
	}
	if len(caps) == 0 {
		caps = []string{capGateway, capMCP}
	}
	if len(agents) == 0 {
		agents = detectedAgents()
		if len(agents) == 0 {
			rep.info("no coding agents detected — name one: orq connect <%s>", strings.Join(agentIDs(), "|"))
			return nil
		}
	}
	opts.agents = agents
	opts.noMCP = !hasCap(caps, capMCP)
	opts.noGateway = !hasCap(caps, capGateway)

	if dryRun {
		return dryRunConnect(rep, opts, agents, caps)
	}

	state, client, err := resolveConnectAuth(cmd, rep, opts)
	if err != nil {
		return err
	}
	agentResults, err := instrumentAgents(rep, client, state, opts)
	if err != nil {
		return err
	}
	if !wantsHumanView(cmd) {
		if err := emit(map[string]any{"agents": agentResults}); err != nil {
			return err
		}
	}
	for _, a := range agentResults {
		if a.Error != "" {
			return errAgentFailed
		}
	}
	return nil
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// dropUnavailableCaps strips capabilities that parse but are not built yet, so
// the surface stays stable while they land.
func dropUnavailableCaps(rep *reporter, caps []string) []string {
	out := caps[:0]
	for _, c := range caps {
		if c == capTracing {
			rep.info("tracing is not available yet — see RES-1407")
			continue
		}
		out = append(out, c)
	}
	return out
}

func capsWereOnlyTracing(args []string) bool {
	sawTracing := false
	for _, a := range args {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case capTracing:
			sawTracing = true
		case capGateway, capMCP:
			return false
		}
	}
	return sawTracing
}

// dryRunConnect prints the files each selected capability would touch. Paths
// only, not content: the writers resolve content against the live catalogue.
func dryRunConnect(rep *reporter, opts *setupOptions, agents, caps []string) error {
	rep.info("dry run — files that would change, nothing written")
	for _, id := range agents {
		spec, ok := lookupAgent(id)
		if !ok {
			continue
		}
		if hasCap(caps, capMCP) {
			switch {
			case spec.writeMCP == nil:
				rep.info("%-8s mcp       no MCP support in this agent", id)
			default:
				if path, err := spec.mcpConfig(opts.global); err == nil && path != "" {
					rep.info("%-8s mcp       %s", id, tilde(path))
				}
			}
		}
		if hasCap(caps, capGateway) {
			switch {
			case spec.writeProvider == nil:
				rep.info("%-8s gateway   no gateway provider config for this agent", id)
			default:
				if path, err := spec.providerConfig(opts.global); err == nil && path != "" {
					rep.info("%-8s gateway   %s", id, tilde(path))
				}
			}
		}
	}
	return nil
}

// resolveConnectAuth is the credential path connect and setup share: a durable
// key must exist (never minted here), and it must belong to the workspace this
// run resolved.
func resolveConnectAuth(cmd *cobra.Command, rep *reporter, opts *setupOptions) (*authState, *auth.Client, error) {
	saved, savedWS := savedAPIKey()
	envKey := UserEnvAPIKey()
	if saved == "" && strings.TrimSpace(opts.apiKey) == "" && envKey == "" {
		return nil, nil, errors.New("no saved API key — run 'orq setup' once to create one, or pass --api-key")
	}
	// A saved key with no session must not fall into resolveAuth's login path:
	// this command reuses credentials, never creates them.
	if strings.TrimSpace(opts.apiKey) == "" && saved != "" {
		if session, _ := auth.ReadSession(); session == nil {
			rep.ok("api key (saved profile: %s)", auth.ActiveProfile())
			state := &authState{apiBase: apiBaseFromEnv()}
			state.useDurableKey(saved)
			return state, auth.NewClient(state.apiBase), nil
		}
	}
	state, err := resolveAuth(cmd.Context(), rep, opts)
	if err != nil {
		return nil, nil, err
	}
	if active := activeWorkspaceKey(state.session); saved != "" && keyWorkspaceMismatch(savedWS, active) {
		return nil, nil, fmt.Errorf("saved API key belongs to workspace %s, but the active workspace is %s — run 'orq setup --workspace %s' to create one for it", savedWS, active, active)
	}
	if state.suppliedKey == "" {
		switch {
		case saved != "":
			state.useDurableKey(saved)
		case envKey != "":
			state.useDurableKey(envKey)
		}
	}
	return state, auth.NewClient(state.apiBase), nil
}

func NewDisconnectCommand() *cobra.Command {
	opts := setupOptions{}

	cmd := &cobra.Command{
		Use:   "disconnect [agent...] [capability...]",
		Short: "Remove what orq connect wrote",
		Long: bartolocli.Markdown(`Removes orq's entries from agent configs: only the keys and tables ` +
			"`orq connect`" + ` (or ` + "`orq setup`" + `) wrote, never the rest of the file. No agents ` +
			`named means every detected agent; no capabilities means all of them.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDisconnect(cmd, &opts, args)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.global, "global", false, "Remove from the home-directory config instead of this project")
	f.BoolVar(&opts.local, "local", false, "Remove from this project's config even when inference would pick $HOME")
	return cmd
}

func runDisconnect(cmd *cobra.Command, opts *setupOptions, args []string) error {
	if err := resolveScope(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)

	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	if len(caps) == 0 {
		caps = []string{capGateway, capMCP}
	}
	if len(agents) == 0 {
		agents = detectedAgents()
	}

	type row struct {
		Agent   string `json:"agent"`
		Removed string `json:"removed,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	var rows []row
	failed := false
	for _, id := range agents {
		spec, ok := lookupAgent(id)
		if !ok {
			continue
		}
		r := row{Agent: id}
		var removedFrom []string
		remove := func(cap string, resolve func(bool) (string, error), remover func(string) (bool, error)) {
			if resolve == nil || remover == nil {
				return
			}
			for _, path := range bothScopePaths(resolve) {
				removed, err := remover(path)
				switch {
				case err != nil:
					rep.fail("%-8s %-9s %v", id, cap, err)
					r.Error = err.Error()
					failed = true
				case removed:
					rep.ok("%-8s %-9s removed from %s", id, cap, tilde(path))
					removedFrom = append(removedFrom, cap)
				}
			}
		}
		if hasCap(caps, capMCP) {
			remove(capMCP, spec.mcpConfig, spec.removeMCP)
		}
		if hasCap(caps, capGateway) {
			remove(capGateway, spec.providerConfig, spec.removeProvider)
		}
		if len(removedFrom) == 0 && r.Error == "" {
			rep.info("%-8s nothing to remove", id)
		}
		r.Removed = strings.Join(removedFrom, "+")
		rows = append(rows, r)
	}

	if !wantsHumanView(cmd) {
		if err := emit(map[string]any{"agents": rows}); err != nil {
			return err
		}
	}
	if failed {
		return errAgentFailed
	}
	return nil
}

// bothScopePaths resolves the project and global paths, deduplicated: many
// agents resolve both scopes to the same file.
func bothScopePaths(resolve func(bool) (string, error)) []string {
	seen := map[string]bool{}
	var out []string
	for _, global := range []bool{false, true} {
		if path, err := resolve(global); err == nil && path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}
