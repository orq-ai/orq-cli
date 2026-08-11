package auth

import (
	"errors"
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

func (c *Client) ListProjects(bearer string) ([]Project, error) {
	var resp struct {
		Data []Project `json:"data"`
	}
	err := c.jsonRequest(http.MethodGet, c.URLs.APIBaseURL+"/v2/projects", bearer, nil, &resp)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
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
func (c *Client) RouterBaseURL() string {
	return c.URLs.APIBaseURL + "/v2/router"
}

// RouterModel is one model offered by the AI gateway. The router addresses
// models as "<provider>/<model_id>".
type RouterModel struct {
	ModelID   string `json:"model_id"`
	Provider  string `json:"provider"`
	Developer string `json:"model_developer"`
	Type      string `json:"model_type"`
	Active    bool   `json:"is_active"`
	Functions bool   `json:"has_functions"`
	Metadata  struct {
		ContextWindow int `json:"context_window"`
	} `json:"metadata"`
}

// Ref is the identifier the router expects in a request's `model` field.
func (m RouterModel) Ref() string {
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
// (claude-sonnet-4-6 over -4-5, kimi-k2.6 over k2.5).
func CandidateCodingModels(models []RouterModel, preferred []string) [][]RouterModel {
	groups := make([][]RouterModel, 0, len(preferred))
	for _, prefix := range preferred {
		matches := []RouterModel{}
		for _, m := range models {
			if !m.Active || m.Type != "chat" || !m.Functions {
				continue
			}
			if strings.HasPrefix(m.Ref(), prefix) {
				matches = append(matches, m)
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			return matches[i].ModelID > matches[j].ModelID
		})
		if len(matches) > 0 {
			groups = append(groups, matches)
		}
	}
	return groups
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
