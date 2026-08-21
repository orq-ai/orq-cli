package commands

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

const (
	capGateway = "gateway"
	capTracing = "tracing"
)

var connectCapabilities = []string{capGateway, capTracing}

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

// detectedAgents lists the agents on this machine that connect can act on.
// claude is installed on plenty of machines and has no gateway provider config
// — it reads the endpoint from its environment — so offering it, or reporting
// it as "not wired", promises a wire that cannot exist.
func detectedAgents() []string {
	var out []string
	for _, spec := range agentRegistry() {
		if spec.writeProvider != nil && spec.detect() {
			out = append(out, spec.ID)
		}
	}
	return out
}

func NewConnectCommand() *cobra.Command {
	opts := setupOptions{}
	dryRun := false
	status := false

	cmd := &cobra.Command{
		Use:   "connect [agent...] [capability...]",
		Short: "Wire coding agents to orq permanently",
		Long: bartolocli.Markdown(`Registers orq with the coding agents on this machine, routing the ` +
			`agent's model calls through the orq gateway. No agents named means every detected agent.

Reuses the credential from ` + "`orq setup`" + ` — it never creates keys or edits your shell. ` +
			`Undo with ` + "`orq disconnect`" + `. For a single throwaway session, use ` + "`orq launch`" + `.

Agents: ` + strings.Join(agentIDs(), ", ") + `.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if status {
				return runConnectStatus(&opts, args)
			}
			return runConnect(cmd, &opts, args, dryRun)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.apiKey, "api-key", "", "Use this API key for this run instead of the saved one")
	f.BoolVarP(&opts.yes, "yes", "y", false, "Answer yes to every confirmation instead of being asked")
	f.BoolVar(&dryRun, "dry-run", false, "Show the files that would change, write nothing")
	f.BoolVar(&status, "status", false, "Show what is wired on this machine, change nothing")
	return cmd
}

// runConnectStatus is the read-only view of on-disk wiring. It never prompts,
// never authenticates, always exits 0.
func runConnectStatus(opts *setupOptions, args []string) error {
	if err := resolveScope(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)
	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	if len(caps) == 0 {
		caps = []string{capGateway}
	}
	if len(agents) == 0 {
		agents = detectedAgents()
	}
	wired := wiredTargets(agents, caps, opts)
	if len(wired) == 0 {
		rep.info("nothing wired on this machine")
	}
	for _, w := range wired {
		rep.info("%-8s %-9s %s", w.agent, w.capability, tilde(w.path))
	}
	isWired := map[string]bool{}
	for _, w := range wired {
		isWired[w.agent] = true
	}
	var unwired []string
	for _, id := range detectedAgents() {
		if !isWired[id] {
			unwired = append(unwired, id)
		}
	}
	if len(unwired) > 0 {
		rep.info("detected but not wired: %s", strings.Join(unwired, ", "))
	}
	return nil
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
		caps = []string{capGateway}
	}
	// Naming an agent is the intent; leaving it bare is not, so ask rather than
	// act on every agent the machine happens to have.
	if len(agents) == 0 {
		detected := detectedAgents()
		if len(detected) == 0 {
			rep.info("no coding agents detected — name one: orq connect <%s>", strings.Join(agentIDs(), "|"))
			return nil
		}
		switch {
		case opts.yes || dryRun:
			agents = detected
		case opts.noInput:
			return fmt.Errorf("name the agents to connect (%s), or pass --yes to connect all detected",
				strings.Join(detected, ", "))
		default:
			// The ask must not surprise anyone after two selections.
			if saved, _ := savedAPIKey(); saved == "" && strings.TrimSpace(opts.apiKey) == "" && UserEnvAPIKey() == "" {
				rep.info("no API key on this machine — you'll be asked to set one up before wiring")
			}
			agents, err = promptForAgents(rep)
			if err != nil {
				return fmt.Errorf("cancelled at the agent selection: %w", err)
			}
			if len(agents) == 0 {
				rep.info("nothing selected")
				return nil
			}
		}
	}
	return connectSelected(cmd, rep, opts, agents, caps, dryRun)
}

func connectSelected(cmd *cobra.Command, rep *reporter, opts *setupOptions, agents, caps []string, dryRun bool) error {
	opts.agents = agents
	opts.noGateway = !hasCap(caps, capGateway)

	if dryRun {
		return dryRunConnect(rep, opts, agents, caps)
	}
	if len(caps) == 0 {
		return nil
	}

	state, client, err := resolveConnectAuth(cmd, rep, opts)
	if errors.Is(err, errLoginDeclined) {
		rep.info("nothing wired — run 'orq setup' when ready, or pass --api-key")
		return nil
	}
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
		case capGateway:
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
		if hasCap(caps, capGateway) {
			switch {
			case spec.writeProvider == nil:
				rep.info("%-8s gateway   no gateway provider config for this agent", id)
			default:
				if path, err := spec.providerConfig(false); err == nil && path != "" {
					rep.info("%-8s gateway   %s", id, tilde(path))
				}
			}
		}
	}
	return nil
}

// errLoginDeclined marks the user's "no" to the inline login offer: not a
// fault, so the caller turns it into a hint and exit 0.
var errLoginDeclined = errors.New("login declined")

// resolveConnectAuth is connect's credential path. It reuses whatever exists —
// saved key, ORQ_API_KEY, --api-key, session — and when nothing does, an
// interactive run offers to log in and mint one right here, so the selection
// already made is never wasted. Non-interactive runs never create credentials.
func resolveConnectAuth(cmd *cobra.Command, rep *reporter, opts *setupOptions) (*authState, *auth.Client, error) {
	saved, savedWS := savedAPIKey()
	envKey := UserEnvAPIKey()
	if saved == "" && strings.TrimSpace(opts.apiKey) == "" && envKey == "" {
		if opts.noInput || opts.yes {
			return nil, nil, errors.New("no saved API key — run 'orq setup' once to create one, or pass --api-key")
		}
		message := "No API key on this machine — log in now?"
		if session, _ := auth.ReadSession(); session != nil {
			message = "No API key on this machine — create one now?"
		}
		if !opts.confirm(message, true) {
			return nil, nil, errLoginDeclined
		}
	}
	// A saved key with no session must not fall into resolveAuth's login path:
	// the credential exists, so nothing may prompt for another.
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
	client := auth.NewClient(state.apiBase)
	if active := activeWorkspaceKey(state.session); saved != "" && keyWorkspaceMismatch(savedWS, active) {
		return nil, nil, fmt.Errorf("saved API key belongs to workspace %s, but the active workspace is %s — run 'orq setup --workspace %s' to create one for it", savedWS, active, active)
	}
	if state.suppliedKey == "" {
		switch {
		case saved != "":
			state.useDurableKey(saved)
		case envKey != "":
			state.useDurableKey(envKey)
		default:
			// The user accepted the offer above: mint and persist the durable
			// key the wiring writes against, exactly as setup would. No shell
			// profile prompt here — doctor names the source line if it's missing.
			token, _, err := ensureDurableKey(rep, client, state, opts)
			if err != nil {
				return nil, nil, err
			}
			state.useDurableKey(token)
			if path, err := writeShellEnvFile(token); err != nil {
				rep.warn("could not write the shell env file: %v", err)
			} else {
				rep.ok("env file    %s  → exports ORQ_API_KEY", tilde(path))
			}
		}
	}
	return state, client, nil
}

func NewDisconnectCommand() *cobra.Command {
	opts := setupOptions{}
	dryRun := false

	cmd := &cobra.Command{
		Use:   "disconnect [agent...] [capability...]",
		Short: "Remove what orq connect wrote",
		Long: bartolocli.Markdown(`Edits your agents' config files: it removes the keys and tables ` +
			"`orq connect`" + ` (or ` + "`orq setup`" + `) wrote, and nothing else.

Naming agents removes from those. A bare ` + "`orq disconnect`" + ` targets every detected agent ` +
			`and lists what it would remove before asking. ` + "`--dry-run`" + ` shows the list and stops.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDisconnect(cmd, &opts, args, dryRun)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.yes, "yes", "y", false, "Remove without confirming")
	f.BoolVar(&dryRun, "dry-run", false, "Show what would be removed, remove nothing")
	return cmd
}

func runDisconnect(cmd *cobra.Command, opts *setupOptions, args []string, dryRun bool) error {
	if err := resolveScope(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)

	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	if len(caps) == 0 {
		caps = []string{capGateway}
	}
	namedAgents := len(agents) > 0
	if !namedAgents {
		agents = detectedAgents()
	}

	// Removal is the one destructive verb, so it says what it will remove
	// before it does. Naming agents is intent; a bare run is not.
	wired := wiredTargets(agents, caps, opts)
	if len(wired) == 0 {
		rep.info("nothing wired on this machine")
		return nil
	}
	if dryRun {
		rep.info("dry run — what would be removed, nothing changed")
		for _, w := range wired {
			rep.info("%-8s %-9s %s", w.agent, w.capability, tilde(w.path))
		}
		return nil
	}
	if !opts.yes && !namedAgents {
		rep.warn("about to remove orq from %d config file(s):", len(wired))
		for _, w := range wired {
			rep.warn("  %-8s %-9s %s", w.agent, w.capability, tilde(w.path))
		}
		if opts.noInput {
			return errors.New("refusing to remove from every detected agent without confirmation; name the agents, or pass --yes")
		}
		if !opts.confirm("Remove these?", false) {
			rep.info("nothing removed")
			return nil
		}
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
		if hasCap(caps, capGateway) {
			remove(capGateway, spec.providerConfig, spec.removeProvider)
		}
		r.Removed = strings.Join(removedFrom, "+")
		rows = append(rows, r)
	}

	reportBackups(rep, wired)
	reportCredentialSurvives(rep)

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

// wiredTarget is one agent's capability and the file holding it.
type wiredTarget struct{ agent, capability, path string }

// wiredTargets is what disconnect would act on: only capabilities actually
// present on disk, so the preview never lists a file it will not touch.
func wiredTargets(agents, caps []string, opts *setupOptions) []wiredTarget {
	var out []wiredTarget
	for _, id := range agents {
		spec, ok := lookupAgent(id)
		if !ok {
			continue
		}
		if hasCap(caps, capGateway) {
			if path, ok := wiredPath(spec.providerConfig, spec.providerPresent); ok {
				out = append(out, wiredTarget{id, capGateway, path})
			}
		}
	}
	return out
}

// reportCredentialSurvives corrects the mental model disconnect otherwise
// leaves. Removing the config removes the wire, not the credential: the key is
// still valid, still on the machine, and still exported to anything that reads
// the environment. Saying nothing here reads as "orq is off this machine".
func reportCredentialSurvives(rep *reporter) {
	if saved, _ := savedAPIKey(); saved == "" {
		return
	}
	rep.info("the API key is untouched — still valid, and still saved for the next 'orq connect'")
	if id := savedGatewayKeyID(); id != "" {
		rep.info("  to revoke it as well: orq api-keys delete %s", id)
	}
}

// reportBackups names the copies writeJSONConfig took, once. A safety net the
// user cannot see is not one.
func reportBackups(rep *reporter, wired []wiredTarget) {
	seen := map[string]bool{}
	var backups []string
	for _, w := range wired {
		backup := w.path + ".orq-bak"
		if seen[backup] {
			continue
		}
		seen[backup] = true
		if _, err := os.Stat(backup); err == nil {
			backups = append(backups, tilde(backup))
		}
	}
	if len(backups) > 0 {
		rep.info("a copy from before orq first wrote is at: %s", strings.Join(backups, ", "))
	}
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

// setupConnectStep is setup's step 3: a consent gate, then connect's
// interactive selection. Non-interactive runs wire nothing and point at
// `orq connect`, which composes with setup in CI.
func setupConnectStep(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) ([]agentResult, error) {
	if opts.noInput {
		rep.info("Next: orq connect — wires the coding agents on this machine")
		return nil, nil
	}
	rep.note("Connecting routes an agent's model calls through the orq gateway.")
	if !opts.confirm("Connect your coding agents now?", true) {
		rep.info("Next: orq connect — anytime, same credential")
		return nil, nil
	}
	agents, err := promptForAgents(rep)
	if err != nil {
		return nil, fmt.Errorf("setup cancelled at the agent selection: %w", err)
	}
	if len(agents) == 0 {
		rep.info("nothing selected — orq connect wires an agent anytime")
		return nil, nil
	}
	opts.agents = agents
	opts.noGateway = false
	return instrumentAgents(rep, client, state, opts)
}
