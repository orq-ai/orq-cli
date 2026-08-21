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
