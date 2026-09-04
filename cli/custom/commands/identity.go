package commands

import (
	"os"
	"strings"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

type IdentityWorkspace struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	TotalMembers int    `json:"total_members"`
}

type IdentityUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// IdentityCredential is what the credential this invocation would authenticate
// with says about itself. Read from the token's own claims, so it costs no
// round trip and stays honest when the key outranks the session.
type IdentityCredential struct {
	// Source is where the credential came from: "session", an environment
	// variable name, or a credentials profile.
	Source      string `json:"source"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	KeyID       string `json:"key_id,omitempty"`
	// ProjectID is set only for a credential scoped to one project.
	ProjectID string `json:"project_id,omitempty"`
	// Scope is what could be established about the credential's project reach:
	// scopeAllProjects, scopeProject, or scopeUnknown. An empty ProjectID is
	// not an answer on its own — the opaque sk-orq-<ULID>-<secret> shape
	// carries no scope at all, and reporting that silence as "all projects" is
	// an assertion nothing here can make.
	Scope string `json:"scope"`
}

const (
	scopeAllProjects = "all_projects"
	scopeProject     = "project"
	scopeUnknown     = "unknown"
)

type IdentityReport struct {
	Authenticated      bool                `json:"authenticated"`
	SessionFile        string              `json:"session_file"`
	User               *IdentityUser       `json:"user"`
	ActiveWorkspaceKey *string             `json:"active_workspace_key"`
	ActiveProjectID    string              `json:"active_project_id,omitempty"`
	ActiveProjectName  string              `json:"active_project_name,omitempty"`
	Workspaces         []IdentityWorkspace `json:"workspaces"`
	Server             string              `json:"server,omitempty"`
	Credential         *IdentityCredential `json:"credential,omitempty"`
	URLs               *auth.URLs          `json:"urls,omitempty"`
}

func BuildIdentityReport(session *auth.Session, urls *auth.URLs) IdentityReport {
	rep := IdentityReport{
		Authenticated:      true,
		SessionFile:        auth.SessionFilePath(),
		ActiveWorkspaceKey: session.ActiveWorkspaceKey,
		ActiveProjectID:    session.ActiveProjectID,
		ActiveProjectName:  session.ActiveProjectName,
		Server:             auth.Server(),
		Credential:         describeCredential(session),
		URLs:               urls,
		Workspaces:         []IdentityWorkspace{},
	}
	if session.User != nil {
		rep.User = &IdentityUser{
			ID:          session.User.ID,
			Email:       session.User.Email,
			DisplayName: session.User.DisplayName,
		}
	}
	for _, w := range session.Workspaces {
		rep.Workspaces = append(rep.Workspaces, workspaceFromMap(w))
	}
	return rep
}

// describeCredential names the credential the next command would actually
// authenticate with, and says what can be established about its scope.
//
// Which credential wins is not "is ORQ_API_KEY set": since RES-1465 the key
// `orq setup` minted and exported defers to the login session, which is the
// ordinary state on any machine that has run setup and sourced ~/.orq/env.
// explicitAPIKey is the post-deferral answer the custom PreRun already
// computed, so this reads that snapshot instead of re-deriving the rule and
// letting the two drift.
func describeCredential(session *auth.Session) *IdentityCredential {
	if !explicitAPIKey && session != nil {
		return describeSessionCredential(session)
	}
	key, source := ConfiguredCredential()
	if key == "" {
		return nil
	}
	claims := auth.InspectToken(key)
	return &IdentityCredential{
		Source:      source,
		WorkspaceID: claims.WorkspaceID,
		KeyID:       claims.KeyID,
		ProjectID:   claims.ProjectID(),
		Scope:       credentialScope(claims),
	}
}

// describeSessionCredential describes the session's own workspace token. Its
// scope is not guessed from the token shape: the session records which project
// it is pinned to, and that is what the token was fetched for.
func describeSessionCredential(session *auth.Session) *IdentityCredential {
	cred := &IdentityCredential{
		Source:    "session",
		ProjectID: strings.TrimSpace(session.ActiveProjectID),
		Scope:     scopeAllProjects,
	}
	if cred.ProjectID != "" {
		cred.Scope = scopeProject
	}
	// Local claim read, no round trip: it names the workspace the cached token
	// was actually minted for, which the session's workspace key alone cannot.
	if token := storedWorkspaceToken(session); token != "" {
		claims := auth.InspectToken(token)
		cred.WorkspaceID, cred.KeyID = claims.WorkspaceID, claims.KeyID
	}
	return cred
}

// ConfiguredCredential returns the key the next request would authenticate
// with and the name of where it came from, in the order bartolo's apikey
// handler resolves them: a profile in force first — bartolo does not fall
// through from a selected profile to an ambient environment key — then
// APIKeyEnvVars. Reading ORQ_API_KEY alone reported "no credential at all" for
// anyone whose key is ORQ_TOKEN or ORQ_AUTHORIZATION — describing a state no
// command runs in, which is the one thing `orq status` exists to prevent.
//
// ORQ_API_KEY comes from the snapshot rather than the environment, because our
// own PreRun injects the session token into that variable; the other two are
// never injected, so the live environment is the honest source for them.
func ConfiguredCredential() (key, source string) {
	if profileInForce() {
		if v := profileAPIKey(); v != "" {
			return v, "profile " + bartolocli.ActiveProfileName()
		}
		// A profile with no key authenticates nothing, and bartolo will not
		// reach past it: reporting an env key here would name a credential no
		// request is going to use.
		return "", ""
	}
	for _, envVar := range APIKeyEnvVars {
		v := strings.TrimSpace(os.Getenv(envVar))
		if envVar == APIKeyEnvVars[0] {
			v = UserEnvAPIKey()
		}
		if v != "" {
			return v, envVar
		}
	}
	return "", ""
}

func profileAPIKey() string {
	return strings.TrimSpace(bartolocli.GetProfile()["api_key"])
}

// credentialOutranksSession reports whether the credential in force is
// something other than the login session, which makes the session's own active
// project inert: no command will narrow a token to it.
func credentialOutranksSession(r IdentityReport) bool {
	return r.Credential != nil && r.Credential.Source != "session"
}

// credentialScope reports how far a credential reaches, distinguishing "every
// project" from "the token does not say". The opaque key shape yields a key id
// and nothing else, so an absent workspace id means the claims could not be
// read rather than that the key is workspace-wide.
func credentialScope(claims auth.TokenClaims) string {
	switch {
	case len(claims.Projects) > 0:
		return scopeProject
	case claims.WorkspaceID != "":
		return scopeAllProjects
	default:
		return scopeUnknown
	}
}

func workspaceFromMap(w map[string]any) IdentityWorkspace {
	out := IdentityWorkspace{}
	if v, ok := w["id"].(string); ok {
		out.ID = v
	}
	if v, ok := w["key"].(string); ok {
		out.Key = v
	}
	if v, ok := w["name"].(string); ok {
		out.Name = v
	}
	if v, ok := w["total_members"].(float64); ok {
		out.TotalMembers = int(v)
	}
	return out
}
