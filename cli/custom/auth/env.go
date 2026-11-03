package auth

import "strings"

// APIKeyEnvVars preserves the legacy precedence before the Toqan alias.
var APIKeyEnvVars = []string{"ORQ_API_KEY", "ORQ_TOKEN", "ORQ_AUTHORIZATION", "TOQAN_API_KEY"}

// EnvironmentAPIKey returns the first configured key and its variable name.
func EnvironmentAPIKey(getenv func(string) string) (string, string) {
	for _, name := range APIKeyEnvVars {
		if key := strings.TrimSpace(getenv(name)); key != "" {
			return key, name
		}
	}
	return "", ""
}
