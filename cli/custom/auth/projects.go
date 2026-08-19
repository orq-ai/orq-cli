package auth

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Project mirrors the fields of the v2 Project schema that the CLI needs.
type Project struct {
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
	Key        string `json:"key"`
	IsArchived bool   `json:"is_archived"`
	IsDefault  bool   `json:"is_default"`
}

// projectPageLimit is the per-request page size. The endpoint caps it; the
// exact cap does not matter because ListProjects follows the cursor.
const projectPageLimit = 100

// maxProjectPages bounds the walk so a paging bug cannot turn into an endless
// loop. 100 pages is far more projects than any workspace has.
const maxProjectPages = 100

// ListProjects returns every project in the workspace, following the
// `starting_after` cursor. Reading only the first page would make a name lookup
// miss projects further down the list and create a duplicate instead.
func (c *Client) ListProjects(bearer string) ([]Project, error) {
	var all []Project
	cursor := ""
	for page := 0; page < maxProjectPages; page++ {
		url := fmt.Sprintf("%s/v2/projects?limit=%d", c.URLs.APIBaseURL, projectPageLimit)
		if cursor != "" {
			url += "&starting_after=" + cursor
		}
		var resp struct {
			Data    []Project `json:"data"`
			HasMore bool      `json:"has_more"`
		}
		if err := c.jsonRequest(http.MethodGet, url, bearer, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if !resp.HasMore || len(resp.Data) == 0 {
			return all, nil
		}
		cursor = resp.Data[len(resp.Data)-1].ProjectID
	}
	return all, nil
}

func (c *Client) CreateProject(bearer, name, description string) (*Project, error) {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	var resp struct {
		Project Project `json:"project"`
	}
	err := c.jsonRequest(http.MethodPost, c.URLs.APIBaseURL+"/v2/projects", bearer, body, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Project.ProjectID == "" {
		return nil, errors.New("project create returned no project_id")
	}
	return &resp.Project, nil
}

// ulidPattern is the project_id format /v2/api-keys accepts. Note that
// /v2/projects currently returns UUIDs, which this endpoint rejects — see
// CreateAPIKey.
var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ProjectScopable reports whether an API key can be scoped to this project.
// The two endpoints disagree about ID formats today, so callers must be
// prepared to fall back to a workspace-wide key.
func ProjectScopable(projectID string) bool {
	return ulidPattern.MatchString(strings.TrimPrefix(projectID, "proj_"))
}

// CreateAPIKey mints a key and returns the raw token, which the API returns
// exactly once — callers must persist it before doing anything else.
//
// The key is scoped to projectID when that ID is in the format this endpoint
// accepts, and to the whole workspace otherwise. scopedToProject reports which
// happened so the caller can tell the user.
//
// The request shape here deliberately does not match openapi.yaml: the live API
// uses discriminated unions keyed on `type`/`mode` and lowercase permission
// values, while the committed spec documents wrapper objects and
// PERMISSION_MODE_* constants. The spec is stale; this is what the server
// actually validates against.
func (c *Client) CreateAPIKey(bearer, name, projectID string) (token string, scopedToProject bool, err error) {
	scope := map[string]any{"mode": "all"}
	if ProjectScopable(projectID) {
		scope = map[string]any{
			"mode":       "single",
			"project_id": strings.TrimPrefix(projectID, "proj_"),
		}
		scopedToProject = true
	}
	body := map[string]any{
		"name":            name,
		"owner":           map[string]any{"type": "service_account"},
		"project_scope":   scope,
		"permission_mode": "all",
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := c.jsonRequest(http.MethodPost, c.URLs.APIBaseURL+"/v2/api-keys", bearer, body, &resp); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(resp.Token) == "" {
		return "", false, errors.New("api key create returned no token")
	}
	return resp.Token, scopedToProject, nil
}

// MCPServerURL is the workspace MCP endpoint for the host this session
// authenticated against, so self-hosted installs get their own URL.
func (c *Client) MCPServerURL() string {
	return c.URLs.APIBaseURL + "/v2/mcp"
}

// RouterBaseURL is the OpenAI-compatible gateway base. Coding agents that speak
// OpenAI can point straight at it and have every call routed through orq.
//
// v3 is the documented base (https://docs.orq.ai/docs/ai-gateway/features/openai-compatible-api)
// and what `orq launch` wires into agents; v2 still answers, but the two must
// not drift apart.
func (c *Client) RouterBaseURL() string {
	return c.URLs.APIBaseURL + "/v3/router"
}

// RouterModel is one model offered by the AI gateway. The router addresses
// models by their refId, which is "<provider>/<model_id>" for system models.
type RouterModel struct {
	ModelID string `json:"model_id"`
	// RefID is the canonical invoke id: "provider/model_id" for system models but
	// "workspace@orq/model_id" for custom ones, so it cannot be reconstructed.
	RefID     string `json:"refId"`
	Provider  string `json:"provider"`
	Developer string `json:"model_developer"`
	Type      string `json:"model_type"`
	// Active is a catalogue property: every entry the gateway knows about is
	// active, including models this workspace has not enabled. Filtering on it
	// is almost never what you want — use Enabled.
	Active bool `json:"is_active"`
	// Enabled is the workspace's enabled set, the one `enforce_enabled_models`
	// checks and the only models a caller is guaranteed to be allowed to use.
	Enabled   bool `json:"enabled"`
	Functions bool `json:"has_functions"`
	Metadata  struct {
		ContextWindow int `json:"context_window"`
		// MaxOutputTokens and SupportsResponses are published per model and
		// were previously dropped here, which left setup unable to write an
		// output cap or pick the right API shape — data the endpoint returns
		// and a struct field is all it takes to keep.
		MaxOutputTokens   int  `json:"max_output_tokens"`
		SupportsResponses bool `json:"supports_responses_api"`
	} `json:"metadata"`
}

// Ref is the identifier the router expects in a request's `model` field.
//
// refId wins when the endpoint sends one: composing provider/model_id yields
// "orq/<name>" for custom models, which the agents' normalizers strip back to a
// bare, un-invokable name.
func (m RouterModel) Ref() string {
	if m.RefID != "" {
		return m.RefID
	}
	return m.Provider + "/" + m.ModelID
}

// ListModels returns the gateway's model catalogue. The endpoint answers with a
// bare array rather than the usual {data: []} envelope.
func (c *Client) ListModels(bearer string) ([]RouterModel, error) {
	var models []RouterModel
	err := c.jsonRequest(http.MethodGet, c.URLs.APIBaseURL+"/v2/models?limit=1000", bearer, nil, &models)
	if err != nil {
		return nil, err
	}
	return models, nil
}

// CandidateCodingModels groups the catalogue by preferred prefix, best-first
// within each group, so a caller can try candidates in order. Only tool-capable
// active chat models are eligible — a coding agent is useless without function
// calling.
//
// "Best" is the lexically greatest model_id, which tracks version suffixes
// (claude-sonnet-4-6 over -4-5, kimi-k2.6 over k2.5) — but stronger editions
// are ranked above cut-down ones first, see SizeVariantRank.
func CandidateCodingModels(models []RouterModel, preferred []string) [][]RouterModel {
	groups := make([][]RouterModel, 0, len(preferred))
	for _, prefix := range preferred {
		matches := []RouterModel{}
		for _, m := range models {
			// Enabled, not Active: is_active is true for the whole catalogue,
			// so filtering on it offered models the workspace had disabled —
			// which a workspace with enforce_enabled_models on rejects outright.
			if !m.Enabled || m.Type != "chat" || !m.Functions {
				continue
			}
			if strings.HasPrefix(m.Ref(), prefix) {
				matches = append(matches, m)
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			// Stronger edition first (lower rank), newest version within it.
			if a, b := SizeVariantRank(matches[i].ModelID), SizeVariantRank(matches[j].ModelID); a != b {
				return a < b
			}
			return matches[i].ModelID > matches[j].ModelID
		})
		if len(matches) > 0 {
			groups = append(groups, matches)
		}
	}
	return groups
}

// sizeVariantSuffixes are the cut-down editions vendors ship alongside a full
// model, ordered strongest to weakest. They must be ranked explicitly because a
// size suffix sorts lexically above the model it comes from ("gpt-5.4-nano" >
// "gpt-5.4"), so picking the greatest id hands a coding agent the weakest
// option: gpt-5.4-nano rejects tools codex sends on its first request.
//
// A name heuristic only because the catalogue cannot answer the question:
// metadata.supports_web_search is inverted for this family.
var sizeVariantSuffixes = []string{"-flash", "-small", "-mini", "-lite", "-micro", "-tiny", "-nano"}

// SizeVariantRank orders a family's editions strongest-first: 0 for a full
// model, 1+ for cut-down ones. Callers sort ascending before any lexical compare.
func SizeVariantRank(modelID string) int {
	for i, suffix := range sizeVariantSuffixes {
		if strings.HasSuffix(modelID, suffix) {
			return i + 1
		}
	}
	return 0
}

// probeMaxTokens must leave room for a complete reply. The gateway rejects a
// truncated generation with 400 "max_tokens or model output limit was reached",
// so a smaller budget fails every probe regardless of whether the model works.
// Reasoning models spend their first tokens on reasoning, hence the headroom.
const probeMaxTokens = 16

// ProbeModel reports whether the gateway can actually serve this model. The
// catalogue lists models that return 500 on use, so a config written from the
// catalogue alone would advertise models that do not work.
func (c *Client) ProbeModel(bearer, ref string) bool {
	_, err := c.TimeModel(bearer, ref)
	return err == nil
}

// TimeModel is ProbeModel with the round-trip time, for reporting a first
// successful gateway request back to the user.
func (c *Client) TimeModel(bearer, ref string) (time.Duration, error) {
	body := map[string]any{
		"model":      ref,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": probeMaxTokens,
	}
	client := &Client{URLs: c.URLs, HTTPClient: &http.Client{Timeout: 20 * time.Second}}
	started := time.Now()
	err := client.jsonRequest(
		http.MethodPost,
		c.RouterBaseURL()+"/chat/completions",
		bearer,
		body,
		nil,
	)
	return time.Since(started), err
}
