package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// PassThrough is the raw `unit:` block: section -> key -> values, in file
// order. The renderer merges it under three rules: a directive a
// translated key owns is refused, a repeatable directive concatenates,
// a default gives way.
type PassThrough struct {
	Sections []PassSection
}

// PassSection is one section of unit:, such as Service.
type PassSection struct {
	Name string
	Keys []PassKey
}

// PassKey is one directive of unit:, with every value it is given and the
// yaml line, for messages.
type PassKey struct {
	Name   string
	Values []string
	Line   int
}

// parsePassThrough reads `unit:`: section -> key -> value or list of values.
// Sections are limited to what a service can carry.
func parsePassThrough(n *yaml.Node, ctx string, scheduled, listens bool) (PassThrough, error) {
	var pt PassThrough
	m, err := mapping(n, ctx+": unit")
	if err != nil {
		return pt, err
	}
	for _, sec := range m.pairs {
		sname := sec.key.Value
		section, known := unitMap.key(sname)
		switch {
		case sname == "Timer":
			if !scheduled {
				return pt, fmt.Errorf("line %d: %s: unit: [Timer] only applies to a service with schedule:", sec.key.Line, ctx)
			}
		case sname == "Socket":
			if !listens {
				return pt, fmt.Errorf("line %d: %s: unit: [Socket] only applies to a service with listen:", sec.key.Line, ctx)
			}
		case sname == "Install":
			return pt, fmt.Errorf("line %d: %s: unit: [Install] is refused: boot enablement is declared once, on the project target; services are pulled in by it", sec.key.Line, ctx)
		case !known:
			return pt, fmt.Errorf("line %d: %s: unit: section %q; a service carries Unit, Service, Timer (with schedule:) and Socket (with listen:)", sec.key.Line, ctx, sname)
		}
		km, err := mapping(sec.value, ctx+": unit: "+sname)
		if err != nil {
			return pt, err
		}
		ps := PassSection{Name: sname}
		for _, kv := range km.pairs {
			key := kv.key.Value
			if why, refused := section.Forms[0].Refused[key]; refused {
				return pt, fmt.Errorf("line %d: %s: unit: %s: %s: %s", kv.key.Line, ctx, sname, key, why)
			}
			if !reDirective.MatchString(key) {
				return pt, fmt.Errorf("line %d: %s: unit: %s: %q is not a directive name", kv.key.Line, ctx, sname, key)
			}
			pk := PassKey{Name: key, Line: kv.key.Line}
			value := unalias(kv.value) // a yaml alias stands for a value or a list
			switch value.Kind {
			case yaml.ScalarNode:
				pk.Values = []string{value.Value}
			case yaml.SequenceNode:
				for _, item := range value.Content {
					s, err := scalar(unalias(item), ctx+": unit: "+sname+": "+key)
					if err != nil {
						return pt, err
					}
					pk.Values = append(pk.Values, s)
				}
			default:
				return pt, fmt.Errorf("line %d: %s: unit: %s: %s: a value or a list of values", kv.value.Line, ctx, sname, key)
			}
			// an empty Directive= is systemd's reset of every earlier line
			// of it: the tool's own Environment= and ExecStartPost= with them
			if len(pk.Values) == 0 || slices.ContainsFunc(pk.Values, func(v string) bool { return strings.Trim(v, " \t") == "" }) {
				return pt, fmt.Errorf("line %d: %s: unit: %s: %s: an empty value resets the directive in systemd, every line of it written before (the tool's too); give it a value", kv.key.Line, ctx, sname, key)
			}
			ps.Keys = append(ps.Keys, pk)
		}
		pt.Sections = append(pt.Sections, ps)
	}
	return pt, nil
}

// unalias is the node a yaml alias stands for, or the node itself.
func unalias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// Values are every value unit: gives key in section, in file order.
func (pt PassThrough) Values(section, key string) []string {
	var out []string
	for _, s := range pt.Sections {
		if s.Name != section {
			continue
		}
		for _, k := range s.Keys {
			if k.Name == key {
				out = append(out, k.Values...)
			}
		}
	}
	return out
}

// Has reports whether unit: sets key in section to value.
func (pt PassThrough) Has(section, key, value string) bool {
	return slices.Contains(pt.Values(section, key), value)
}
