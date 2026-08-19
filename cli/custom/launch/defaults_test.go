package launch

import (
	"strings"
	"testing"
)

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

// A cut-down edition is the wrong end of the range for a coding agent: they
// reject tools the agents send. opencode and pi both shipped defaulting to
// openai/gpt-5-mini.
func TestNoDefaultIsASizeVariant(t *testing.T) {
	for agent, model := range map[string]string{
		"claude": DefaultClaudeModel, "codex": DefaultCodexModel, "kimi": DefaultKimiModel,
		"opencode": DefaultOpenCodeModel, "pi": DefaultPiModel,
	} {
		for _, suffix := range []string{"-mini", "-nano", "-small", "-lite", "-micro", "-tiny", "-flash"} {
			if strings.HasSuffix(model, suffix) {
				t.Errorf("%s defaults to %q, a %s variant", agent, model, suffix)
			}
		}
	}
}
