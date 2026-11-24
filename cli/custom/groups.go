package custom

import "github.com/spf13/cobra"

// Topic groups for the root help screen; the taxonomy mirrors the docs.orq.ai tabs and the studio sidebar.
//
// A group ID is the section title without the trailing colon, because that is
// what the OpenAPI spec's x-cli-help-section carries and what Bartolo's
// HelpSection stamps into a generated command's GroupID. Using the same string
// is what lets the spec's section and our hand-written fallback land under one
// heading instead of two.
const (
	groupGetStarted    = "Get started"
	groupGateway       = "AI Gateway"
	groupObservability = "Observability"
	groupAgents        = "Managed agents"
	groupOptimization  = "Optimization"
	groupFactory       = "Factory"
	groupAdmin         = "Administration"
	groupUtilities     = "Utilities"
)

// In display order: cobra renders groups in the order they were added, not alphabetically.
var helpGroups = []*cobra.Group{
	{ID: groupGetStarted, Title: "Get started:"},
	{ID: groupGateway, Title: "AI Gateway:"},
	{ID: groupObservability, Title: "Observability:"},
	{ID: groupAgents, Title: "Managed agents:"},
	{ID: groupOptimization, Title: "Optimization:"},
	{ID: groupFactory, Title: "Factory:"},
	{ID: groupAdmin, Title: "Administration:"},
	{ID: groupUtilities, Title: "Utilities:"},
}

// RegisterHelpGroups adds the root help sections in docs order. It must run
// BEFORE generated.Register: Bartolo's HelpSection registers a section the
// first time a generated command names it and skips it when a group with that
// ID already exists, so the order here wins over generated register order and
// no section is announced twice. Idempotent; Register calls it again as a
// safety net for callers that only add groups after generation (the headings
// then render in the order they were first added, which is the only drawback).
//
// A section title the spec introduces that is not listed here is still
// registered by Bartolo, after ours, so it renders as its own heading after
// Utilities; nothing panics and its commands are not moved.
func RegisterHelpGroups(root *cobra.Command) {
	for _, g := range helpGroups {
		if !root.ContainsGroup(g.ID) {
			root.AddGroup(g)
		}
	}
}

var commandGroup = map[string]string{
	"setup":      groupGetStarted,
	"connect":    groupGetStarted,
	"disconnect": groupGetStarted,
	"launch":     groupGetStarted,
	"orqi":       groupGetStarted,
	"doctor":     groupGetStarted,
	"update":     groupGetStarted,
	"version":    groupUtilities,
	"auth":       groupGetStarted,
	"login":      groupGetStarted,
	"logout":     groupGetStarted,
	"status":     groupGetStarted,
	"switch":     groupGetStarted,

	"models":          groupGateway,
	"model-catalog":   groupGateway,
	"chat":            groupGateway,
	"completions":     groupGateway,
	"responses":       groupGateway,
	"embeddings":      groupGateway,
	"images":          groupGateway,
	"speech":          groupGateway,
	"transcriptions":  groupGateway,
	"translations":    groupGateway,
	"moderations":     groupGateway,
	"ocr":             groupGateway,
	"rerank":          groupGateway,
	"chunking":        groupGateway,
	"mcp-servers":     groupGateway,
	"mcp-gateways":    groupGateway,
	"smart-routers":   groupGateway,
	"routing-rules":   groupGateway,
	"guardrail-rules": groupGateway,
	"pii":             groupGateway,
	"budgets":         groupGateway,
	"model-fusions":   groupGateway,
	"decisions":       groupGateway,
	"websearch":       groupGateway,
	"hub":             groupGateway,
	"batches":         groupGateway,
	"live":            groupGateway,
	"virtual-models":  groupGateway,

	"traces":     groupObservability,
	"logs":       groupObservability,
	"activities": groupObservability,
	"alerts":     groupObservability,
	"notifiers":  groupObservability,
	"identities": groupObservability,
	"feedback":   groupObservability,
	"sessions":   groupObservability,
	"telemetry":  groupObservability,
	"views":      groupObservability,
	"monitors":   groupObservability,

	"agents":           groupAgents,
	"agents-responses": groupAgents,
	"deployments":      groupAgents,
	"skills":           groupAgents,
	"tools":            groupAgents,
	"memory-stores":    groupAgents,
	"knowledge-bases":  groupAgents,
	"wikis":            groupAgents,
	"schedules":        groupAgents,
	"prompts":          groupAgents,

	"datasets":          groupOptimization,
	"experiments":       groupOptimization,
	"evals":             groupOptimization,
	"human-review-sets": groupOptimization,
	"annotation-queues": groupOptimization,

	"file-systems": groupFactory,

	"workspace":              groupAdmin,
	"workspace-settings":     groupAdmin,
	"workspace-security":     groupAdmin,
	"projects":               groupAdmin,
	"people":                 groupAdmin,
	"api-keys":               groupAdmin,
	"management-keys":        groupAdmin,
	"policies":               groupAdmin,
	"model-sharing":          groupAdmin,
	"webhooks":               groupAdmin,
	"files":                  groupAdmin,
	"reporting":              groupAdmin,
	"feature-previews":       groupAdmin,
	"workspaces":             groupAdmin,
	"environments":           groupAdmin,
	"users":                  groupAdmin,
	"audit-log-destinations": groupAdmin,
}

// deliberateUtilities are the commands that belong in Utilities on purpose:
// cobra's own, bartolo's help pages, and the three of ours that are utilities
// by definition. Everything else reaching Utilities got there by fallback,
// which is a wrong section until someone chooses one — see UngroupedCommands.
var deliberateUtilities = map[string]bool{
	"help":           true, // cobra's own, grouped via SetHelpCommandGroupID
	"completion":     true, // cobra's own, grouped via SetCompletionCommandGroupID
	"help-config":    true, // bartolo's config help page
	"help-input":     true, // bartolo's input help page
	"request":        true, // raw-HTTP escape hatch — a utility by definition
	"mcp":            true, // generated MCP server over the raw API, like request
	"server":         true, // inspects/persists server-URL defaults
	"default-format": true, // persists the default output format
}

// UngroupedCommands returns the visible top-level commands nobody has put in a
// topic. It is exported for cmd/surface-dump: ~95% of the tree is regenerated
// from openapi.yaml, so a new tag arrives as a new top-level command with no
// group, and the person running the regeneration is the one who knows where it
// belongs. Reporting it there means they hear about it while they are looking
// at the change, not from a test failing on main a day later.
func UngroupedCommands(root *cobra.Command) []string {
	var out []string
	for _, cmd := range root.Commands() {
		// IsAvailableCommand, not just Hidden: a parent whose subcommands are
		// all hidden never renders.
		if cmd.Hidden || !cmd.IsAvailableCommand() {
			continue
		}
		// A GroupID that is already set came from the spec's x-cli-help-section.
		if cmd.GroupID != "" {
			continue
		}
		name := cmd.Name()
		if _, mapped := commandGroup[name]; mapped || deliberateUtilities[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// applyCommandGroups assigns every top-level command a registered group. A
// GroupID already on the command came from the spec's x-cli-help-section and
// wins; otherwise the hand-written commandGroup map decides, and anything left
// defaults to Utilities (cobra panics at Execute time on an unregistered
// GroupID, and an empty one exiles the command to an "Additional Commands"
// trailer).
func applyCommandGroups(root *cobra.Command) {
	RegisterHelpGroups(root)
	for _, cmd := range root.Commands() {
		if cmd.GroupID != "" {
			if !root.ContainsGroup(cmd.GroupID) {
				// Only reachable if something set a GroupID without registering it.
				cmd.GroupID = groupUtilities
			}
			continue
		}
		if id, ok := commandGroup[cmd.Name()]; ok {
			cmd.GroupID = id
			continue
		}
		cmd.GroupID = groupUtilities
	}
	for _, cmd := range root.Commands() {
		joinSectionedSiblings(cmd)
	}
	// cobra creates help and completion in ExecuteC, after this walk, so they can only be grouped through these setters.
	root.SetHelpCommandGroupID(groupUtilities)
	root.SetCompletionCommandGroupID(groupUtilities)
}

// joinSectionedSiblings handles subcommands under a generated group. Bartolo
// stamps the operation's section on every generated leaf, so the group holds a
// heading ("Managed agents:") while the subcommands we attach by hand (traces
// conversation, ...) have none and would be split off under "Additional
// Commands". Giving them the group the generated siblings use keeps one list.
// When the spec carries no sections the group has no headings and nothing
// changes.
func joinSectionedSiblings(parent *cobra.Command) {
	for _, child := range parent.Commands() {
		joinSectionedSiblings(child)
	}
	groups := parent.Groups()
	if len(groups) == 0 {
		return
	}
	target := groups[0].ID
	if parent.GroupID != "" && parent.ContainsGroup(parent.GroupID) {
		target = parent.GroupID
	}
	for _, child := range parent.Commands() {
		if child.GroupID == "" {
			child.GroupID = target
		}
	}
}
