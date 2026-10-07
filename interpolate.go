// interpolate.go — compose's ${VAR} interpolation, at render time, from the
// .env file beside the yaml and nothing else.
//
// The shell environment is not a source: what a unit runs must not depend
// on which shell typed `up`. An unset variable
// without a default is an error, never compose's blank-plus-warning, since
// a blank baked into a unit is a silent failure. Values only, never keys,
// and never inside `unit:`, where `$` is systemd's own syntax.
package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// readDotenv reads a .env file: KEY=value lines, # comments and blank lines
// skipped, an optional "export " prefix. A value is unquoted (trimmed; " #"
// starts a comment), 'single-quoted' (literal) or "double-quoted" (\n \t
// \\ \" escapes); a quoted value ends at its closing quote, after which
// only a # comment may follow, so the quotes never reach the value.
// A quoted value whose closing quote is on a later line spans those lines,
// as compose's .env allows. Unquoted and double-quoted values are
// interpolated against the keys above them. A missing file is an empty set.
func readDotenv(path string) (map[string]string, error) {
	vars := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return vars, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		start, line := i, strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !isVarName(k) {
			return nil, fmt.Errorf("%s:%d: expected KEY=value", path, i+1)
		}
		v = strings.TrimSpace(v)
		expand := true
		if v != "" && (v[0] == '\'' || v[0] == '"') {
			quote := v[0]
			end := closingQuote(v)
			for end < 0 && i+1 < len(lines) {
				i++
				v += "\n" + lines[i]
				end = closingQuote(v)
			}
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unclosed %c quote", path, start+1, quote)
			}
			// What follows the closing quote is a comment or nothing; the
			// quoted span alone is the value, so the quotes never leak into
			// it the way a "value" # comment line would otherwise leave them.
			if tail := strings.TrimSpace(v[end+1:]); tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("%s:%d: %q after the closing %c quote; quote the whole value or drop the quotes", path, start+1, tail, quote)
			}
			v, expand = v[1:end], quote == '"'
			if quote == '"' {
				v = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\\`, `\`).Replace(v)
			}
		} else if c := strings.Index(v, " #"); c >= 0 {
			v = strings.TrimSpace(v[:c])
		}
		if expand {
			if v, err = interpolate(v, vars); err != nil {
				return nil, fmt.Errorf("%s:%d: %v", path, start+1, err)
			}
		}
		vars[k] = v
	}
	return vars, nil
}

// closingQuote is the index of the quote that closes the one at s[0], or
// -1. Inside "..." a backslash escapes the next byte, so \" is not it;
// '...' has no escapes.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch {
		case s[0] == '"' && s[i] == '\\':
			i++
		case s[i] == s[0]:
			return i
		}
	}
	return -1
}

// varNameLen is how much of s is a variable name: [A-Za-z_][A-Za-z0-9_]*,
// the one rule for a name here and for an environment key (parseEnvironment).
// It is where a $VAR or a ${VAR... form ends.
func varNameLen(s string) int {
	n := 0
	for ; n < len(s); n++ {
		c := s[n]
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || n > 0 && c >= '0' && c <= '9') {
			break
		}
	}
	return n
}

func isVarName(s string) bool { return s != "" && varNameLen(s) == len(s) }

// interpolate expands compose's forms in s: $$ is a literal $; $VAR and
// ${VAR} are the value; ${VAR:-d} and ${VAR-d} default when unset or empty
// (or only unset); ${VAR:?e} and ${VAR?e} fail; ${VAR:+r} and ${VAR+r}
// replace when set. Defaults and replacements may nest.
func interpolate(s string, vars map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		rest := s[i+1:]
		switch {
		case strings.HasPrefix(rest, "$"):
			b.WriteByte('$')
			i++
		case strings.HasPrefix(rest, "{"):
			end := closingBrace(rest)
			if end < 0 {
				return "", fmt.Errorf("unclosed ${ in %q", s)
			}
			v, err := braced(rest[1:end], vars)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i += end + 1
		default:
			n := varNameLen(rest)
			if n == 0 {
				return "", fmt.Errorf("a $ that starts no variable in %q; write $$ for a literal dollar", s)
			}
			v, ok := vars[rest[:n]]
			if !ok {
				return "", unset(rest[:n])
			}
			b.WriteString(v)
			i += n
		}
	}
	return b.String(), nil
}

// closingBrace finds the } that closes the { at s[0], counting nested ${.
func closingBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

func braced(expr string, vars map[string]string) (string, error) {
	n := varNameLen(expr)
	name, op := expr[:n], expr[n:]
	if name == "" {
		return "", fmt.Errorf("${%s}: no variable name", expr)
	}
	v, set := vars[name]
	for _, o := range []string{":-", ":?", ":+", "-", "?", "+"} {
		if !strings.HasPrefix(op, o) {
			continue
		}
		arg := op[len(o):]
		present := set && (v != "" || !strings.HasPrefix(o, ":"))
		switch o[len(o)-1] {
		case '-':
			if present {
				return v, nil
			}
			return interpolate(arg, vars)
		case '?':
			if present {
				return v, nil
			}
			msg, err := interpolate(arg, vars)
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("%s: %s", name, msg)
		default: // '+'
			if present {
				return interpolate(arg, vars)
			}
			return "", nil
		}
	}
	if op != "" {
		return "", fmt.Errorf("${%s}: unknown form (known: ${VAR}, ${VAR:-default}, ${VAR-default}, ${VAR:?error}, ${VAR?error}, ${VAR:+other}, ${VAR+other})", expr)
	}
	if !set {
		return "", unset(name)
	}
	return v, nil
}

func unset(name string) error {
	return fmt.Errorf("%s is not set: variables come from the .env beside the yaml, never from the shell; set it there, or write ${%s:-default}", name, name)
}

// interpolateTree expands every scalar value of the yaml in place, keys and
// `services.<name>.unit` excepted. A plain scalar loses its tag so yaml
// infers it again: `oneshot: ${ONE}` decodes as the bool it expands to.
func interpolateTree(doc *yaml.Node, vars map[string]string) error {
	seen := map[*yaml.Node]bool{}
	var walk func(n *yaml.Node, path []string) error
	walk = func(n *yaml.Node, path []string) error {
		if n == nil || seen[n] {
			return nil
		}
		seen[n] = true
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				if err := walk(c, path); err != nil {
					return err
				}
			}
		case yaml.AliasNode:
			return walk(n.Alias, path)
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				p := path
				// A merge key is transparent, as yaml makes it: what `<<`
				// brings in belongs to the enclosing map, so a `unit:`
				// reached through one is still the service's raw systemd.
				if key := n.Content[i].Value; key != "<<" {
					p = append(append([]string(nil), path...), key)
				}
				// An x- block (top level or in a service) means nothing where
				// it is written; a service brings it in through a merge key
				// or an alias, and the walk reaches it there, under that
				// service's path. So it is expanded where it is used, and a
				// unit: inside it, or aliased from it, is raw systemd like a
				// service's own. (len(p) is 0 for a top-level `<<`.)
				if extensionKey(p) || len(p) == 3 && p[0] == "services" && p[2] == "unit" {
					continue
				}
				if err := walk(n.Content[i+1], p); err != nil {
					return err
				}
			}
		case yaml.ScalarNode:
			if !strings.Contains(n.Value, "$") {
				return nil
			}
			v, err := interpolate(n.Value, vars)
			if err != nil {
				return fmt.Errorf("line %d: %s: %v", n.Line, strings.Join(path, ": "), err)
			}
			n.Value = v
			if n.Style == 0 {
				n.Tag = ""
			}
		}
		return nil
	}
	return walk(doc, nil)
}

// extensionKey reports a compose x- key where one is allowed: at the top
// level or directly in a service. Elsewhere x- is an ordinary name (a
// service may be called x-api).
func extensionKey(p []string) bool {
	switch {
	case len(p) == 1:
		return strings.HasPrefix(p[0], "x-")
	case len(p) == 3 && p[0] == "services":
		return strings.HasPrefix(p[2], "x-")
	}
	return false
}
