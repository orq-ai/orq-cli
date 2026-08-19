package launch

import (
	"strings"
	"testing"
)

// A vendor's own CLI must open on that vendor's model when the workspace
// catalogue cannot answer. kimi shipped defaulting to anthropic/claude-sonnet-4-6,
// so `orq launch kimi` against an empty catalogue started Kimi Code on Claude.
// opencode and pi are vendor-neutral and are deliberately absent.
func TestVendorAgentsDefaultToTheirOwnVendor(t *testing.T) {
	for agent, want := range map[string]string{
		"claude": "anthropic/",
		"codex":  "openai/",
		"kimi":   "moonshotai/",
	} {
		got := map[string]string{
			"claude": DefaultClaudeModel,
			"codex":  DefaultCodexModel,
			"kimi":   DefaultKimiModel,
		}[agent]
		if !strings.HasPrefix(got, want) {
			t.Errorf("%s defaults to %q, want a %s model: launching a vendor's own CLI "+
				"on a competitor's model is not what the user asked for", agent, got, want)
		}
	}
}
