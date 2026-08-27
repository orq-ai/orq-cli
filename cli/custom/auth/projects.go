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

// gatewayAccessMap is the permission set for a key that only routes model
// calls: every domain in the catalog's GATEWAY group except mcp_gateway, which
// is MCP data-plane execution rather than gateway wiring.
//
// Held locally rather than fetched. The catalog endpoint (GET
// /v2/api-keys/capabilities, declared public in the platform-api proto) is
// unreachable in production: identity-api owns /v2/api-keys/* and its /:id
// route matches "capabilities", returning 404 "API Key not found." Fetching it
// meant every mint silently fell back to full permissions, which is the exact
// failure this map exists to prevent.
//
// Mirrors libs/catalog/orq/apikeys/v1/catalog.textpb in orquesta-web. A domain
// added there and missing here costs a capability, never a permission; the
// server ignores access-map entries it does not recognise.
var gatewayAccessMap = map[string]string{
	"chat_completions":    "write",
	"responses":           "write",
	"embeddings":          "write",
	"moderations":         "write",
	"images":              "write",
	"audio_speech":        "write",
	"audio_transcription": "write",
	"realtime":            "write",
	"rerank":              "write",
	"ocr":                 "write",
	"batches":             "write",
	"count_tokens":        "write",
	// Read-only in the catalog; write would resolve to nothing.
	"model": "read",
}

// GatewayAccess returns a copy, so a caller cannot mutate the shared map.
func GatewayAccess() map[string]string {
	out := make(map[string]string, len(gatewayAccessMap))
	for domain, level := range gatewayAccessMap {
		out[domain] = level
	}
	return out
}

// createAPIKeyBody builds the request the live API validates against, split out
// from CreateAPIKey so the owner and scope decisions are testable without a
// network call. An empty access map mints the legacy all-permissions key.
//
// Owner choice is the important part. A service-account key is workspace-owned
// and outlives its creator, but only workspace admins may create one — so
// asking for it unconditionally makes `orq setup` fail outright for a Developer
// or Researcher in an Enterprise workspace, which is most of the people the
// installer is aimed at. A user key is what the product documents for "personal
// use and local development", and any member can mint one. It is revoked if the
// user leaves the org, which for a key that only ever sits in one person's
// ~/.orq and coding-agent configs is the correct lifecycle, not a limitation.
//
// Falls back to a service account when the caller has no user id — an
// API-key-only run has no session and therefore nobody to attribute the key to.
//
// The request shape here deliberately does not match openapi.yaml: the live API
// uses discriminated unions keyed on `type`/`mode` and lowercase permission
// values, while the committed spec documents wrapper objects and
// PERMISSION_MODE_* constants. The spec is stale; this is what the server
// actually validates against (verified against /v2/api-keys, both owner types).
func createAPIKeyBody(req APIKeyRequest) (body map[string]any, scopedToProject bool) {
	scope := map[string]any{"mode": "all"}
	if ProjectScopable(req.projectID) {
		scope = map[string]any{
			"mode":       "single",
			"project_id": strings.TrimPrefix(req.projectID, "proj_"),
		}
		scopedToProject = true
	}
	owner := map[string]any{"type": "service_account"}
	if id := strings.TrimSpace(req.userID); id != "" {
		owner = map[string]any{"type": "user", "user_id": id}
	}
	// source is what actually restricts the key. identity-api, which serves this
	// route in production, never reads permission_mode or access — it discards
	// both and applies the catalog's router preset when, and only when, source is
	// "router". The two fields are still sent because platform-api's proto service
	// does honour them, and either may answer.
	out := map[string]any{
		"name":            req.name,
		"owner":           owner,
		"project_scope":   scope,
		"source":          "router",
		"permission_mode": "restricted",
		"access":          req.access,
	}
	// "expiration", not "expires_at": the live endpoint is identity-api's strict
	// decoder, which rejects the field name the committed spec documents.
	out["expiration"] = req.expiresAt.UTC().Format(time.RFC3339)
	return out, scopedToProject
}

// APIKeyRequest is what a mint decided. Construct it only through
// NewAPIKeyRequest: the two fields carrying blast radius have no safe default,
// and as a plain struct its zero value asked for a workspace-wide,
// never-expiring, all-permissions key owned by a service account — the widest
// key this API can issue, from the literal APIKeyRequest{Name: "x"}.
type APIKeyRequest struct {
	name      string
	projectID string
	userID    string
	access    map[string]string
	expiresAt time.Time
}

// APIKeyOption sets a field that does have a safe default.
type APIKeyOption func(*APIKeyRequest)

// WithUser attributes the key to a person. Without it the API mints against a
// service account, which only workspace admins may create; see createAPIKeyBody.
func WithUser(id string) APIKeyOption { return func(r *APIKeyRequest) { r.userID = id } }

// WithProject narrows the key to one project when the id is in the format this
// endpoint accepts.
func WithProject(id string) APIKeyOption { return func(r *APIKeyRequest) { r.projectID = id } }

// NewAPIKeyRequest refuses an empty access map and a zero expiry rather than
// reading either as "unrestricted" and "forever". Pass GatewayAccess() for the
// only kind of key the CLI mints today.
//
// There is deliberately no way to ask for an unrestricted or permanent key. The
// body always sends source: "router", which is what the server acts on, so an
// unrestricted variant would need a different request shape rather than a
// different argument. Add one when something needs it.
func NewAPIKeyRequest(name string, access map[string]string, expiresAt time.Time, opts ...APIKeyOption) (APIKeyRequest, error) {
	if strings.TrimSpace(name) == "" {
		return APIKeyRequest{}, errors.New("api key request needs a name")
	}
	if len(access) == 0 {
		return APIKeyRequest{}, errors.New("api key request needs an access map: auth.GatewayAccess()")
	}
	if expiresAt.IsZero() {
		return APIKeyRequest{}, errors.New("api key request needs an expiry")
	}
	req := APIKeyRequest{name: name, access: access, expiresAt: expiresAt}
	for _, opt := range opts {
		opt(&req)
	}
	return req, nil
}

// CreateAPIKey mints a key and returns the raw token, which the API returns
// exactly once — callers must persist it before doing anything else, along with
// keyID, which is equally unrecoverable afterwards.
//
// The key is scoped to projectID when that ID is in the format this endpoint
// accepts, and to the whole workspace otherwise. scopedToProject reports which
// happened so the caller can tell the user. userID attributes the key to a
// person; see createAPIKeyBody for why that is the default.
func (c *Client) CreateAPIKey(bearer string, req APIKeyRequest) (token, keyID string, scopedToProject bool, err error) {
	body, scopedToProject := createAPIKeyBody(req)
	// The live endpoint answers flat (`id` alongside `token`); the proto service
	// documented in openapi.yaml nests under `api_key`. Read both, then fall back
	// to the id carried in the token itself.
	var resp struct {
		Token  string `json:"token"`
		ID     string `json:"id"`
		APIKey struct {
			ID string `json:"id"`
		} `json:"api_key"`
	}
	if err := c.jsonRequest(http.MethodPost, c.URLs.APIBaseURL+"/v2/api-keys", bearer, body, &resp); err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(resp.Token) == "" {
		return "", "", false, errors.New("api key create returned no token")
	}
	for _, candidate := range []string{resp.ID, resp.APIKey.ID, keyIDFromToken(resp.Token)} {
		if keyID = strings.TrimSpace(candidate); keyID != "" {
			break
		}
	}
	return resp.Token, keyID, scopedToProject, nil
}

// keyIDFromToken reads the id out of `sk-orq-<api_key_id>-<secret>`, the opaque
// token shape. Router tokens share the `sk-orq-` prefix but carry a JWT after
// it, and base64url contains "-", so the id is only trusted when it is shaped
// like one. A wrong id is worse than none: it would be stored and later used to
// address somebody else's key.
func keyIDFromToken(token string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "sk-orq-")
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, "-")
	if !ok || !ulidPattern.MatchString(id) {
		return ""
	}
	return id
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
	// RefID is the canonical invoke id the endpoint publishes. It is
	// "provider/model_id" for system models but "workspace@orq/model_id" for
	// custom ones (autorouters), so it cannot be reconstructed from the two
	// fields below — see Ref.
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
// refId wins when the endpoint sends one. Composing provider/model_id instead
// yields "orq/<name>" for a workspace's custom models and autorouters, which
// the agents' model normalizers strip back to a bare, un-invokable name — so
// setup wrote config naming models that cannot be called, while `orq launch`
// (which reads refId, see launch.FetchEnabledModels) got them right.
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

// UsableForCodingAgent reports whether a model can be wired into a coding
// agent: in the workspace's enabled set, a chat model, and able to call tools.
// A coding agent edits files and runs commands through tools, so a model
// without function calling fails on the first real turn.
//
// Enabled, not Active: is_active is true for the whole catalogue, so filtering
// on it offered models the workspace had disabled, which a workspace with
// enforce_enabled_models on rejects outright.
//
// launch/gateway.go applies the same rule inline against its own response shape
// (ModelType rather than Type); the two have to change together.
func UsableForCodingAgent(m RouterModel) bool {
	return m.Enabled && m.Type == "chat" && m.Functions
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
			if !UsableForCodingAgent(m) {
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

// sizeVariantSuffixes name the cut-down editions vendors ship alongside a full
// model, ordered strongest to weakest. Order matters twice: -flash before
// -lite makes gemini-2.5-flash beat gemini-2.5-flash-lite, and -mini before
// -nano makes gpt-5.4-mini beat gpt-5.4-nano when a workspace enables only
// variants.
//
// They have to be ranked explicitly because size suffixes sort lexically
// *above* the model they are derived from — "gpt-5.4-nano" > "gpt-5.4-mini" >
// "gpt-5.4" — so picking the lexically greatest id hands a coding agent the
// weakest option in the family. That is not just a quality question:
// gpt-5.4-nano rejects tools the full model accepts, and codex, which sends
// its whole tool set on the first request, fails outright with "[openai] Tool
// 'tool_search' is not supported with gpt-5.4-nano".
//
// A name heuristic is unsatisfying, and it is here only because the catalogue
// cannot answer the question. /v2/models does publish
// metadata.supports_web_search, but for this family it is inverted:
// gpt-5.4-nano, the model that fails, advertises true, while gpt-5.4 and
// gpt-5.6-terra, which both work, leave it unset. Filtering on that field would
// select exactly the broken model. Replace this with a capability filter once
// the catalogue can be trusted for one.
var sizeVariantSuffixes = []string{"-flash", "-small", "-mini", "-lite", "-micro", "-tiny", "-nano"}

// SizeVariantRank orders a family's editions strongest-first: 0 for a full
// model, 1+ for cut-down ones, weaker editions ranking higher. Callers sort
// ascending by rank before any lexical comparison.
func SizeVariantRank(modelID string) int {
	for i, suffix := range sizeVariantSuffixes {
		// Suffix rather than Contains: the segment has to end the id, so a model
		// merely containing a size word mid-name is not demoted.
		if strings.HasSuffix(modelID, suffix) {
			return i + 1
		}
	}
	return 0
}

// APIKeyRecord is the metadata the API holds about one key. The raw secret is
// never part of it.
type APIKeyRecord struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	WorkspaceID string   `json:"workspace_id"`
	Projects    []string `json:"projects"`
	Status      string   `json:"status"`
	Active      *bool    `json:"active"`
}

// GetAPIKey looks a key up by id. Authenticate with the session's workspace
// token, not the key itself: a gateway key is minted with source "router",
// whose catalog preset has no api_keys access, so it cannot read its own
// record. The route is workspace-scoped, so a 404 here means the key is not in
// the workspace you are logged in to.
func (c *Client) GetAPIKey(bearer, keyID string) (*APIKeyRecord, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("no api key id to look up")
	}
	var rec APIKeyRecord
	url := c.URLs.APIBaseURL + "/v2/api-keys/" + keyID
	if err := c.jsonRequest(http.MethodGet, url, bearer, nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// KeyIDFromToken exposes the id carried in an opaque token, for callers holding
// a key but no saved id.
func KeyIDFromToken(token string) string { return keyIDFromToken(token) }

// NotFound reports whether err is the API saying the resource is not in this workspace.
func NotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
