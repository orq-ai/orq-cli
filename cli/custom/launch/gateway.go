// Package launch starts coding-agent CLIs (claude, codex, opencode, kilo,
// kimi) preconfigured to route model calls through the orq.ai AI Router.
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
	"sort"
	"strings"
	"time"
)

const (
	DefaultGatewayBaseURL    = "https://api.orq.ai/v3/router"
	DefaultGatewayAPIBaseURL = "https://api.orq.ai"
)

// GatewayFlags are the shared flags every agent subcommand understands.
type GatewayFlags struct {
	Model         string
	Models        string
	BaseURL       string
	NoFetchModels bool
	Sandbox       bool
	MountCwd      bool
	Rebuild       bool
	DryRun        bool
	Help          bool
}

// GatewayConfig is the fully resolved routing configuration for one launch.
type GatewayConfig struct {
	AuthToken         string
	APIBaseURL        string
	BaseURL           string
	GatewayModel      string
	GatewayModels     []string
	ModelFetchWarning string
}

// ModelInfo is one entry from GET /v2/models, with the metadata kimi needs.
type ModelInfo struct {
	ID              string // provider/model_id
	ContextWindow   int
	MaxOutputTokens int
}

// NormalizeModel strips an agent's provider prefixes (e.g. "orq/",
// "orq-openai/") back to a bare gateway id ("provider/model_id").
type NormalizeModel func(model string) string

// IsResponsesModel reports whether the gateway serves this model via the
// Responses API. openai/* models support (and for gpt-5.x with tools+reasoning,
// require) Responses; everything else only has chat completions.
func IsResponsesModel(gatewayModel string) bool {
	return strings.HasPrefix(gatewayModel, "openai/")
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
		ModelType string `json:"model_type"`
		Enabled   bool   `json:"enabled"`
		Metadata  *struct {
			ContextWindow   int `json:"context_window"`
			MaxOutputTokens int `json:"max_output_tokens"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	var models []ModelInfo
	for _, m := range payload {
		if !m.Enabled || m.ModelType != "chat" {
			continue
		}
		info := ModelInfo{ID: m.Provider + "/" + m.ModelID}
		if m.Metadata != nil {
			info.ContextWindow = m.Metadata.ContextWindow
			info.MaxOutputTokens = m.Metadata.MaxOutputTokens
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

// ResolveGatewayConfig ports resolveGatewaySpawnConfig from the internal CLI:
// base URL priority flag > agent env > ORQ_GATEWAY_URL > default; model
// priority flag > env > default-if-fetched > first-fetched > first-explicit >
// fallback.
func ResolveGatewayConfig(input ResolveInput) (*ResolvedModels, error) {
	if input.AuthToken == "" {
		return nil, errors.New("not logged in. Run 'orq auth login' or export ORQ_API_KEY")
	}
	getenv := input.Getenv
	normalize := input.Normalize

	baseURL := firstNonEmpty(
		input.Flags.BaseURL,
		getenv(input.BaseURLEnvKey),
		getenv("ORQ_GATEWAY_URL"),
		input.DefaultBaseURL,
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
	var fetchWarning string
	if !input.Flags.NoFetchModels {
		fetcher := input.Fetch
		if fetcher == nil {
			fetcher = FetchEnabledModels
		}
		fetchedInfos, err := fetcher(input.AuthToken, input.APIBaseURL)
		if err != nil {
			fetchWarning = fmt.Sprintf(
				"Could not fetch enabled models from %s/v2/models. Falling back to explicit/default models. %v",
				input.APIBaseURL, err)
		} else {
			ids := make([]string, len(fetchedInfos))
			for i, m := range fetchedInfos {
				ids[i] = m.ID
			}
			fetched = DedupeModels(ids, normalize)
			if input.CollectModelInfos {
				infos = fetchedInfos
			}
		}
	}

	fallbackModel := firstNonEmpty(getenv(input.ModelEnvKey), input.DefaultModel)
	gatewayModel := normalize(firstNonEmpty(
		input.Flags.Model,
		getenv(input.ModelEnvKey),
		func() string {
			if contains(fetched, input.DefaultModel) {
				return input.DefaultModel
			}
			return firstNonEmpty(first(fetched), first(explicitModels), fallbackModel)
		}(),
	))

	all := append(append(append([]string{}, fetched...), explicitModels...), gatewayModel)
	gatewayModels := DedupeModels(all, normalize)

	return &ResolvedModels{
		GatewayConfig: GatewayConfig{
			AuthToken:         input.AuthToken,
			APIBaseURL:        input.APIBaseURL,
			BaseURL:           baseURL,
			GatewayModel:      gatewayModel,
			GatewayModels:     gatewayModels,
			ModelFetchWarning: fetchWarning,
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

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
