package commands

import (
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
	// ProjectID is set only for a credential scoped to one project; empty
	// means it reaches every project in the workspace.
	ProjectID string `json:"project_id,omitempty"`
}

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
		Credential:         describeCredential(),
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

// describeCredential names the credential a command would authenticate with
// right now and reads its scope out of the token. Returns nil for the session,
// whose scope the report already states as the active workspace and project.
func describeCredential() *IdentityCredential {
	key := UserEnvAPIKey()
	source := "ORQ_API_KEY"
	if key == "" {
		key = strings.TrimSpace(bartolocli.GetProfile()["api_key"])
		source = "profile " + auth.ActiveProfile()
	}
	if key == "" {
		return nil
	}
	claims := auth.InspectToken(key)
	return &IdentityCredential{
		Source:      source,
		WorkspaceID: claims.WorkspaceID,
		KeyID:       claims.KeyID,
		ProjectID:   claims.ProjectID(),
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
