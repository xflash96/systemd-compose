package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dependency is one depends_on: edge, from the service that declares it.
type Dependency struct {
	Service   string
	Condition string // its condition: word
	Required  bool   // Requires= instead of Wants=
	Restart   bool   // PartOf=
}

// newDependency is an edge to service with the table's defaults.
func newDependency(service string) Dependency {
	return Dependency{Service: service, Condition: Default("depends_on.condition"), Required: Default("depends_on.required") == "true"}
}

func parseDependsOn(n *yaml.Node, ctx string) ([]Dependency, error) {
	var out []Dependency
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": depends_on")
			if err != nil {
				return nil, err
			}
			out = append(out, newDependency(s))
		}
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": depends_on")
		if err != nil {
			return nil, err
		}
		for _, kv := range m.pairs {
			d := newDependency(kv.key.Value)
			if kv.value.Kind == yaml.MappingNode {
				dm, err := mapping(kv.value, ctx+": depends_on: "+d.Service)
				if err != nil {
					return nil, err
				}
				if err := unknownKeys(dm, ctx+": depends_on: "+d.Service, dependency.names()...); err != nil {
					return nil, err
				}
				if cn := dm.get("condition"); cn != nil {
					if d.Condition, err = scalar(cn, ctx+": depends_on: condition"); err != nil {
						return nil, err
					}
				}
				explicitRequired := false
				if rn := dm.get("required"); rn != nil {
					if err := rn.Decode(&d.Required); err != nil {
						return nil, fmt.Errorf("line %d: %s: depends_on: required: %v", rn.Line, ctx, err)
					}
					explicitRequired = true
				}
				if d.Condition != "service_started" && d.Condition != "" {
					if explicitRequired && !d.Required {
						return nil, fmt.Errorf("line %d: %s: depends_on: %s: condition %s with required: false would not enforce the condition (a Wants= dependency starts even when %s fails); drop required:", kv.key.Line, ctx, d.Service, d.Condition, d.Service)
					}
					d.Required = true
				}
				if rn := dm.get("restart"); rn != nil {
					if err := rn.Decode(&d.Restart); err != nil {
						return nil, fmt.Errorf("line %d: %s: depends_on: restart: %v", rn.Line, ctx, err)
					}
				}
			} else if !(kv.value.Kind == yaml.ScalarNode && kv.value.Tag == "!!null") {
				return nil, fmt.Errorf("line %d: %s: depends_on: %s: is a map of %s or empty", kv.value.Line, ctx, d.Service, dependency.braces())
			}
			if !slices.Contains(condition.Words, d.Condition) {
				return nil, fmt.Errorf("line %d: %s: depends_on: %s: condition %q; use %s", kv.key.Line, ctx, d.Service, d.Condition, orList(condition.Words))
			}
			out = append(out, d)
		}
	default:
		return nil, fmt.Errorf("line %d: %s: depends_on: is a list of services or a map", n.Line, ctx)
	}
	return out, nil
}

// validateGraph checks depends_on against the declared services.
func validateGraph(p *Project) error {
	byName := map[string]*Service{}
	for _, s := range p.Services {
		byName[s.Name] = s
	}
	for _, s := range p.Services {
		for _, d := range s.DependsOn {
			t, ok := byName[d.Service]
			if !ok {
				return fmt.Errorf("service %s: depends_on: %q is not a service in this file (a dependency on an outside unit goes through unit: Unit: After:)", s.Name, d.Service)
			}
			if t == s {
				return fmt.Errorf("service %s: depends_on: itself", s.Name)
			}
			if t.Schedule != nil {
				return fmt.Errorf("service %s: depends_on: %s runs on its timer, not on a dependent's start; a scheduled job cannot be a dependency", s.Name, d.Service)
			}
			switch d.Condition {
			case "service_healthy":
				// a Type= that signals its own readiness (notify,
				// notify-reload, dbus) is healthy once started
				readiness := t.Unit.Has("Service", "Type", "notify") || t.Unit.Has("Service", "Type", "notify-reload") || t.Unit.Has("Service", "Type", "dbus")
				if t.Healthcheck == nil && !readiness {
					return fmt.Errorf("service %s: depends_on: %s: condition service_healthy, but %s has no healthcheck: (nor a Type= that signals readiness: notify, notify-reload, dbus); it would be service_started in disguise", s.Name, d.Service, d.Service)
				}
			case "service_completed_successfully":
				if !t.Oneshot {
					return fmt.Errorf("service %s: depends_on: %s: condition service_completed_successfully needs %s to declare oneshot: true", s.Name, d.Service, d.Service)
				}
			}
		}
	}
	// Cycles: a plain DFS over depends_on.
	state := map[string]int{}
	var visit func(s *Service, path []string) error
	visit = func(s *Service, path []string) error {
		switch state[s.Name] {
		case 1:
			return fmt.Errorf("depends_on cycle: %s -> %s", strings.Join(path, " -> "), s.Name)
		case 2:
			return nil
		}
		state[s.Name] = 1
		for _, d := range s.DependsOn {
			if err := visit(byName[d.Service], append(path, s.Name)); err != nil {
				return err
			}
		}
		state[s.Name] = 2
		return nil
	}
	for _, s := range p.Services {
		if err := visit(s, nil); err != nil {
			return err
		}
	}
	return nil
}
