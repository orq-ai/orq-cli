package commands

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"

	yaml "go.yaml.in/yaml/v3"
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

// ompReadDoc parses models.yml into a document node whose content is a
// mapping. A missing or empty file is a fresh empty mapping. A comment-only
// file parses to no document at all, so it is re-read with an empty mapping
// appended, which the comments attach to; a null root is turned into an empty
// mapping in place. Either way the user's comments survive. Parsing into a
// yaml.Node, never a map, is what keeps their comments and key order.
func ompReadDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML — left untouched: %w", path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		if len(bytes.TrimSpace(data)) > 0 {
			// yaml.v3 drops a comment-only stream; "{}" gives the comments a node to ride on.
			var withRoot yaml.Node
			if err := yaml.Unmarshal(append(bytes.TrimRight(data, "\n"), "\n\n{}\n"...), &withRoot); err == nil && len(withRoot.Content) == 1 && withRoot.Content[0].Kind == yaml.MappingNode {
				withRoot.Content[0].Style = 0
				return &withRoot, nil
			}
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	if isNullNode(doc.Content[0]) {
		root := doc.Content[0]
		root.Kind, root.Tag, root.Value, root.Style = yaml.MappingNode, "!!map", "", 0
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s is valid YAML but not a mapping — left untouched", path)
	}
	return &doc, nil
}

func isNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// mapIndex returns the index of key's value within a mapping's Content, or -1.
func mapIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

// ompProvidersNode returns the providers mapping. A missing key (or null
// value) is created when create is set; anything else that is not a mapping is an error.
func ompProvidersNode(path string, root *yaml.Node, create bool) (*yaml.Node, error) {
	i := mapIndex(root, "providers")
	switch {
	case i < 0:
		if !create {
			return nil, nil
		}
		providers := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "providers"}, providers)
		return providers, nil
	case isNullNode(root.Content[i]):
		if !create {
			return nil, nil
		}
		root.Content[i] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return root.Content[i], nil
	case root.Content[i].Kind != yaml.MappingNode:
		return nil, fmt.Errorf("%s: providers is not a mapping — left untouched", path)
	}
	return root.Content[i], nil
}

func encodeYAMLDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// plainStyle drops the flow/quoted styling the JSON source parsed with, so the
// spliced block reads as block-style YAML like the rest of the file. The
// encoder re-quotes any string that would otherwise resolve to another type.
func plainStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plainStyle(c)
	}
}

// ompGeneratedProvider builds the providers.orq subtree from the same builder
// `orq launch omp` uses; its JSON output is valid YAML.
func ompGeneratedProvider(routerURL string, models []auth.RouterModel) (*yaml.Node, error) {
	refs, infos := launchCatalog(models)
	built, err := launch.BuildPiModelsJSON(routerURL, refs, infos)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(built), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("generated omp provider config is not a mapping")
	}
	providers := mapIndex(doc.Content[0], "providers")
	if providers < 0 || doc.Content[0].Content[providers].Kind != yaml.MappingNode {
		return nil, errors.New("generated omp provider config has no providers")
	}
	block := mapIndex(doc.Content[0].Content[providers], launch.OmpProvider)
	if block < 0 {
		return nil, errors.New("generated omp provider config has no orq provider")
	}
	node := doc.Content[0].Content[providers].Content[block]
	plainStyle(node)
	return node, nil
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
	block, err := ompGeneratedProvider(routerURL, models)
	if err != nil {
		return 0, err
	}
	doc, err := ompReadDoc(path)
	if err != nil {
		return 0, err
	}
	providers, err := ompProvidersNode(path, doc.Content[0], true)
	if err != nil {
		return 0, err
	}
	// Replace in place so a rerun neither merges into a stale model list nor moves the key.
	if i := mapIndex(providers, launch.OmpProvider); i >= 0 {
		providers.Content[i] = block
	} else {
		providers.Content = append(providers.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: launch.OmpProvider}, block)
	}
	data, err := encodeYAMLDoc(doc)
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
	doc, err := ompReadDoc(path)
	if err != nil {
		return false
	}
	providers, err := ompProvidersNode(path, doc.Content[0], false)
	if err != nil || providers == nil {
		return false
	}
	return mapIndex(providers, launch.OmpProvider) >= 0
}

// removeOmpProvider is writeOmpProviderYAML's inverse, under removeJSONKeys's
// rules: a malformed file or a non-mapping providers value is refused, and the
// file is deleted only when we created it (no .orq-bak) and nothing is left.
func removeOmpProvider(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	doc, err := ompReadDoc(path)
	if err != nil {
		return false, err
	}
	root := doc.Content[0]
	providers, err := ompProvidersNode(path, root, false)
	if err != nil {
		return false, err
	}
	if providers == nil {
		return false, nil
	}
	i := mapIndex(providers, launch.OmpProvider)
	if i < 0 {
		return false, nil
	}
	providers.Content = append(providers.Content[:i-1], providers.Content[i+1:]...)
	if len(providers.Content) == 0 {
		j := mapIndex(root, "providers")
		root.Content = append(root.Content[:j-1], root.Content[j+1:]...)
	}
	if len(root.Content) == 0 {
		if _, err := os.Stat(path + ".orq-bak"); errors.Is(err, os.ErrNotExist) {
			return true, os.Remove(path)
		}
	}
	data, err := encodeYAMLDoc(doc)
	if err != nil {
		return false, err
	}
	return true, writeConfigFile(path, data, mcpFileMode(path))
}
