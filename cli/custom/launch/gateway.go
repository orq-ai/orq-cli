// Package launch starts coding-agent CLIs (claude, codex, opencode, kilo,
// kimi, pi) preconfigured to route model calls through the orq.ai AI Router.
//
// Shared core for every `orq launch <agent>` command. Agents differ only in
// how they serialize provider config and launch their CLI; resolving the
// gateway model catalog, auth, base URLs, and arg parsing is identical, so it
// lives here.
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	DefaultGatewayBaseURL    = "https://api.orq.ai/v3/router"
	DefaultGatewayAPIBaseURL = "https://api.orq.ai"
)

// The label agents show for the orq provider in their own model pickers. It was
// spelled three different ways across the agent builders; a user who wires two
// agents sees this string in both, so it lives in one place.
const (
	ProviderDisplayName          = "Orq AI Gateway"
	ProviderResponsesDisplayName = "Orq AI Gateway (Responses)"
)

// GatewayFlags are the shared flags every agent subcommand understands.
type GatewayFlags struct {
	Model         string
	Models        string
	BaseURL       string
	NoFetchModels bool
	NoMCP         bool
	NoSkills      bool
	Sandbox       bool
	MountCwd      bool
	Rebuild       bool
	DryRun        bool
	Help          bool
}

// GatewayConfig is the fully resolved routing configuration for one launch.
type GatewayConfig struct {
	AuthToken     string
	APIBaseURL    string
	BaseURL       string
	GatewayModel  string
	GatewayModels []string
	// ModelWarnings collects everything the user should hear about model
	// resolution: fetch failures, an empty catalog, a silently substituted
	// default. Surfaced via appendModelWarnings.
	ModelWarnings []string
}

// ModelInfo is one entry from GET /v2/models, with the metadata kimi needs.
type ModelInfo struct {
	ID              string // provider/model_id
	ContextWindow   int
	MaxOutputTokens int
	// SupportsResponses mirrors metadata.supports_responses_api. Agents that
	// declare providers per API shape need it to put each model on the right
	// one; see IsResponsesModel for the fallback when a model is not in the
	// fetched catalogue.
	SupportsResponses bool
}

// NormalizeModel strips an agent's provider prefixes (e.g. "orq/",
// "orq-openai/") back to a bare gateway id ("provider/model_id").
type NormalizeModel func(model string) string

// IsResponsesModel reports whether the gateway serves this model via the
// Responses API, for models absent from the fetched catalogue: --model,
// --models and the built-in defaults never carry metadata. Prefer
// ResponsesModelSet, which uses the catalogue's own
// metadata.supports_responses_api.
//
// The prefix is deliberately under-inclusive here. Putting a Responses-capable
// model on the chat provider still works — chat completions serves every model
// — while the reverse fails outright, so guessing "chat" is the safe default.
func IsResponsesModel(gatewayModel string) bool {
	return strings.HasPrefix(gatewayModel, "openai/")
}

// ResponsesModelSet answers IsResponsesModel from catalogue metadata, falling
// back to the prefix heuristic for ids the catalogue did not describe.
//
// The heuristic alone missed 13 of 25 Responses-capable models in a real
// workspace — every azure/gpt-*, the openai autorouters, and several
// third-party models — putting them on chat completions and losing the
// tools+reasoning path the gateway would otherwise serve.
func ResponsesModelSet(infos []ModelInfo) func(string) bool {
	known := make(map[string]bool, len(infos))
	for _, info := range infos {
		known[info.ID] = info.SupportsResponses
	}
	return func(id string) bool {
		if supports, ok := known[id]; ok {
			return supports
		}
		return IsResponsesModel(id)
	}
}

func MakeNormalizeModel(providerPrefixes []string) NormalizeModel {
	prefixes := make([]string, len(providerPrefixes))
	for i, p := range providerPrefixes {
		prefixes[i] = p + "/"
	}
	return func(model string) string {
		for _, prefix := range prefixes {
			if strings.HasPrefix(model, prefix) {
				return model[len(prefix):]
			}
		}
		return model
	}
}

var errModelList = errors.New("flag --models expects a comma-separated list or JSON array")

// ParseModelList accepts "a,b,c" or a JSON array `["a","b"]`.
func ParseModelList(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var parsed []string
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, errModelList
		}
		out := parsed[:0]
		for _, entry := range parsed {
			if e := strings.TrimSpace(entry); e != "" {
				out = append(out, e)
			}
		}
		return out, nil
	}
	var out []string
	for _, entry := range strings.Split(trimmed, ",") {
		if e := strings.TrimSpace(entry); e != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func DedupeModels(models []string, normalize NormalizeModel) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range models {
		if m == "" {
			continue
		}
		n := normalize(m)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func ShouldWarnMissingProviderPrefix(model string, normalize NormalizeModel) bool {
	return !strings.Contains(normalize(model), "/")
}

// deriveFromAPIBase returns apiBase+path when the CLI points at a non-default
// API base (self-hosted / regional), so anthropic-native and MCP endpoints
// follow the override instead of silently staying on production. Returns ""
// on the default base — the hardcoded per-endpoint defaults win there.
func deriveFromAPIBase(apiBase, path string) string {
	if apiBase == "" || apiBase == DefaultGatewayAPIBaseURL {
		return ""
	}
	return strings.TrimSuffix(apiBase, "/") + path
}

// MissingCapModels returns the gateway models that have no fetched metadata —
// agents that bake context/output caps into config (kimi, pi) fall back to
// conservative limits for these, which silently truncates output on capable
// models unless the user is told.
func MissingCapModels(gatewayModels []string, infos []ModelInfo) []string {
	known := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info.ContextWindow > 0 || info.MaxOutputTokens > 0 {
			known[info.ID] = true
		}
	}
	var missing []string
	for _, id := range gatewayModels {
		if !known[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// appendCapWarning adds the conservative-caps warning for agents that write
// per-model output limits into their config.
func appendCapWarning(plan *LaunchPlan, resolved *ResolvedModels) {
	if missing := MissingCapModels(resolved.GatewayModels, resolved.Infos); len(missing) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"no metadata for %s; using conservative caps (context %d, max output %d) — responses may truncate early on capable models",
			strings.Join(missing, ", "), fallbackContextSize, fallbackOutputSize))
	}
}

// appendModelWarnings collects the shared post-resolution warnings: the model
// fetch failure (if any) and the missing provider/ prefix hint. example is an
// agent-appropriate model id shown in the hint.
func appendModelWarnings(plan *LaunchPlan, resolved *ResolvedModels, normalize NormalizeModel, example string) {
	plan.Warnings = append(plan.Warnings, resolved.ModelWarnings...)
	if ShouldWarnMissingProviderPrefix(resolved.GatewayModel, normalize) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"model %q has no provider/ prefix; the gateway expects e.g. %s", resolved.GatewayModel, example))
	}
}

// ModelFetcher fetches the enabled chat models from the gateway.
type ModelFetcher func(apiKey, apiBaseURL string) ([]ModelInfo, error)

// FetchEnabledModels calls GET <apiBase>/v2/models and returns enabled chat
// models sorted by id.
func FetchEnabledModels(apiKey, apiBaseURL string) ([]ModelInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiBaseURL, "/")+"/v2/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET /v2/models: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// The endpoint returns a top-level JSON array (see openapi.yaml ModelList).
	var payload []struct {
		Provider  string `json:"provider"`
		ModelID   string `json:"model_id"`
		RefID     string `json:"refId"`
		ModelType string `json:"model_type"`
		Enabled   bool   `json:"enabled"`
		Functions bool   `json:"has_functions"`
		Metadata  *struct {
			ContextWindow     int  `json:"context_window"`
			MaxOutputTokens   int  `json:"max_output_tokens"`
			SupportsResponses bool `json:"supports_responses_api"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	var models []ModelInfo
	for _, m := range payload {
		// Tool calling is not optional for a coding agent: it edits files and
		// runs commands through tools, so a model without it fails on the first
		// real turn. The workspace can legitimately enable such models
		// (perplexity/sonar-* are search models) — they just do not belong in a
		// coding agent's list. Same rule setup applies.
		if !m.Enabled || m.ModelType != "chat" || !m.Functions {
			continue
		}
		// refId is the canonical invoke id: "provider/model_id" for system
		// models, "workspace@orq/model_id" for custom models (autorouters).
		// Building provider/model_id for the latter would yield "orq/<name>",
		// which agent normalizers strip back to a bare, un-invokable name.
		id := m.RefID
		if id == "" {
			id = m.Provider + "/" + m.ModelID
		}
		info := ModelInfo{ID: id}
		if m.Metadata != nil {
			info.ContextWindow = m.Metadata.ContextWindow
			info.MaxOutputTokens = m.Metadata.MaxOutputTokens
			info.SupportsResponses = m.Metadata.SupportsResponses
		}
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// ResolveInput carries everything ResolveGatewayConfig needs, with injectable
// seams for tests.
type ResolveInput struct {
	AuthToken  string // resolved API key (see auth.go)
	APIBaseURL string // resolved API base (see auth.go)

	Getenv func(string) string
	Flags  GatewayFlags
	Fetch  ModelFetcher // nil → FetchEnabledModels

	Normalize         NormalizeModel
	DefaultModel      string
	BaseURLEnvKey     string
	ModelEnvKey       string
	ModelsEnvKey      string // "" → agent has no models env
	DefaultBaseURL    string // "" → DefaultGatewayBaseURL
	CollectModelInfos bool   // kimi: keep per-model metadata
}

// ResolvedModels is GatewayConfig plus optional per-model metadata (kimi).
type ResolvedModels struct {
	GatewayConfig
	Infos []ModelInfo // only when CollectModelInfos; fetched models only
}

// ResolveGatewayConfig resolves the gateway wiring shared by all agents.
// Base URL priority: flag > agent env > ORQ_GATEWAY_URL > agent default >
// derived from the session's API base > public default; model priority: flag >
// env > default-if-fetched > first-fetched > first-explicit > fallback.
func ResolveGatewayConfig(input ResolveInput) (*ResolvedModels, error) {
	if input.AuthToken == "" {
		return nil, errors.New("not logged in. Run 'orq auth login' or export ORQ_API_KEY")
	}
	getenv := input.Getenv
	normalize := input.Normalize

	// Derive from the session's API base before falling back to the public
	// host. orq can be deployed on-prem, where the gateway lives on the
	// customer's own domain: without this the agent's MCP server pointed at
	// their host while every model call — prompts, code, whole file contents —
	// went to api.orq.ai instead, authenticated with a key their own gateway
	// issued. claude got this right on its own (see claude.go); the shared
	// resolver every other agent uses did not.
	baseURL := firstNonEmpty(
		input.Flags.BaseURL,
		getenv(input.BaseURLEnvKey),
		getenv("ORQ_GATEWAY_URL"),
		input.DefaultBaseURL,
		deriveFromAPIBase(input.APIBaseURL, "/v3/router"),
		DefaultGatewayBaseURL,
	)

	var explicit []string
	if input.Flags.Models != "" {
		parsed, err := ParseModelList(input.Flags.Models)
		if err != nil {
			return nil, err
		}
		explicit = append(explicit, parsed...)
	}
	if input.ModelsEnvKey != "" {
		if envModels := getenv(input.ModelsEnvKey); envModels != "" {
			parsed, err := ParseModelList(envModels)
			if err != nil {
				return nil, err
			}
			explicit = append(explicit, parsed...)
		}
	}
	explicitModels := DedupeModels(explicit, normalize)

	var fetched []string
	var infos []ModelInfo
	var warnings []string
	if !input.Flags.NoFetchModels {
		fetcher := input.Fetch
		if fetcher == nil {
			fetcher = FetchEnabledModels
		}
		fetchedInfos, err := fetcher(input.AuthToken, input.APIBaseURL)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"Could not fetch enabled models from %s/v2/models. Falling back to explicit/default models. %v",
				input.APIBaseURL, err))
		} else {
			ids := make([]string, len(fetchedInfos))
			for i, m := range fetchedInfos {
				ids[i] = m.ID
			}
			fetched = DedupeModels(ids, normalize)
			if input.CollectModelInfos {
				infos = fetchedInfos
			}
			if len(fetched) == 0 {
				warnings = append(warnings, fmt.Sprintf(
					"the workspace has no enabled chat models; launching against %q anyway — enable models in the orq.ai studio or pass --model",
					firstNonEmpty(input.Flags.Model, getenv(input.ModelEnvKey), first(explicitModels), input.DefaultModel)))
			}
		}
	}

	fallbackModel := firstNonEmpty(getenv(input.ModelEnvKey), input.DefaultModel)
	defaultSubstituted := false
	gatewayModel := normalize(firstNonEmpty(
		input.Flags.Model,
		getenv(input.ModelEnvKey),
		func() string {
			if slices.Contains(fetched, normalize(input.DefaultModel)) {
				return input.DefaultModel
			}
			if m := first(fetched); m != "" {
				defaultSubstituted = true
				return m
			}
			return firstNonEmpty(first(explicitModels), fallbackModel)
		}(),
	))
	if defaultSubstituted {
		warnings = append(warnings, fmt.Sprintf(
			"default model %q is not enabled in this workspace; using %q (first enabled model) — pass --model to pick explicitly",
			input.DefaultModel, gatewayModel))
	}

	all := append(append(append([]string{}, fetched...), explicitModels...), gatewayModel)
	gatewayModels := DedupeModels(all, normalize)

	return &ResolvedModels{
		GatewayConfig: GatewayConfig{
			AuthToken:     input.AuthToken,
			APIBaseURL:    input.APIBaseURL,
			BaseURL:       baseURL,
			GatewayModel:  gatewayModel,
			GatewayModels: gatewayModels,
			ModelWarnings: warnings,
		},
		Infos: infos,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func first(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
