package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// gemini-cli speaks the Gemini-native protocol, not the OpenAI-shaped
	// router every other agent here uses: GOOGLE_GEMINI_BASE_URL only affects
	// the Gemini Developer API client, the same one GEMINI_API_KEY
	// authenticates. /v3/google/v1beta/models/{model}:{action} is what
	// @google/genai (gemini-cli's SDK) builds against a custom baseUrl. The
	// route is merged on orquesta-web main and packaged into the v4.15.0-rc
	// line, but no stable v4.15.0 tag has been cut, so it 404s on production
	// today. No change is needed on either side once 4.15 ships.
	DefaultGeminiGatewayURL = DefaultGatewayAPIBaseURL + "/v3/google"

	// Bare rather than provider-qualified, because gemini-cli rewrites any
	// model id ending in "flash" to geminiRewriteTarget before it leaves the
	// machine, stripping the provider prefix with it. Naming the id that
	// actually gets served keeps the launch honest; geminiNormalize is what
	// lets this still match a google/-qualified catalogue entry.
	DefaultGeminiModel = geminiRewriteTarget

	// The only auth type `gemini -p` honours: outside ACP mode the CLI
	// requires security.auth.selectedType in settings.json, and
	// GOOGLE_GEMINI_BASE_URL alone is not enough. AuthType.GATEWAY looks like
	// it should skip settings.json but validateAuthMethod rejects it, so it is
	// unusable today.
	geminiAuthSelectedType = "gemini-api-key"

	// What gemini-cli substitutes for every model id ending in "flash".
	// Version-specific: it is DEFAULT_GEMINI_3_5_FLASH_MODEL on the 0.58.0
	// bundle this was verified against.
	geminiRewriteTarget = "gemini-3.5-flash"
)

// geminiNormalize strips the provider prefix the orq catalogue carries, so a
// bare DefaultGeminiModel matches a google/gemini-3.5-flash entry from
// FetchEnabledModels. Without it the membership test in ResolveGatewayConfig
// can never hold and every launch substitutes the first enabled model instead.
var geminiNormalize = MakeNormalizeModel([]string{"google", "google-ai"})

// geminiServesModel keeps the catalogue to the providers the Gemini-native
// wire can answer for. Workspace autorouters (workspace@orq/...) are excluded
// deliberately: they resolve to any provider, so they hit the same problem one
// step later.
func geminiServesModel(id string) bool {
	return strings.HasPrefix(id, "google/") || strings.HasPrefix(id, "google-ai/")
}

func geminiAgent() AgentDef {
	return AgentDef{
		Name:          "gemini",
		Binary:        "gemini",
		Label:         "Gemini CLI",
		InstallHint:   "npm install -g @google/gemini-cli",
		FetchesModels: true,
		// gemini-cli takes one --model per session and has no picker list to
		// populate (settings.json holds auth and MCP servers, nothing about
		// models), so resolved.GatewayModels has nowhere to go here. Offering
		// --models would accept a list and use at most one entry of it. Same
		// call as codex, which has the same one-model-per-session shape.
		AllowModels: false,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"-p", v} },
		},
		Resolve: resolveGemini,
	}
}

// resolveGemini wires gemini-cli through GOOGLE_GEMINI_BASE_URL and
// GEMINI_API_KEY, plus GEMINI_CLI_HOME pointed at a fresh temp dir holding
// .gemini/settings.json. gemini-cli has no per-file config override like
// claude's --mcp-config, but its own homedir() reads GEMINI_CLI_HOME ahead of
// os.homedir(), so this hands it a selectedType and an MCP config without
// touching the user's real ~/.gemini and without overriding all of HOME.
func resolveGemini(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:     ctx.Creds.APIKey,
		APIBaseURL:    ctx.Creds.APIBaseURL,
		Getenv:        ctx.Getenv,
		Flags:         ctx.Flags,
		Fetch:         ctx.Fetch,
		Normalize:     geminiNormalize,
		ServesModel:   geminiServesModel,
		DefaultModel:  DefaultGeminiModel,
		BaseURLEnvKey: "ORQ_GEMINI_BASE_URL",
		ModelEnvKey:   "GEMINI_MODEL",
		// ORQ_GATEWAY_URL is the router URL; gemini is not on the router.
		IgnoreSharedGatewayEnv: true,
		// One active model, so there is no explicit-models list to merge in.
		ModelsEnvKey: "",
		// Derive from the session's API base before the hardcoded public default,
		// the way claude.go does for /v3/anthropic. This cannot be left empty like
		// the router agents do: ResolveGatewayConfig's own derive step hardcodes
		// /v3/router, the wrong surface for gemini. Without this, an on-prem
		// deployment sends every prompt and file to my.orq.ai while authenticating
		// with the customer's own key.
		DefaultBaseURL: firstNonEmpty(
			deriveFromAPIBase(ctx.Creds.APIBaseURL, "/v3/google"),
			DefaultGeminiGatewayURL,
		),
	})
	if err != nil {
		return nil, err
	}

	home, cleanup, err := writeGeminiHome(BuildGeminiSettingsJSON(mcpURL(ctx)))
	if err != nil {
		return nil, err
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"GEMINI_API_KEY":         ctx.Creds.APIKey,
			"GOOGLE_GEMINI_BASE_URL": resolved.BaseURL,
			"ORQ_API_KEY":            ctx.Creds.APIKey,
			"ORQ_SERVER":             ctx.Creds.APIBaseURL,
			"GEMINI_CLI_HOME":        home,
			// gemini-cli refuses to run non-interactively outside a trusted
			// directory (confirmed live: without this it exits before making
			// any request, with "Gemini CLI is not running in a trusted
			// directory"). orq launch is headless, so there is no interactive
			// prompt to trust it through.
			"GEMINI_CLI_TRUST_WORKSPACE": "true",
		},
		TempDirs: []TempDir{{HostPath: home}},
		Cleanup:  cleanup,
		Warnings: []string{
			"gemini launches with a fresh config home for this session, so its own extensions, skills and session history are not available",
		},
		PreArgs: []string{"--model", resolved.GatewayModel},
	}
	// Only the fetch warnings, not appendModelWarnings' missing-provider-prefix
	// hint: a bare id is the correct shape here, since the gateway's
	// NormalizeGoogleModelID resolves gemini-* itself and a provider-qualified
	// flash id is exactly what gemini-cli rewrites. Telling a gemini user to add
	// a prefix would push them into the bug below.
	plan.Warnings = append(plan.Warnings, resolved.ModelWarnings...)
	if w := geminiRewriteWarning(resolved.GatewayModel); w != "" {
		plan.Warnings = append(plan.Warnings, w)
	}
	return plan, nil
}

// geminiRewritesModel reports whether gemini-cli will swap this id for
// geminiRewriteTarget before sending. Its gate is
//
//	useGemini3_5Flash && isFlashModel(resolved) && normalized != "gemini-3-flash-preview"
//
// and isFlashModel ends in model.endsWith("flash"), so a plain suffix test is
// the whole rule: every other clause in it is an id that already ends in
// "flash". Verified on 0.58.0 by pointing the CLI at a local Google-wire
// server and reading back the id it requested, for flash, gemini-flash,
// orq-flash, custom-model-flash, gemini-2.5-flash and
// google-ai/gemini-3.7-flash, against gemini-3-flash-preview and
// gemini-2.5-pro passing through intact.
func geminiRewritesModel(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" &&
		model != geminiPreviewFlashModel &&
		strings.HasSuffix(model, "flash")
}

// geminiPreviewFlashModel is PREVIEW_GEMINI_FLASH_MODEL, the one id the
// rewrite gate explicitly exempts.
const geminiPreviewFlashModel = "gemini-3-flash-preview"

// geminiRewriteWarning reports what gemini-cli will actually request when the
// resolved model is one it rewrites. This warns rather than failing on
// purpose: the rewrite is upstream bug google-gemini/gemini-cli#28859, still
// open and unexplained, so a hard error keyed to it would break the day it is
// fixed. Without the warning the substitution is invisible, and orq's own logs
// would show a model the user never chose.
func geminiRewriteWarning(model string) string {
	if !geminiRewritesModel(model) || model == geminiRewriteTarget {
		return ""
	}
	return fmt.Sprintf(
		"gemini-cli rewrites %q to %q before sending (upstream bug google-gemini/gemini-cli#28859), so that is the model orq will see; pass a model whose id does not end in \"flash\" to avoid it",
		model, geminiRewriteTarget)
}

// BuildGeminiSettingsJSON serializes the .gemini/settings.json launch writes:
// the auth type gemini-cli needs to accept GEMINI_API_KEY outside ACP mode,
// plus the orq MCP server when mcpServerURL is non-empty. gemini-cli reads
// httpUrl for a remote server (server.httpUrl || server.url in its own
// source), the same field name a persisted `gemini mcp add` entry would use.
func BuildGeminiSettingsJSON(mcpServerURL string) map[string]any {
	settings := map[string]any{
		"security": map[string]any{
			"auth": map[string]any{"selectedType": geminiAuthSelectedType},
		},
	}
	if mcpServerURL != "" {
		settings["mcpServers"] = map[string]any{
			MCPServerName: map[string]any{"httpUrl": mcpServerURL},
		}
	}
	return settings
}

func writeGeminiHome(settings map[string]any) (home string, cleanup func(), err error) {
	home, err = os.MkdirTemp("", "orq-gemini-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(home) }

	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), encoded, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return home, cleanup, nil
}
