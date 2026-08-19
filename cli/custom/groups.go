package custom

import "github.com/spf13/cobra"

// Topic groups for the root help screen. `orq --help` is first contact with
// the CLI, and a flat alphabetical list of dozens of commands hides `setup`
// between `server` and `skills`. Cobra renders a grouped list as soon as the root has
// groups, so this is display-only: no command is renamed, moved, or hidden,
// and nothing here reaches surface.json.
//
// The taxonomy mirrors the docs.orq.ai tabs and the studio sidebar, so a user
// who learned the product in either place finds the same shelves here.
const (
	groupGetStarted    = "getting-started"
	groupGateway       = "ai-gateway"
	groupObservability = "observability"
	groupAgents        = "managed-agents"
	groupOptimization  = "optimization"
	groupAdmin         = "administration"
	// groupUtilities is the fallback for every unmapped command — see
	// applyCommandGroups.
	groupUtilities = "utilities"
)

// helpGroups is in display order: cobra renders groups in the order they were
// added, not alphabetically. Titles carry their own colon; cobra prints them
// verbatim as section headers.
var helpGroups = []*cobra.Group{
	{ID: groupGetStarted, Title: "Get started:"},
	{ID: groupGateway, Title: "AI Gateway:"},
	{ID: groupObservability, Title: "Observability:"},
	{ID: groupAgents, Title: "Managed agents:"},
	{ID: groupOptimization, Title: "Optimization:"},
	{ID: groupAdmin, Title: "Administration:"},
	{ID: groupUtilities, Title: "Utilities:"},
}

// commandGroup maps a top-level command name to its group. Names that no
// command answers to are harmless (a spec can drop a tag between releases);
// commands missing from the map are not (see applyCommandGroups).
//
// A few entries name commands that are currently invisible because every one
// of their subcommands is generated Hidden (guardrail-rules, routing-rules,
// policies, model-sharing, feature-previews, human-review-sets) or that only
// exist in the rc tree (workspace-security). They are listed anyway so the
// command lands in the right section the day it becomes visible.
var commandGroup = map[string]string{
	// Get started
	"setup":  groupGetStarted,
	"launch": groupGetStarted,
	"doctor": groupGetStarted,
	"auth":   groupGetStarted,
	"login":  groupGetStarted,
	"logout": groupGetStarted,
	"whoami": groupGetStarted,

	// AI Gateway
	"models":          groupGateway,
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
	"smart-routers":   groupGateway,
	"routing-rules":   groupGateway,
	"guardrail-rules": groupGateway,
	"pii":             groupGateway,
	"budgets":         groupGateway,

	// Observability
	"traces":     groupObservability,
	"logs":       groupObservability,
	"activities": groupObservability,
	"alerts":     groupObservability,
	"notifiers":  groupObservability,
	"identities": groupObservability,
	"feedback":   groupObservability,

	// Managed agents
	"agents":           groupAgents,
	"agents-responses": groupAgents,
	"deployments":      groupAgents,
	"skills":           groupAgents,
	"tools":            groupAgents,
	"memory-stores":    groupAgents,
	"knowledge-bases":  groupAgents,
	"chunking":         groupAgents,
	"schedules":        groupAgents,
	"prompts":          groupAgents,

	// Optimization
	"datasets":          groupOptimization,
	"evals":             groupOptimization,
	"human-review-sets": groupOptimization,
	"annotation-queues": groupOptimization,

	// Administration
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

// applyCommandGroups registers the groups on the root and assigns one to every
// top-level command. It runs after the generated tree is attached, so it never
// edits a generated file.
//
// Unmapped commands default to Utilities rather than being left ungrouped:
// cobra panics on a GroupID that was never registered, and an EMPTY GroupID
// exiles the command to an "Additional Commands" trailer. Defaulting means a
// newly generated tag shows up in the wrong section at worst, never crashes
// and never disappears.
//
// Descriptions are deliberately untouched here — RES-1372 covers the generated
// Short/Long text that echoes the OpenAPI tag name.
func applyCommandGroups(root *cobra.Command) {
	for _, g := range helpGroups {
		// Pure defense against a double Register on one root; every fresh
		// root (bartolo recreates Root per Init) starts with zero groups.
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
	// `help` is created by cobra at Execute time, after this walk, so it can
	// only be grouped through the setter. Same for the completion command when
	// bartolo has not already registered its own.
	root.SetHelpCommandGroupID(groupUtilities)
	root.SetCompletionCommandGroupID(groupUtilities)
}
