package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// KV is one environment variable, in the order the yaml gives it.
type KV struct{ Key, Value string }

// EnvFile is one env_file: entry. A required file must exist at up.
type EnvFile struct {
	Path     string // absolute
	Required bool
}

// CheckEnvFiles refuses a required env_file systemd would fail the unit on:
// missing, a directory, or not a regular file. An optional one is left to
// systemd.
func CheckEnvFiles(files []EnvFile) error {
	for _, ef := range files {
		if !ef.Required {
			continue
		}
		st, err := os.Stat(ef.Path)
		switch {
		case err != nil:
			return fmt.Errorf("env_file %s: %v (mark it {path, required: false} if it may be absent)", ef.Path, err)
		case st.IsDir():
			return fmt.Errorf("env_file %s is a directory, not a file", ef.Path)
		case !st.Mode().IsRegular():
			return fmt.Errorf("env_file %s is not a regular file", ef.Path)
		}
	}
	return nil
}

// ownVariables are the names a service's environment: and env_file: set,
// the files read leniently: only a note depends on them.
func ownVariables(svc *Service) map[string]bool {
	own := map[string]bool{}
	for _, kv := range svc.Environment {
		own[kv.Key] = true
	}
	for _, ef := range svc.EnvFiles {
		vars, _, _, _ := ReadEnvFile(ef.Path) // a file it cannot read sets nothing it knows of
		for k := range vars {
			own[k] = true
		}
	}
	return own
}

func parseEnvironment(n *yaml.Node, ctx string) ([]KV, error) {
	var out []KV
	given := map[string]int{} // key -> line
	add := func(k, v string, line int) error {
		if !IsVarName(k) { // the same rule interpolation uses for ${KEY}
			return fmt.Errorf("line %d: %s: environment: %q is not a valid variable name", line, ctx, k)
		}
		if first, ok := given[k]; ok {
			return fmt.Errorf("line %d: %s: environment: %s already given on line %d; a second one would silently win", line, ctx, k, first)
		}
		given[k] = line
		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("line %d: %s: environment: %s contains a newline", line, ctx, k)
		}
		if err := specifiers(v); err != nil {
			return fmt.Errorf("line %d: %s: environment: %s: %v", line, ctx, k, err)
		}
		out = append(out, KV{k, v})
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": environment")
		if err != nil {
			return nil, err
		}
		for _, kv := range m.pairs {
			// compose's KEY: with no value takes the shell's variable
			if kv.value.Kind == yaml.ScalarNode && kv.value.Tag == "!!null" {
				return nil, fmt.Errorf("line %d: %s: environment: %q has no value; a bare name would capture the shell's variable, which is refused: write %s: value, or %s: \"\" for an empty one", kv.key.Line, ctx, kv.key.Value, kv.key.Value, kv.key.Value)
			}
			v, err := scalar(kv.value, ctx+": environment: "+kv.key.Value)
			if err != nil {
				return nil, err
			}
			if err := add(kv.key.Value, v, kv.key.Line); err != nil {
				return nil, err
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": environment")
			if err != nil {
				return nil, err
			}
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				return nil, fmt.Errorf("line %d: %s: environment: %q has no value; a bare name would capture the shell's variable, which is refused: write %s=value", item.Line, ctx, s, s)
			}
			if err := add(k, v, item.Line); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: environment: must be a map or a list of KEY=value", n.Line, ctx)
	}
	return out, nil
}

func parseEnvFiles(n *yaml.Node, ctx, dir string) ([]EnvFile, error) {
	one := func(item *yaml.Node) (EnvFile, error) {
		item = unalias(item)
		ef := EnvFile{Required: Default("env_file.required") == "true"}
		switch item.Kind {
		case yaml.ScalarNode:
			ef.Path = item.Value
		case yaml.MappingNode:
			m, err := mapping(item, ctx+": env_file")
			if err != nil {
				return ef, err
			}
			if err := unknownKeys(m, ctx+": env_file", envFileMap.names()...); err != nil {
				return ef, err
			}
			pn := m.get("path")
			if pn == nil {
				return ef, fmt.Errorf("line %d: %s: env_file: path: is required", item.Line, ctx)
			}
			if ef.Path, err = scalar(pn, ctx+": env_file: path"); err != nil {
				return ef, err
			}
			if rn := m.get("required"); rn != nil {
				if err := rn.Decode(&ef.Required); err != nil {
					return ef, fmt.Errorf("line %d: %s: env_file: required: %v", rn.Line, ctx, err)
				}
			}
		default:
			return ef, fmt.Errorf("line %d: %s: env_file: entries are a path or %s", item.Line, ctx, envFileMap.braces())
		}
		if ef.Path == "" {
			return ef, fmt.Errorf("line %d: %s: env_file: empty path", item.Line, ctx)
		}
		if err := literalPath(ef.Path, "env_file", ctx, item.Line); err != nil {
			return ef, err
		}
		if strings.ContainsAny(ef.Path, "*?[") {
			// systemd reads every file a pattern matches, which the tool
			// would neither check nor hash for changes
			return ef, fmt.Errorf("line %d: %s: env_file: no * ? or [ here: the tool reads this file itself (to check its lines and see it change), so name one file", item.Line, ctx)
		}
		if !filepath.IsAbs(ef.Path) {
			ef.Path = filepath.Join(dir, ef.Path)
		}
		ef.Path = filepath.Clean(ef.Path)
		return ef, nil
	}
	var out []EnvFile
	if n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			ef, err := one(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ef)
		}
		return out, nil
	}
	ef, err := one(n)
	if err != nil {
		return nil, err
	}
	return []EnvFile{ef}, nil
}
