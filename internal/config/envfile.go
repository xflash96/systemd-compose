package config

import (
	"fmt"
	"os"
	"strings"
)

// ReadEnvFile reads an env file as systemd's EnvironmentFile= does: a port
// of load_env_file's state machine. # and ; start a comment only at the
// start of a line; a value is bare (a backslash escapes the next byte, and
// before a newline joins the lines; trailing blanks trimmed), 'single' or
// "double" quoted, and after a closing quote the value goes on (A="q" x is
// qx); no $VAR expansion; \r is a newline too. It returns the values, the
// lines systemd drops (no =, or a name that is none: export KEY), and the
// keys whose value carries what compose's .env would have taken for a
// comment (a bare " #", text after a closing quote), each as "line N: ...".
func ReadEnvFile(path string) (vars map[string]string, dropped, differs []string, err error) {
	// a FIFO's read waits for a writer forever, so the verb would hang
	if st, err := os.Stat(path); err == nil && !st.Mode().IsRegular() {
		return nil, nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	const (
		preKey = iota
		inKey
		preValue
		inValue
		valueEscape
		single
		double
		doubleEscape
		comment
		commentEscape
	)
	newline := func(c byte) bool { return c == '\n' || c == '\r' }
	blank := func(c byte) bool { return c == ' ' || c == '\t' || newline(c) }
	vars = map[string]string{}
	var k, v []byte
	state, line, start, trail := preKey, 1, 1, 0
	quoted, after, hash, blanked := false, false, false, false
	noEquals := func() {
		dropped = append(dropped, fmt.Sprintf("line %d: %q has no =", start, strings.TrimSpace(string(k))))
	}
	push := func() {
		key := strings.TrimRight(string(k), " \t")
		switch {
		case !IsVarName(key):
			dropped = append(dropped, fmt.Sprintf("line %d: %q", start, key))
		default:
			vars[key] = string(v[:len(v)-trail])
			if hash || after {
				differs = append(differs, fmt.Sprintf("line %d: %s", start, key))
			}
		}
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch state {
		case preKey:
			switch {
			case c == '#' || c == ';':
				state = comment
			case !blank(c):
				state, k, start = inKey, []byte{c}, line
			}
		case inKey:
			switch {
			case newline(c):
				noEquals()
				state = preKey
			case c == '=':
				state, v, trail = preValue, nil, 0
				quoted, after, hash, blanked = false, false, false, false
			default:
				k = append(k, c)
			}
		case preValue:
			switch {
			case newline(c):
				push()
				state = preKey
			case c == '\'':
				state, quoted = single, true
			case c == '"':
				state, quoted = double, true
			case c == '\\':
				state = valueEscape
			case !blank(c):
				after = after || quoted
				hash = hash || c == '#' && len(v) == 0 && blanked // A= # c is "# c" to systemd
				state, v, trail = inValue, append(v, c), 0
			default:
				blanked = true
			}
		case inValue:
			switch {
			case newline(c):
				push()
				state = preKey
			case c == '\\':
				state, trail = valueEscape, 0
			default:
				hash = hash || c == '#' && len(v) > 0 && (v[len(v)-1] == ' ' || v[len(v)-1] == '\t')
				v = append(v, c)
				if c == ' ' || c == '\t' {
					trail++
				} else {
					trail = 0
				}
			}
		case valueEscape:
			state = inValue
			if !newline(c) {
				v, trail = append(v, c), 0
			}
		case single:
			if c == '\'' {
				state = preValue
			} else {
				v = append(v, c)
			}
		case double:
			switch c {
			case '"':
				state = preValue
			case '\\':
				state = doubleEscape
			default:
				v = append(v, c)
			}
		case doubleEscape:
			state = double
			switch {
			case strings.IndexByte("\"\\`$", c) >= 0:
				v = append(v, c)
			case c == '\n':
			default:
				v = append(v, '\\', c)
			}
		case comment:
			if c == '\\' {
				state = commentEscape
			} else if newline(c) {
				state = preKey
			}
		case commentEscape:
			// before systemd 254 a comment ending in a backslash swallows
			// the next line; from 254 it does not: a line whose meaning
			// depends on the host's systemd is refused
			if newline(c) {
				dropped = append(dropped, fmt.Sprintf("line %d: a comment ending in a backslash (systemd before 254 joins the next line to it, 254 and later do not)", line))
				state = preKey
			} else {
				state = comment
			}
		}
		if c == '\n' {
			line++
		}
	}
	switch state {
	case inKey:
		noEquals()
	case single, double, doubleEscape:
		// a quote left open reads the rest of the file into one value
		dropped = append(dropped, fmt.Sprintf("line %d: %s: its quote is never closed, so the rest of the file becomes its value and every later key is lost", start, strings.TrimSpace(string(k))))
	case preValue, inValue, valueEscape:
		push()
	}
	return vars, dropped, differs, nil
}
