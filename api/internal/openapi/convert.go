// Package openapi converts the YAML contract to JSON without reordering it.
package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// JSON converts a YAML document to indented JSON, keeping mapping order.
func JSON(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := write(&buf, &doc, ""); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func write(buf *bytes.Buffer, n *yaml.Node, path string) error {
	switch n.Kind {
	case yaml.DocumentNode:
		return write(buf, n.Content[0], path)
	case yaml.AliasNode:
		return write(buf, n.Alias, path)
	case yaml.MappingNode:
		buf.WriteByte('{')
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if seen[key] {
				return fmt.Errorf("line %d: duplicate key %q in %s", n.Content[i].Line, key, path)
			}
			seen[key] = true
			if i > 0 {
				buf.WriteByte(',')
			}
			k, _ := json.Marshal(key)
			buf.Write(k)
			buf.WriteByte(':')
			if err := write(buf, n.Content[i+1], path+"/"+key); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, c, path+"/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		buf.Write(b)
	default:
		return fmt.Errorf("line %d: unsupported YAML node", n.Line)
	}
	return nil
}
