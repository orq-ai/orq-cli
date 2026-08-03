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
		Name:        "codex",
		Binary:      "codex",
		Label:       "OpenAI Codex CLI",
		InstallHint: "npm install -g @openai/codex",
		NpmPackage:  "@openai/codex",
		AllowModels: false,
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
	if ctx.ExecProbe != nil {
		if path, cleanup, err := writeCodexCatalog(ctx, resolved.GatewayModels); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"could not build codex model catalog (model picker will show bundled models): %v", err))
		} else {
			catalogPath = path
			plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: filepath.Dir(path)})
			plan.Cleanup = cleanup
		}
	}

	plan.PreArgs = BuildCodexOverrideArgs(resolved.GatewayModel, resolved.BaseURL, catalogPath)
	if url := mcpURL(ctx); url != "" {
		plan.PreArgs = append(plan.PreArgs, codexMCPArgs(url)...)
	}
	return plan, nil
}

func tomlOverride(key, value string) string {
	quoted, _ := json.Marshal(value)
	return key + "=" + string(quoted)
}

// BuildCodexOverrideArgs builds the -c TOML override argv prefix.
func BuildCodexOverrideArgs(model, baseURL, modelCatalogPath string) []string {
	args := []string{
		"-c", tomlOverride("model", model),
		"-c", tomlOverride("model_provider", "orq"),
		"-c", tomlOverride("model_providers.orq.name", "orq Gateway"),
		"-c", tomlOverride("model_providers.orq.base_url", baseURL),
		"-c", tomlOverride("model_providers.orq.env_key", "ORQ_API_KEY"),
		"-c", tomlOverride("model_providers.orq.wire_api", "responses"),
	}
	if modelCatalogPath != "" {
		args = append(args, "-c", tomlOverride("model_catalog_json", modelCatalogPath))
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
		entry["description"] = "Available through orq Gateway: " + model
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
