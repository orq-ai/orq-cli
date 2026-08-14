package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultCodexModel = "openai/gpt-5.4"

var codexNormalize = MakeNormalizeModel([]string{"orq"})

func codexAgent() AgentDef {
	return AgentDef{
		Name:          "codex",
		Binary:        "codex",
		Label:         "OpenAI Codex CLI",
		InstallHint:   "npm install -g @openai/codex",
		NpmPackage:    "@openai/codex",
		FetchesModels: true,
		AllowModels:   false,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"exec", "--full-auto", v} },
		},
		Resolve: resolveCodex,
	}
}

// resolveCodex configures codex with the router base URL via -c TOML
// overrides, and rewrites the bundled model catalog so the picker lists
// gateway models. Catalog failures degrade to a warning.
func resolveCodex(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:     ctx.Creds.APIKey,
		APIBaseURL:    ctx.Creds.APIBaseURL,
		Getenv:        ctx.Getenv,
		Flags:         ctx.Flags,
		Fetch:         ctx.Fetch,
		Normalize:     codexNormalize,
		DefaultModel:  DefaultCodexModel,
		BaseURLEnvKey: "ORQ_CODEX_BASE_URL",
		ModelEnvKey:   "CODEX_MODEL",
	})
	if err != nil {
		return nil, err
	}

	plan := &LaunchPlan{
		Env: map[string]string{"ORQ_API_KEY": ctx.Creds.APIKey},
	}
	appendModelWarnings(plan, resolved, codexNormalize, "openai/gpt-5.4")

	catalogPath := ""
	if ctx.ExecProbe == nil {
		// Sandbox dry-run: no container to probe, so the catalog step is
		// skipped — say so, or the printed command silently differs from a
		// real run.
		plan.Warnings = append(plan.Warnings, "codex model catalog skipped (no container on dry-run); a real run adds -c model_catalog_json=…")
	} else if path, cleanup, err := writeCodexCatalog(ctx, resolved.GatewayModels); err != nil {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"could not build codex model catalog (model picker will show bundled models): %v", err))
	} else {
		catalogPath = path
		plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: filepath.Dir(path)})
		plan.Cleanup = cleanup
	}

	plan.PreArgs = BuildCodexOverrideArgs(resolved.GatewayModel, resolved.BaseURL, catalogPath)
	if url := mcpURL(ctx); url != "" {
		plan.PreArgs = append(plan.PreArgs, codexMCPArgs(url)...)
	}
	return plan, nil
}

func tomlOverride(key, value string) string {
	return key + "=" + TOMLString(value)
}

// CodexProvider is the key codex registers the orq gateway under.
const CodexProvider = "orq"

// CodexProviderSettings is the codex configuration that points it at the orq
// gateway, as ordered dotted-key/value pairs.
//
// Two commands deliver these differently — launch as `-c key=value` overrides
// on the argv, setup as TOML in a profile file — so the settings themselves
// live in one place and each command only formats them. An empty model or
// modelCatalogPath omits that key rather than writing a blank one.
func CodexProviderSettings(model, baseURL, modelCatalogPath string) [][2]string {
	settings := [][2]string{}
	if model != "" {
		settings = append(settings, [2]string{"model", model})
	}
	settings = append(settings,
		[2]string{"model_provider", CodexProvider},
		[2]string{"model_providers." + CodexProvider + ".name", ProviderDisplayName},
		[2]string{"model_providers." + CodexProvider + ".base_url", baseURL},
		[2]string{"model_providers." + CodexProvider + ".env_key", "ORQ_API_KEY"},
		// codex removed chat/completions support (hard error since Feb 2026);
		// "responses" is the only valid wire_api, and it is per-provider only
		// — a second chat-shaped provider like opencode's is not an option.
		[2]string{"model_providers." + CodexProvider + ".wire_api", "responses"},
	)
	if modelCatalogPath != "" {
		settings = append(settings, [2]string{"model_catalog_json", modelCatalogPath})
	}
	return settings
}

// BuildCodexOverrideArgs builds the -c TOML override argv prefix.
func BuildCodexOverrideArgs(model, baseURL, modelCatalogPath string) []string {
	var args []string
	for _, kv := range CodexProviderSettings(model, baseURL, modelCatalogPath) {
		args = append(args, "-c", tomlOverride(kv[0], kv[1]))
	}
	return args
}

// BuildCodexModelCatalog rewrites codex's bundled catalog template so every
// gateway model appears in the picker, reusing the first bundled entry's
// capability fields.
func BuildCodexModelCatalog(templateCatalogJSON string, models []string) (string, error) {
	var template struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal([]byte(templateCatalogJSON), &template); err != nil {
		return "", err
	}
	if len(template.Models) == 0 {
		return "", fmt.Errorf("codex model catalog is empty")
	}
	base := template.Models[0]

	deduped := DedupeModels(models, codexNormalize)
	out := struct {
		Models []map[string]any `json:"models"`
	}{Models: make([]map[string]any, 0, len(deduped))}

	for i, model := range deduped {
		entry := make(map[string]any, len(base)+6)
		for k, v := range base {
			entry[k] = v
		}
		entry["slug"] = model
		entry["display_name"] = model
		entry["description"] = "Available through " + ProviderDisplayName + ": " + model
		priority := 1000 - i
		if priority < 1 {
			priority = 1
		}
		entry["priority"] = priority
		entry["availability_nux"] = nil
		entry["upgrade"] = nil
		out.Models = append(out.Models, entry)
	}

	encoded, err := json.Marshal(out)
	return string(encoded), err
}

// ExtractJSONFromCodexOutput finds the last JSON line in `codex debug models
// --bundled` output (codex prints log noise before it).
func ExtractJSONFromCodexOutput(output string) (string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
			return lines[i], nil
		}
	}
	return "", fmt.Errorf("codex did not return a JSON model catalog")
}

func writeCodexCatalog(ctx *AgentContext, models []string) (path string, cleanup func(), err error) {
	output, err := ctx.ExecProbe("codex", "debug", "models", "--bundled")
	if err != nil {
		return "", nil, err
	}
	templateJSON, err := ExtractJSONFromCodexOutput(output)
	if err != nil {
		return "", nil, err
	}
	catalogJSON, err := BuildCodexModelCatalog(templateJSON, models)
	if err != nil {
		return "", nil, err
	}

	dir, err := os.MkdirTemp("", "orq-codex-catalog-")
	if err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "model-catalog.json")
	if err := os.WriteFile(path, []byte(catalogJSON), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}
