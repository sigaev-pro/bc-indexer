// The strict YAML reader of the deployment schemas: unknown and
// duplicate keys are rejected on every level, exactly as LIVE_CONFIG_SPEC
// demands for the collector and gateway files.
package deployconfig

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecodeStrict decodes one YAML document into out: the document must be
// a mapping, must hold no duplicate keys anywhere, and every key must be
// known to the destination struct. There is no environment substitution
// and no fallback: a schema violation is an error, never a default.
func DecodeStrict(data []byte, out any) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("yaml: document is empty")
	}
	if doc.Kind != yaml.DocumentNode {
		return fmt.Errorf("yaml: document is not a document node")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("yaml: document root is not a mapping")
	}
	if err := rejectDuplicateKeys(root, "$"); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	return nil
}

// rejectDuplicateKeys walks the parsed tree and rejects any mapping that
// sets the same key twice, at any depth.
func rejectDuplicateKeys(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("yaml: %s holds a non-scalar key", path)
			}
			if seen[key.Value] {
				return fmt.Errorf("yaml: duplicate key %q under %s", key.Value, path)
			}
			seen[key.Value] = true
			child := node.Content[i+1]
			if err := rejectDuplicateKeys(child, path+"."+key.Value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := rejectDuplicateKeys(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
