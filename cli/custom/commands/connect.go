package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/skills"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

const (
	capGateway = "gateway"
	capTracing = "tracing"
	capSkills  = "skills"
)

var connectCapabilities = []string{capGateway, capTracing, capSkills}

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

// availableCapabilities is what a bare `orq connect`, `--status` or
// `orq disconnect` covers: every capability that is actually built. Naming no
// capability asks about the machine, not about the gateway — a bare
// `--status` that reports only the gateway makes a false statement about a
// machine carrying installed skills, and a bare `disconnect` that removes only
// the gateway cannot undo what a bare `connect` wrote.
//
// Tracing is excluded for the same reason defaultCapabilities excludes it:
// dropUnavailableCaps would strip it and print "not available yet" on every
// bare invocation.
func availableCapabilities() []string { return []string{capGateway, capSkills} }

func capAvailable(c string) bool { return slices.Contains(availableCapabilities(), c) }

// detectedAgents lists the agents on this machine that connect can act on for
// the gateway. claude is installed on plenty of machines and has no gateway
// provider config — it reads the endpoint from its environment — so offering
// it, or reporting it as "not wired", promises a wire that cannot exist.
func detectedAgents() []string {
	var out []string
	for _, spec := range agentRegistry() {
		if spec.writeProvider != nil && spec.detect() {
			out = append(out, spec.ID)
		}
	}
	return out
}

// agentsToConnect is the agent set a bare command writes to. "Which agents can
// receive this?" is a different question per capability: the gateway needs a
// provider config to write, skills need a directory the agent reads. Filtering
// both on writeProvider left claude — the one agent that receives skills and
// has no provider config, and the most common machine there is — out of every
// bare skills run.
func agentsToConnect(caps []string) []string {
	wantSkills := hasCap(caps, capSkills)
	var out []string
	for _, spec := range agentRegistry() {
		if !spec.detect() {
			continue
		}
		if spec.writeProvider != nil || (wantSkills && skills.Receives(spec.ID)) {
			out = append(out, spec.ID)
		}
	}
	return out
}

// agentsToInspect is the agent set a bare `--status` or `disconnect` acts on:
// what agentsToConnect would write to, plus what the manifest says was already
// written. Detection answers "what can I add to"; only the manifest answers
// "what have I got", and an agent that received skills and was then
// uninstalled must stay removable rather than becoming permanently orphaned.
//
// The union is deliberately one-directional: the manifest never widens the set
// connect writes to, so a recorded-but-uninstalled agent can be inspected and
// removed without a bare `orq connect` reviving config for a tool the user no
// longer has.
func agentsToInspect(caps []string) []string {
	out := agentsToConnect(caps)
	if !hasCap(caps, capSkills) {
		return out
	}
	seen := map[string]bool{}
	for _, id := range out {
		seen[id] = true
	}
	for _, id := range manifestSkillAgents() {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// manifestSkillAgents lists the agents the manifest records non-session skill
// links for. A link with an empty Agent is the shared agents-spec directory,
// which no single agent owns; it is attributed to every shared reader, since
// naming any one of them is what reaches it (the same membership rule Remove
// and reportMissingSkillLinks apply).
func manifestSkillAgents() []string {
	m, err := skills.LoadManifest()
	if err != nil || m == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if l.Agent != "" {
			add(l.Agent)
			continue
		}
		for _, id := range agentIDs() {
			if skills.SharedReader(id) {
				add(id)
			}
		}
	}
	return out
}

// capsNeedCredential reports whether any requested capability talks to orq.
// Skills are embedded in this binary and unpack onto the local filesystem, so
// asking for a credential before installing them stands a network prompt in
// front of an operation that is pure file I/O — and makes the offline install
// the spec promises impossible.
// credentialFreeCaps is what is left of a request once the credential is known
// to be unavailable: everything this binary can still do from its own contents.
func credentialFreeCaps(caps []string) []string {
	var out []string
	for _, c := range caps {
		if c == capSkills {
			out = append(out, c)
		}
	}
	return out
}

func capsNeedCredential(caps []string) bool {
	for _, c := range caps {
		if c != capSkills {
			return true
		}
	}
	return false
}

// validateCapabilities checks `orq setup --capability` values against the same
// grammar `orq connect` enforces on its positional arguments. It reuses
// partitionConnectArgs rather than repeating the capability list, so the two
// entry points cannot drift: `orq connect claude bogus` errored while
// `orq setup --capability bogus` accepted the value and then silently wired
// nothing at all.
//
// An agent name is rejected too: the flag names capabilities, and `--capability
// claude` would otherwise parse into an empty capability set and connect
// nothing, which is the same silent no-op by another route.
func validateCapabilities(caps []string) ([]string, error) {
	agents, out, err := partitionConnectArgs(caps)
	if err != nil {
		return nil, err
	}
	if len(agents) > 0 {
		return nil, fmt.Errorf("--capability takes a capability (%s), not an agent: %s",
			strings.Join(connectCapabilities, ", "), strings.Join(agents, ", "))
	}
	return out, nil
}

// nothingWired keeps the claim as narrow as the question: naming agents asks
// about those agents, so answering "nothing wired on this machine" overstates
// what was looked at.
func nothingWired(named bool, agents []string) string {
	if !named {
		return "nothing wired on this machine"
	}
	return "nothing wired for " + strings.Join(agents, ", ")
}

// reportUnwirableAgents warns about the named agents that have no provider
// config, so a request never says nothing and never falls through to
// "detected but not wired" for a gateway wire that cannot exist. The same
// message runConnect prints, for the same reason.
//
// It only drops the agent from the returned set when gateway is the sole
// requested capability: a gateway-only request has nothing left to do for
// that agent, so dropping it is the whole story. A request combining gateway
// with anything else — skills, most concretely — must keep the agent in
// play; an agent with no provider config can still carry skills, and every
// other capability's own per-agent logic already no-ops cleanly on a nil
// provider config (see wiredPath). Dropping the agent there would silently
// discard those other capabilities along with the gateway leg.
func reportUnwirableAgents(rep *reporter, agents, caps []string) []string {
	if !hasCap(caps, capGateway) {
		return agents
	}
	onlyGateway := len(caps) == 1
	var out []string
	for _, id := range agents {
		spec, ok := lookupAgent(id)
		if ok && spec.writeProvider == nil {
			rep.info("%-8s no gateway provider config for this agent — nothing to configure", id)
			if onlyGateway {
				continue
			}
		}
		out = append(out, id)
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
		Long: bartolocli.Markdown(`Registers orq with the coding agents on this machine. No agents ` +
			`named means every detected agent; no capability named means every capability below.

Capabilities:

  gateway   Routes the agent's model calls through the orq AI Gateway, by writing a
            provider entry into the agent's own config file. Needs a credential.
  skills    Installs the orq skills — the instructions that teach an agent how to
            use orq — into the skills directory that agent reads (~/.claude/skills
            and the equivalents). They ship inside this binary, so this needs no
            credential and works offline.
  tracing   Parses, but is not available yet.

` + "`orq skills`" + ` is a different command and a different noun: it manages skill entities on the
orq platform. The ` + "`skills`" + ` capability here installs files into your agents' own skills
directories.

Reuses the credential from ` + "`orq setup`" + ` when there is one. With no credential at all, an ` +
			`interactive run offers to log in and mint one. Undo with ` + "`orq disconnect`" + `. ` +
			`For a single throwaway session, use ` + "`orq launch`" + `.

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

// runConnectStatus is the read-only view of on-disk wiring. It never prompts and
// never authenticates; it exits non-zero only on an argument it cannot parse.
func runConnectStatus(opts *setupOptions, args []string) error {
	if err := applyGlobalFlags(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)
	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	// Both filters connect and disconnect apply. Without the first,
	// `--status tracing` left caps as ["tracing"] and reported "nothing wired"
	// on a machine that plainly was; without the second, a named claude was
	// reported unwired when no wire can exist for it.
	caps = dropUnavailableCaps(rep, caps)
	if len(caps) == 0 && capsWereAllUnavailable(args) {
		return nil
	}
	if len(caps) == 0 {
		caps = availableCapabilities()
	}
	named := len(agents) > 0
	if !named {
		agents = agentsToInspect(caps)
	} else if agents = reportUnwirableAgents(rep, agents, caps); len(agents) == 0 {
		return nil
	}
	wired := wiredTargets(agents, caps, opts)
	if len(wired) == 0 {
		rep.info(nothingWired(named, agents))
	}
	byAgent := map[string][]wiredTarget{}
	var order []string
	for _, w := range wired {
		// The shared agents-spec directory has no single owner, so it is
		// reported under its own heading rather than attributed to one agent.
		key := w.agent
		if key == "" {
			key = "shared"
		}
		if _, seen := byAgent[key]; !seen {
			order = append(order, key)
		}
		byAgent[key] = append(byAgent[key], w)
	}
	for _, agent := range order {
		rep.info("%s", agent)
		for _, w := range byAgent[agent] {
			rep.info("  %-9s %s", w.capability, tilde(w.path))
		}
	}
	// Scoped like the rest of this function: `--status kimi` naming only
	// kimi, or a run that never asked about skills at all, must not surface
	// another agent's or another capability's broken links.
	if hasCap(caps, capSkills) {
		reportMissingSkillLinks(rep, agents)
	}
	isWired := map[string]bool{}
	for _, w := range wired {
		isWired[w.agent] = true
	}
	// Scoped to what was asked about: `--status codex` reporting on kimi is an
	// answer to a question nobody asked.
	var unwired []string
	for _, id := range agents {
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
	if err := applyGlobalFlags(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)

	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	caps = dropUnavailableCaps(rep, caps)
	if len(caps) == 0 && capsWereAllUnavailable(args) {
		return nil
	}
	if len(caps) == 0 {
		caps = availableCapabilities()
	}
	// Naming an agent is the intent; leaving it bare is not, so ask rather than
	// act on every agent the machine happens to have.
	if len(agents) == 0 {
		detected := agentsToConnect(caps)
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
			// The ask must not surprise anyone after two selections. Only
			// when something in this run actually needs a credential:
			// warning a skills-only run about a key it will never ask for
			// is the same false network dependency, stated in advance.
			if saved, _ := savedAPIKey(); capsNeedCredential(caps) && saved == "" && strings.TrimSpace(opts.apiKey) == "" && UserEnvAPIKey() == "" {
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
	opts.caps = caps
	opts.noGateway = !hasCap(caps, capGateway)

	if dryRun {
		return dryRunConnect(rep, opts, agents, caps)
	}
	if len(caps) == 0 {
		return nil
	}

	// The credential gate belongs to the capabilities that need one. Skills
	// unpack from this binary onto the local filesystem, so a skills-only run
	// must never demand a key, prompt for a login, or fail without one.
	var state *authState
	var client *auth.Client
	if capsNeedCredential(caps) {
		var err error
		state, client, err = resolveConnectAuth(cmd, rep, opts)
		switch {
		case err != nil && hasCap(caps, capSkills):
			// A missing credential costs this run the gateway, not everything
			// it could still do. Losing the offline skills install because the
			// gateway leg — which may have nothing to do anyway, claude has no
			// provider config — could not find a key is the whole credential
			// coupling this branch set out to remove, reintroduced one level
			// up by the bare capability set.
			if errors.Is(err, errLoginDeclined) {
				rep.info("gateway skipped: no credential — run 'orq setup' when ready, or pass --api-key")
			} else {
				rep.warn("gateway skipped: %v", err)
			}
			caps = credentialFreeCaps(caps)
			opts.caps = caps
			opts.noGateway = true
			state, client = nil, nil
		case errors.Is(err, errLoginDeclined):
			rep.info("nothing wired — run 'orq setup' when ready, or pass --api-key")
			return nil
		case err != nil:
			return err
		}
	}
	agentResults, err := instrumentAgents(rep, client, state, opts)
	if err != nil {
		return err
	}
	if !wantsHumanView(cmd) {
		payload := map[string]any{"coding_agents": agentResults}
		// The skills leg reports itself through rep.ok, which quiet mode (and
		// therefore every non-TTY run) suppresses. Without this a script that
		// just installed fourteen skills into the user's home read
		// `coding_agents: null` and had no way to observe the capability at
		// all.
		if hasCap(caps, capSkills) {
			payload[capSkills] = skillsPayload(agents)
		}
		if err := emit(payload); err != nil {
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

// skillsPayload is the machine-readable view of an install: the directories
// that received the set, and how many skills the set holds.
func skillsPayload(agents []string) map[string]any {
	targets := skillTargetsFor(agents)
	dirs := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		entry := map[string]any{"path": t.Dir}
		if t.Agent != "" {
			entry["agent"] = t.Agent
		}
		dirs = append(dirs, entry)
	}
	payload := map[string]any{"directories": dirs}
	if names, err := skills.Names(); err == nil {
		payload["count"] = len(names)
	}
	return payload
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
		if !capAvailable(c) {
			rep.info("%s is not available yet", c)
			continue
		}
		out = append(out, c)
	}
	return out
}

func capsWereAllUnavailable(args []string) bool {
	saw := false
	for _, a := range args {
		c := strings.ToLower(strings.TrimSpace(a))
		if !slices.Contains(connectCapabilities, c) {
			continue
		}
		if capAvailable(c) {
			return false
		}
		saw = true
	}
	return saw
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
	if hasCap(caps, capSkills) {
		for _, target := range skillTargetsFor(agents) {
			rep.info("%-8s skills    %s", target.Agent, tilde(target.Dir))
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
			rep.ok("using your saved key")
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
		Long: bartolocli.Markdown(`Removes what ` + "`orq connect`" + ` (or ` + "`orq setup`" + `) ` +
			`wrote, and nothing else: the ` + "`gateway`" + ` provider entries in your agents' own ` +
			`config files, and the ` + "`skills`" + ` this CLI installed into your agents' skills ` +
			`directories. Only paths this CLI recorded are ever touched — a skill you installed ` +
			`yourself, or one of ours you have since replaced, is reported and left alone.

` + "`orq skills`" + `, the platform skill entities, is a different noun and is not affected.

Naming agents removes from those; naming a capability (` + strings.Join(availableCapabilities(), ", ") +
			`) removes only that one. A bare ` + "`orq disconnect`" + ` targets every detected agent ` +
			`and every capability, and lists what it would remove before asking. ` + "`--dry-run`" +
			` shows the list and stops.`),
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
	if err := applyGlobalFlags(opts); err != nil {
		return err
	}
	rep := newReporter(opts.noInput)

	agents, caps, err := partitionConnectArgs(args)
	if err != nil {
		return err
	}
	// Same filter connect applies: without it `orq disconnect tracing` left caps
	// as ["tracing"], found no gateway target, and reported "nothing wired" on a
	// machine that plainly was.
	caps = dropUnavailableCaps(rep, caps)
	if len(caps) == 0 && capsWereAllUnavailable(args) {
		return nil
	}
	if len(caps) == 0 {
		caps = availableCapabilities()
	}
	namedAgents := len(agents) > 0
	if !namedAgents {
		agents = agentsToInspect(caps)
	} else if agents = reportUnwirableAgents(rep, agents, caps); len(agents) == 0 {
		return nil
	}

	// Removal is the one destructive verb, so it says what it will remove
	// before it does. Naming agents is intent; a bare run is not.
	wired := wiredTargets(agents, caps, opts)
	if len(wired) == 0 {
		rep.info(nothingWired(namedAgents, agents))
		return nil
	}
	if dryRun {
		rep.info("dry run — what would be removed, nothing changed")
		for _, w := range wired {
			rep.info("%-8s %-9s %s", w.agent, w.capability, tilde(w.path))
		}
		return nil
	}
	previewed := false
	if !opts.yes && !namedAgents {
		previewed = true
		rep.warn("about to remove orq from:")
		for _, w := range wired {
			rep.warn("  %s", tilde(w.path))
		}
		if opts.noInput {
			return errors.New("refusing to remove from every detected agent without confirmation; name the agents, or pass --yes")
		}
		if !opts.confirm("Remove these?", false) {
			rep.info("nothing removed")
			return nil
		}
	}

	rows, skillsRemoved, failed := removeWiring(rep, agents, caps, previewed)
	reportCredentialSurvives(rep)

	if !wantsHumanView(cmd) {
		payload := map[string]any{"coding_agents": rows}
		// Same reason as connect's: without this the removal of a whole skill
		// set is invisible to anything reading the output.
		if hasCap(caps, capSkills) {
			payload[capSkills] = map[string]any{"removed": len(skillsRemoved)}
		}
		// The terminal gets this as an advisory line; a script needs it too, or
		// --json reads as a clean removal.
		if saved, _ := savedAPIKey(); saved != "" {
			retained := map[string]any{"retained": true}
			if id := savedGatewayKeyID(); id != "" {
				retained["key_id"] = id
			}
			payload["api_key"] = retained
		}
		if err := emit(payload); err != nil {
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
	if hasCap(caps, capSkills) {
		m, err := skills.LoadManifest()
		if err == nil && m != nil {
			seen := map[string]bool{}
			wanted := map[string]bool{}
			for _, id := range agents {
				wanted[id] = true
			}
			for _, l := range m.Links {
				if l.Session {
					continue
				}
				// An empty agent is the shared directory, which serves any
				// selected agent that reads it.
				if l.Agent != "" && !wanted[l.Agent] {
					continue
				}
				dir := filepath.Dir(l.Path)
				if seen[dir] {
					continue
				}
				seen[dir] = true
				out = append(out, wiredTarget{l.Agent, capSkills, dir})
			}
		}
	}
	return out
}

// reportMissingSkillLinks warns about manifest entries whose path is gone,
// scoped to the agents actually asked about — the same membership rule
// skills.Remove applies to links whose Agent is empty (the shared
// agents-spec directory): they belong to the request whenever any named
// agent is a shared reader.
//
// Session-scoped links are excluded: they are created and destroyed by a
// live `orq launch` and their absence between sessions is not breakage.
//
// Missing entries are collapsed per directory rather than printed one per
// file: deleting a whole skills directory recorded a dozen links, and a
// dozen identical warnings each pointing at the same remedy tell the user
// nothing a single line with a count would not. A directory with exactly one
// missing entry keeps the original, more specific phrasing — a count of one
// reads worse than naming the thing that is actually missing.
func reportMissingSkillLinks(rep *reporter, agents []string) {
	m, err := skills.LoadManifest()
	if err != nil || m == nil {
		return
	}
	wanted := map[string]bool{}
	sharedWanted := false
	for _, id := range agents {
		wanted[id] = true
		sharedWanted = sharedWanted || skills.SharedReader(id)
	}
	missingByDir := map[string][]string{}
	var dirOrder []string
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if l.Agent == "" {
			if !sharedWanted {
				continue
			}
		} else if !wanted[l.Agent] {
			continue
		}
		if _, statErr := os.Lstat(l.Path); statErr == nil {
			continue
		}
		dir := filepath.Dir(l.Path)
		if _, seen := missingByDir[dir]; !seen {
			dirOrder = append(dirOrder, dir)
		}
		missingByDir[dir] = append(missingByDir[dir], l.Path)
	}
	for _, dir := range dirOrder {
		missing := missingByDir[dir]
		if len(missing) == 1 {
			rep.warn("skills   %s is recorded but missing — run 'orq connect skills' to restore it", tilde(missing[0]))
			continue
		}
		rep.warn("skills   %s: %d recorded skills are missing — run 'orq connect skills' to restore them", tilde(dir), len(missing))
	}
}

// disconnectRow is one agent's removal outcome. Removed is a list, not a joined
// string: joining forces a caller to split on a separator we invented.
type disconnectRow struct {
	Agent   string   `json:"agent"`
	Removed []string `json:"removed,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// removeWiring is the removal itself, with no prompting, reporting of the
// surviving credential, or payload emission around it. Split out so `orq auth
// logout` can offer the same removal without inheriting disconnect's own
// confirmation or its --json body.
//
// pathShown says a preview already listed the files, so the result names the
// agent instead of repeating the path the user just read and approved.
func removeWiring(rep *reporter, agents, caps []string, pathShown bool) (rows []disconnectRow, skillsRemoved []string, failed bool) {
	for _, id := range agents {
		spec, ok := lookupAgent(id)
		if !ok {
			continue
		}
		r := disconnectRow{Agent: id}
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
					where := tilde(path)
					if pathShown {
						where = id
					}
					rep.ok("orq removed from %s", where)
					removedFrom = append(removedFrom, cap)
				}
			}
		}
		if hasCap(caps, capGateway) {
			remove(capGateway, spec.providerConfig, spec.removeProvider)
		}
		r.Removed = removedFrom
		rows = append(rows, r)
	}
	if hasCap(caps, capSkills) {
		// skills.Remove always returns a non-nil *Result, even on error, so
		// res.Skipped below is safe to range over unconditionally.
		res, err := skills.Remove(agents)
		switch {
		case err != nil:
			rep.fail("%-8s %-9s %v", "", capSkills, err)
			failed = true
		case len(res.Removed) > 0:
			rep.ok("orq skills removed (%d entries)", len(res.Removed))
		}
		skillsRemoved = res.Removed
		for _, path := range res.Skipped {
			rep.warn("%s is no longer ours — left alone", tilde(path))
		}
	}
	return rows, skillsRemoved, failed
}

// disconnectOnLogout offers to remove orq from the agents this machine wired.
// Logout otherwise leaves kimi's config holding the key literally, and today
// only prints a line telling the user to run disconnect themselves.
//
// Defaults to no, and is skipped without a TTY: signing out is routine, and
// rewriting another program's config on the way past is not. Runs after the
// credentials are cleared, which is safe because removal is pure filesystem.
//
// --yes suppresses the question rather than answering it. That flag means "do
// not ask me about signing out"; taking it as consent to rewrite another
// program's config would be a much larger yes than the one given, and asking
// anyway would hang the scripts that pass it. --disconnect is how a script
// opts in.
func disconnectOnLogout(opts *setupOptions, assumeYes bool) ([]disconnectRow, bool) {
	wired := detectedAgents()
	caps := []string{capGateway}
	targets := wiredTargets(wired, caps, opts)
	if len(targets) == 0 {
		return nil, false
	}
	rep := newReporter(false)
	if !assumeYes {
		if opts.noInput || opts.yes {
			return nil, false
		}
		names := make([]string, 0, len(targets))
		seen := map[string]bool{}
		for _, t := range targets {
			if !seen[t.agent] {
				seen[t.agent] = true
				names = append(names, t.agent)
			}
		}
		if !opts.confirm(fmt.Sprintf("Also remove orq from your coding agents (%s)?", strings.Join(names, ", ")), false) {
			return nil, false
		}
	}
	rows, _, failed := removeWiring(rep, wired, caps, false)
	return rows, failed
}

// reportCredentialSurvives corrects the mental model disconnect otherwise
// leaves: this removed the wire, not the key. The key is still live in the
// workspace and still works anywhere else it was copied -- the shell env file,
// an agent not named in this run, a CI secret. It said "the API key is
// untouched -- still valid, and still saved for the next 'orq connect'", which
// mixed a convenience fact about local storage with a security one about the
// server, and the reassuring half is the half people read.
func reportCredentialSurvives(rep *reporter) {
	if saved, _ := savedAPIKey(); saved == "" {
		return
	}
	if id := savedGatewayKeyID(); id != "" {
		rep.info("not revoked: the key still works. To revoke it: orq api-keys delete %s", id)
		return
	}
	rep.info("not revoked: the key still works wherever else it was copied")
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

	caps, err := resolveCapabilities(rep, opts)
	if err != nil {
		return nil, fmt.Errorf("setup cancelled at the capability selection: %w", err)
	}
	opts.caps = caps
	opts.noGateway = !hasCap(caps, capGateway)
	// setup ends on the final screen, which lists every wire per agent. The
	// per-agent progress lines say the same thing, so setup takes the screen
	// and connect — which has no screen — keeps the lines.
	opts.finalScreen = true
	return instrumentAgents(rep, client, state, opts)
}
