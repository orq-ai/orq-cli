package custom

import "github.com/spf13/cobra"

// Topic groups for the root help screen; the taxonomy mirrors the docs.orq.ai tabs and the studio sidebar.
const (
	groupGetStarted    = "getting-started"
	groupGateway       = "ai-gateway"
	groupObservability = "observability"
	groupAgents        = "managed-agents"
	groupOptimization  = "optimization"
	groupAdmin         = "administration"
	groupUtilities     = "utilities"
)

// In display order: cobra renders groups in the order they were added, not alphabetically.
var helpGroups = []*cobra.Group{
	{ID: groupGetStarted, Title: "Get started:"},
	{ID: groupGateway, Title: "AI Gateway:"},
	{ID: groupObservability, Title: "Observability:"},
	{ID: groupAgents, Title: "Managed agents:"},
	{ID: groupOptimization, Title: "Optimization:"},
	{ID: groupAdmin, Title: "Administration:"},
	{ID: groupUtilities, Title: "Utilities:"},
}

var commandGroup = map[string]string{
	"setup":      groupGetStarted,
	"connect":    groupGetStarted,
	"disconnect": groupGetStarted,
	"launch":     groupGetStarted,
	"doctor":     groupGetStarted,
	"update":     groupGetStarted,
	"version":    groupUtilities,
	"auth":       groupGetStarted,
	"login":      groupGetStarted,
	"logout":     groupGetStarted,
	"whoami":     groupGetStarted,

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
	"smart-routers":   groupGateway,
	"routing-rules":   groupGateway,
	"guardrail-rules": groupGateway,
	"pii":             groupGateway,
	"budgets":         groupGateway,
	"mcp-servers":     groupGateway,
	"mcp-gateways":    groupGateway,

	"traces":     groupObservability,
	"logs":       groupObservability,
	"activities": groupObservability,
	"alerts":     groupObservability,
	"notifiers":  groupObservability,
	"identities": groupObservability,
	"feedback":   groupObservability,

	"agents":           groupAgents,
	"agents-responses": groupAgents,
	"deployments":      groupAgents,
	"skills":           groupAgents,
	"tools":            groupAgents,
	"memory-stores":    groupAgents,
	"knowledge-bases":  groupAgents,
	"schedules":        groupAgents,
	"prompts":          groupAgents,

	"datasets":          groupOptimization,
	"evals":             groupOptimization,
	"human-review-sets": groupOptimization,
	"annotation-queues": groupOptimization,

	"workspace":          groupAdmin,
	"workspace-settings": groupAdmin,
	"workspace-security": groupAdmin,
	"projects":           groupAdmin,
	"people":             groupAdmin,
	"api-keys":           groupAdmin,
	"management-keys":    groupAdmin,
	"policies":           groupAdmin,
	"model-sharing":      groupAdmin,
	"webhooks":           groupAdmin,
	"files":              groupAdmin,
	"reporting":          groupAdmin,
	"feature-previews":   groupAdmin,
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
		name := cmd.Name()
		if _, mapped := commandGroup[name]; mapped || deliberateUtilities[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// applyCommandGroups defaults unmapped commands to Utilities: cobra panics at Execute time on an unregistered GroupID, and an empty GroupID exiles the command to an "Additional Commands" trailer.
func applyCommandGroups(root *cobra.Command) {
	for _, g := range helpGroups {
		if !root.ContainsGroup(g.ID) {
			root.AddGroup(g)
		}
	}
	for _, cmd := range root.Commands() {
		if id, ok := commandGroup[cmd.Name()]; ok {
			cmd.GroupID = id
			continue
		}
		cmd.GroupID = groupUtilities
	}
	// cobra creates help and completion in ExecuteC, after this walk, so they can only be grouped through these setters.
	root.SetHelpCommandGroupID(groupUtilities)
	root.SetCompletionCommandGroupID(groupUtilities)
}
