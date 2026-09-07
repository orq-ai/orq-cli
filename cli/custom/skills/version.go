package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type sourceMetadata struct {
	Commit string `json:"commit"`
}

// installedVersion names the exact bundle an existing projection uses. Source
// metadata is diagnostic, so damage falls back to the refresh fingerprint.
func installedVersion(m *Manifest) string {
	fallback := strings.TrimSpace(m.Fingerprint)
	if strings.TrimSpace(m.Generation) == "" {
		return fallback
	}
	data, err := os.ReadFile(filepath.Join(m.Generation, "SOURCE.json"))
	if err != nil {
		return fallback
	}
	var source sourceMetadata
	if json.Unmarshal(data, &source) != nil {
		return fallback
	}
	if commit := strings.TrimSpace(source.Commit); commit != "" {
		return commit
	}
	return fallback
}
