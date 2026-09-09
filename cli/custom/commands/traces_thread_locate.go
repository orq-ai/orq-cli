package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"orq/cli/custom/auth"

	"github.com/spf13/viper"
)

// threadSearchWindow is how far back the search looks. Trace search keeps 30
// days and rejects a wider range outright, so asking for more finds nothing.
const threadSearchWindow = 29 * 24 * time.Hour

// locateThreadProject names the project a trace lives in, for a 404 that
// project scoping caused. Reads by id answer only within the project the token
// is scoped to, so the trace is not gone — it is out of reach from here.
//
// It reports rather than retries: switching project changes the scope of every
// later call in the session, and silently answering this one read from another
// project would leave the next one failing the same way with no explanation.
func locateThreadProject(api TraceAPI, traceID string, params *viper.Viper) string {
	if api.SearchTraces == nil || explicitAPIKey {
		return ""
	}
	session, err := auth.ReadSession()
	if err != nil || session == nil || session.ActiveWorkspaceKey == nil {
		return ""
	}
	// An unscoped session already searches the whole workspace, so a 404 here
	// is a trace that does not exist rather than one behind a scope.
	if session.ActiveProjectID == "" {
		return ""
	}
	client := auth.NewClient(sessionAPIBase(session))
	token, err := client.WorkspaceToken(session, *session.ActiveWorkspaceKey)
	if err != nil {
		return ""
	}
	summaries, err := searchWorkspaceTrace(api, token, traceID, params)
	if err != nil || len(summaries) == 0 {
		return ""
	}
	projectID := threadString(summaries[0]["project_id"])
	if projectID == "" || projectID == session.ActiveProjectID {
		return ""
	}
	return describeThreadProject(client, token, projectID)
}

// searchWorkspaceTrace runs one workspace-wide search for the trace. The
// workspace token is unscoped, and scope travels in the token rather than the
// request, so it is swapped in for this call and put back after: the generated
// operations read the key from the environment on every request.
func searchWorkspaceTrace(api TraceAPI, token, traceID string, params *viper.Viper) ([]map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"from":    time.Now().Add(-threadSearchWindow).UTC().Format(time.RFC3339),
		"to":      time.Now().UTC().Format(time.RFC3339),
		"filters": []any{map[string]any{"field": "trace_id", "op": "eq", "values": []string{traceID}}},
		"limit":   1,
	})
	if err != nil {
		return nil, err
	}
	previous, had := os.LookupEnv(APIKeyEnvVars[0])
	if err := os.Setenv(APIKeyEnvVars[0], token); err != nil {
		return nil, err
	}
	defer func() {
		if had {
			os.Setenv(APIKeyEnvVars[0], previous)
			return
		}
		os.Unsetenv(APIKeyEnvVars[0])
	}()
	response, err := api.SearchTraces(string(body), params)
	if err != nil {
		return nil, err
	}
	return listEnvelopeData(response), nil
}

// describeThreadProject turns the project id the search returned into the key
// `orq projects use` takes, and says what to do with it.
func describeThreadProject(client *auth.Client, token, projectID string) string {
	name, key := projectID, ""
	if projects, err := client.ListProjects(token); err == nil {
		for _, project := range projects {
			if project.ProjectID == projectID {
				name, key = project.Name, project.Key
				break
			}
		}
	}
	if key == "" {
		return fmt.Sprintf("\nThis trace is in project %s, not the active one. Switch with `orq projects use <key>`: reads by id answer within the active project only.", projectID)
	}
	return fmt.Sprintf("\nThis trace is in project %q (%s), not the active one. Run `orq projects use %s` and try again: reads by id answer within the active project only, so the rest of the session needs the switch too.", name, key, key)
}
