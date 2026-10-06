package launch

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// OmpReadDoc parses models.yml into a document node whose content is a
// mapping. A missing or empty file is a fresh empty mapping. A comment-only
// file parses to no document at all, so it is re-read with an empty mapping
// appended, which the comments attach to; a null root is turned into an empty
// mapping in place. Parsing into a yaml.Node, never a map, is what keeps the
// user's comments and key order.
func OmpReadDoc(path string) (*yaml.Node, error) {
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

// YAMLMapIndex returns the index of key's value within a mapping's Content, or -1.
func YAMLMapIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

// OmpProvidersNode returns the providers mapping. A missing key (or null
// value) is created when create is set; anything else that is not a mapping is an error.
func OmpProvidersNode(path string, root *yaml.Node, create bool) (*yaml.Node, error) {
	i := YAMLMapIndex(root, "providers")
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

func EncodeYAMLDoc(doc *yaml.Node) ([]byte, error) {
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

// ompProviderBlock pulls providers.orq out of BuildPiModelsJSON's output
// (JSON, so valid YAML).
func ompProviderBlock(generated string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(generated), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("generated omp provider config is not a mapping")
	}
	providers := YAMLMapIndex(doc.Content[0], "providers")
	if providers < 0 || doc.Content[0].Content[providers].Kind != yaml.MappingNode {
		return nil, errors.New("generated omp provider config has no providers")
	}
	block := YAMLMapIndex(doc.Content[0].Content[providers], OmpProvider)
	if block < 0 {
		return nil, errors.New("generated omp provider config has no orq provider")
	}
	node := doc.Content[0].Content[providers].Content[block]
	plainStyle(node)
	return node, nil
}

// OmpMergeProvider returns the models.yml at path (missing = empty) with
// providers.orq set to the block in generated, BuildPiModelsJSON's output.
// The user's other providers, comments and key order survive; an existing
// providers.orq is replaced in place so a rerun neither merges into a stale
// model list nor moves the key. path itself is only read.
func OmpMergeProvider(path, generated string) ([]byte, error) {
	block, err := ompProviderBlock(generated)
	if err != nil {
		return nil, err
	}
	doc, err := OmpReadDoc(path)
	if err != nil {
		return nil, err
	}
	providers, err := OmpProvidersNode(path, doc.Content[0], true)
	if err != nil {
		return nil, err
	}
	if i := YAMLMapIndex(providers, OmpProvider); i >= 0 {
		providers.Content[i] = block
	} else {
		providers.Content = append(providers.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: OmpProvider}, block)
	}
	return EncodeYAMLDoc(doc)
}
