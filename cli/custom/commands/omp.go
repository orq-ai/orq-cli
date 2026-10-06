package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
)

// ompPath resolves inside omp's agent directory: $PI_CODING_AGENT_DIR when set,
// ~/.omp/agent otherwise — the same order omp resolves it. The variable is
// shared with pi, so launch.OmpPiOwned decides whose directory it is.
func ompPath(rel string) func(bool) (string, error) {
	return func(bool) (string, error) {
		if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
			return filepath.Join(dir, rel), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".omp", "agent", rel), nil
	}
}

// ompDetect mirrors ompPath so detection and the write agree on one directory.
func ompDetect() bool {
	dir, err := ompPath("")(true)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir() && !launch.OmpPiOwned(dir)
}

// ompMCPEntry is omp's http MCP server shape: exactly type and url. No
// headers; omp logs in through its own OAuth flow (/mcp).
func ompMCPEntry(url string) map[string]any {
	return map[string]any{"type": "http", "url": url}
}

// writeOmpProviderYAML registers the gateway in omp's models.yml, the durable
// twin of the throwaway one `orq launch omp` writes. Spliced into the user's
// document as yaml.Node so their comments, key order and other providers
// survive. apiKey and defaultModel are unused for the reasons
// writePiProviderJSON documents: the block carries "$ORQ_API_KEY" and omp
// keeps the model it opens with in its own settings.
func writeOmpProviderYAML(path, routerURL, _ string, models []auth.RouterModel, _ string) (int, error) {
	if len(models) == 0 {
		return 0, errNoModelsToOffer
	}
	if dir := filepath.Dir(path); launch.OmpPiOwned(dir) {
		return 0, fmt.Errorf("%s holds pi's models.json, not omp's: PI_CODING_AGENT_DIR points at pi's directory — unset it or point it at a different directory for omp", dir)
	}
	refs, infos := launchCatalog(models)
	built, err := launch.BuildPiModelsJSON(routerURL, refs, infos)
	if err != nil {
		return 0, err
	}
	data, err := launch.OmpMergeProvider(path, built)
	if err != nil {
		return 0, err
	}
	return len(models), writeConfigFile(path, data, mcpFileMode(path))
}

// writeOmpMCP carries the ownership rule onto the MCP write: a dir
// pi owns is refused here too, so an explicit 'orq connect omp mcp'
// cannot drop omp's entry into pi's directory. The provider writer
// refuses the same dir with the same error.
func writeOmpMCP(path, url string) error {
	if dir := filepath.Dir(path); launch.OmpPiOwned(dir) {
		return fmt.Errorf("%s holds pi's models.json, not omp's: PI_CODING_AGENT_DIR points at pi's directory — unset it or point it at a different directory for omp", dir)
	}
	return writeMCPJSON("mcpServers", ompMCPEntry)(path, url)
}

// ompProviderPresent is false when the file is absent, unparseable or holds no providers.orq.
func ompProviderPresent(path string) bool {
	doc, err := launch.OmpReadDoc(path)
	if err != nil {
		return false
	}
	providers, err := launch.OmpProvidersNode(path, doc.Content[0], false)
	if err != nil || providers == nil {
		return false
	}
	return launch.YAMLMapIndex(providers, launch.OmpProvider) >= 0
}

// removeOmpProvider is writeOmpProviderYAML's inverse, under removeJSONKeys's
// rules: a malformed file or a non-mapping providers value is refused, and the
// file is deleted only when we created it (no .orq-bak) and nothing is left.
func removeOmpProvider(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	doc, err := launch.OmpReadDoc(path)
	if err != nil {
		return false, err
	}
	root := doc.Content[0]
	providers, err := launch.OmpProvidersNode(path, root, false)
	if err != nil {
		return false, err
	}
	if providers == nil {
		return false, nil
	}
	i := launch.YAMLMapIndex(providers, launch.OmpProvider)
	if i < 0 {
		return false, nil
	}
	providers.Content = append(providers.Content[:i-1], providers.Content[i+1:]...)
	if len(providers.Content) == 0 {
		j := launch.YAMLMapIndex(root, "providers")
		root.Content = append(root.Content[:j-1], root.Content[j+1:]...)
	}
	if len(root.Content) == 0 {
		if _, err := os.Stat(path + ".orq-bak"); errors.Is(err, os.ErrNotExist) {
			return true, os.Remove(path)
		}
	}
	data, err := launch.EncodeYAMLDoc(doc)
	if err != nil {
		return false, err
	}
	return true, writeConfigFile(path, data, mcpFileMode(path))
}
