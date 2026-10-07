package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// clipBlocks drops the one newline a yaml | or > block ends with (clip,
// yaml's default chomping): `command: >` over two lines is one line of
// words, and a newline at the end of a value carries nothing here. A
// newline inside a value is still refused, by interpolateTree.
func clipBlocks(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 && strings.HasSuffix(n.Value, "\n") && !strings.HasSuffix(n.Value, "\n\n") {
		n.Value = strings.TrimSuffix(n.Value, "\n")
	}
	for _, c := range n.Content {
		clipBlocks(c)
	}
}

// yamlError words the yaml library's parse errors for the slips they
// leave unexplained: a tab in the indentation (yaml allows only spaces); an
// indentation error, which yaml reports at the line where the block began
// rather than at the line that is off; and a value that starts with %,
// which yaml reserves at the start of a plain value (a specifier-led path
// is the likeliest way to write one).
func yamlError(err error, data []byte) error {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	line := "" // the line yaml names, when it names one
	var n int
	if _, err := fmt.Sscanf(msg, "line %d:", &n); err == nil {
		if lines := strings.Split(string(data), "\n"); n >= 1 && n <= len(lines) {
			line = lines[n-1]
		}
	}
	if strings.Contains(line[:len(line)-len(strings.TrimLeft(line, " \t"))], "\t") {
		return fmt.Errorf("line %d: a tab in the indentation; yaml indents with spaces only", n)
	}
	for _, s := range []string{"did not find expected key", "could not find expected ':'", "mapping values are not allowed"} {
		if strings.Contains(msg, s) {
			return fmt.Errorf("%s (yaml names the line where the block began: look below it for a line indented deeper or shallower than its neighbours)", msg)
		}
	}
	if strings.Contains(msg, "did not find expected ','") {
		return fmt.Errorf("%s (the [ or { that is not closed is on that line or below it)", msg)
	}
	if strings.Contains(msg, "cannot start any token") && (strings.Contains(line, ": %") || strings.Contains(line, "- %")) {
		return fmt.Errorf("%s (a value that starts with %% must be quoted in yaml: \"%%h/...\")", msg)
	}
	return errors.New(msg)
}

// ---- yaml node helpers ----------------------------------------------------

type kvNode struct{ key, value *yaml.Node }

type mapNode struct{ pairs []kvNode }

func (m *mapNode) get(key string) *yaml.Node {
	for _, kv := range m.pairs {
		if kv.key.Value == key {
			return kv.value
		}
	}
	return nil
}

func mapping(n *yaml.Node, ctx string) (*mapNode, error) {
	n = unalias(n)
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: %s: expected a map", n.Line, ctx)
	}
	m := &mapNode{}
	seen := map[string]int{}
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: %s: keys must be plain words", k.Line, ctx)
		}
		if k.Tag == "!!merge" {
			merges = append(merges, n.Content[i+1])
			continue
		}
		if first, dup := seen[k.Value]; dup {
			return nil, fmt.Errorf("line %d: %s: key %q already given on line %d; a second one would silently win or lose", k.Line, ctx, k.Value, first)
		}
		seen[k.Value] = k.Line
		m.pairs = append(m.pairs, kvNode{k, unalias(n.Content[i+1])}) // a value may be a yaml alias
	}
	// yaml merge keys, compose's way to reuse a block: `<<: *base` or
	// `<<: [*a, *b]` add the keys this map does not give itself, an earlier
	// source winning over a later one.
	for _, v := range merges {
		v = unalias(v)
		sources := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			sources = v.Content
		}
		for _, src := range sources {
			// The shape is checked here, so what mapping() refuses inside a
			// merged block is reported as itself: a duplicate key in an
			// anchor must not read as "<< merges a map".
			shape := unalias(src)
			if shape.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("line %d: %s: << merges a map or a list of maps", src.Line, ctx)
			}
			sm, err := mapping(shape, ctx+": <<")
			if err != nil {
				return nil, err
			}
			for _, kv := range sm.pairs {
				if _, given := seen[kv.key.Value]; !given {
					seen[kv.key.Value] = kv.key.Line
					m.pairs = append(m.pairs, kv)
				}
			}
		}
	}
	return m, nil
}

// withoutExtensions drops compose's x- keys from a top-level or service map:
// they hold blocks for anchors to reuse and mean nothing themselves.
func withoutExtensions(m *mapNode) *mapNode {
	out := &mapNode{}
	for _, kv := range m.pairs {
		if !strings.HasPrefix(kv.key.Value, "x-") {
			out.pairs = append(out.pairs, kv)
		}
	}
	return out
}

func unknownKeys(m *mapNode, ctx string, allowed ...string) error {
	for _, kv := range m.pairs {
		if !slices.Contains(allowed, kv.key.Value) {
			return fmt.Errorf("line %d: %s: unknown key %q (known: %s)", kv.key.Line, ctx, kv.key.Value, knownList(allowed))
		}
	}
	return nil
}

// knownList words the keys a map takes for an unknown-key message: sorted,
// comma-separated.
func knownList(keys []string) string { return strings.Join(slices.Sorted(slices.Values(keys)), ", ") }

func scalar(n *yaml.Node, ctx string) (string, error) {
	n = unalias(n)
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: %s: expected a single value", n.Line, ctx)
	}
	return n.Value, nil
}
