package main

import (
	"bytes"
	"fmt"
	"io"
	"reflect"

	"github.com/samuelncui/yatm/internal/config"
	"github.com/samuelncui/yatm/internal/executor"
	yamlold "gopkg.in/yaml.v2"
	"gopkg.in/yaml.v3"
)

// convertConfiguration edits known keys while preserving unrelated YAML nodes and comments.
func convertConfiguration(original []byte, conf *config.Config, source, target string) ([]byte, error) {
	// Reject ambiguous documents before rewriting aliases or inherited configuration.
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(original))
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain exactly one YAML document")
	}
	var decoded map[string]any
	if err := document.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("invalid configuration mapping, %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration must be a YAML mapping")
	}
	root := document.Content[0]
	paths, err := configurationMapping(root, "paths")
	if err != nil {
		return nil, err
	}
	previews, err := configurationMapping(root, "preview")
	if err != nil {
		return nil, err
	}

	// Preserve effective access before removing legacy location defaults.
	expected := *conf
	changed := removeConfigurationKey(paths, "source")
	changed = removeConfigurationKey(paths, "target") || changed
	if changed {
		expected.Paths.Source, expected.Paths.Target = "", ""
		if conf.Paths.Access == nil {
			expected.Paths.Access = []executor.AccessRange{}
			for _, path := range []string{source, target} {
				if path == "" || len(expected.Paths.Access) > 0 && expected.Paths.Access[0].Root == path {
					continue
				}
				expected.Paths.Access = append(expected.Paths.Access, executor.AccessRange{Root: path})
			}
			var access yaml.Node
			if err := access.Encode(expected.Paths.Access); err != nil {
				return nil, err
			}
			removeConfigurationKey(paths, "access")
			paths.Content = append(paths.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "access"}, &access)
		}
	}
	if removeConfigurationKey(previews, "generators") {
		expected.Preview.Generators = nil
		changed = true
	}
	if !changed {
		return original, nil
	}

	// Re-parse with the service's loader semantics to verify only intended values changed.
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	var actual config.Config
	if err := yamlold.Unmarshal(output.Bytes(), &actual); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, actual) {
		return nil, fmt.Errorf("configuration conversion changed unrelated effective values; review YAML manually")
	}
	return output.Bytes(), nil
}

func configurationMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	for index := 0; index < len(parent.Content); index += 2 {
		if parent.Content[index].Value == "<<" {
			return nil, fmt.Errorf("configuration conversion requires explicit mappings; expand YAML merge keys first")
		}
		if parent.Content[index].Value != key {
			continue
		}
		node := parent.Content[index+1]
		if node.Kind != yaml.MappingNode && node.Tag != "!!null" {
			return nil, fmt.Errorf("configuration %s must be an explicit mapping", key)
		}
		for child := 0; child < len(node.Content); child += 2 {
			if node.Content[child].Value == "<<" || node.Content[child+1].Kind == yaml.AliasNode {
				return nil, fmt.Errorf("configuration %s contains aliases or merges; expand them before conversion", key)
			}
		}
		return node, nil
	}
	return nil, nil
}

func removeConfigurationKey(mapping *yaml.Node, key string) bool {
	if mapping == nil {
		return false
	}
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return true
		}
	}
	return false
}
