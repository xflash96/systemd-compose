package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Words is a command in compose's two spellings: a string that systemd
// parses (Line), or a list with one word per element (List), which the
// renderer quotes so that spaces, quotes and $ are plain characters.
type Words struct {
	Line string
	List []string
}

// Empty reports a command that was not given.
func (w Words) Empty() bool { return w.Line == "" && len(w.List) == 0 }

// program is the first word, the one that names what runs.
func (w Words) program() string {
	if len(w.List) > 0 {
		return w.List[0]
	}
	first, _, _ := FirstWord(w.Line)
	return first
}

// refuseProgram checks the word that names what runs: systemd reads a
// leading - @ : + ! as a prefix, not as part of the path.
func refuseProgram(first, ctx string, line int) error {
	if first == "" {
		return fmt.Errorf("line %d: %s: no command word", line, ctx)
	}
	if strings.ContainsAny(first[:1], "-@:+!") {
		return fmt.Errorf("line %d: %s: first word %q starts with a character systemd treats as a prefix (- @ : + !); rename the program or use unit: Service: ExecStart:", line, ctx, first)
	}
	return nil
}

// refuseLine checks a string command wherever it stands. systemd runs it
// without a shell, so shell syntax outside quotes would reach the program as
// a plain argument (a pipe, a redirect, an && that never happens) or, for a
// bare ;, start a second command; and a leading ~ is never expanded.
func refuseLine(cmd, ctx string, line int) error {
	if err := shellSyntax(cmd, ctx, line, "[sh, -c, '...'], or use unit: Service: ExecStart:", "write %h for the home directory"); err != nil {
		return err
	}
	if err := specifiers(cmd); err != nil {
		return fmt.Errorf("line %d: %s: %v", line, ctx, err)
	}
	return nil
}

// reRedirect is a shell redirection at the start of a word: >, >>, <, 2>,
// &>, 2>&1.
var reRedirect = regexp.MustCompile(`^([0-9]*[<>]|&>)`)

// unquotedWords splits a command line as systemd does, keeping of each word
// only what stands outside quotes: what a shell would read as syntax.
func unquotedWords(cmd string) []string {
	var out []string
	var cur strings.Builder
	in, has := byte(0), false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case in != 0:
			if c == in {
				in = 0
			} else if c == '\\' && in == '"' {
				i++
			}
		case c == '"' || c == '\'':
			in, has = c, true
		case c == ' ' || c == '\t':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// parseWords reads command: or entrypoint:, a string or a list of words.
func parseWords(n *yaml.Node, ctx string) (Words, error) {
	if n.Kind == yaml.SequenceNode {
		if len(n.Content) == 0 {
			return Words{}, fmt.Errorf("line %d: %s: is empty", n.Line, ctx)
		}
		var w Words
		for _, item := range n.Content {
			s, err := scalar(item, ctx)
			if err != nil {
				return Words{}, err
			}
			if err := specifiers(s); err != nil {
				return Words{}, fmt.Errorf("line %d: %s: %v", item.Line, ctx, err)
			}
			w.List = append(w.List, s)
		}
		return w, nil
	}
	s, err := scalar(n, ctx)
	if err != nil {
		return Words{}, err
	}
	if strings.TrimSpace(s) == "" {
		return Words{}, fmt.Errorf("line %d: %s: is empty", n.Line, ctx)
	}
	if _, _, err := FirstWord(s); err != nil {
		return Words{}, fmt.Errorf("line %d: %s: %v", n.Line, ctx, err)
	}
	if err := refuseLine(s, ctx, n.Line); err != nil {
		return Words{}, err
	}
	return Words{Line: s}, nil
}

// shellSyntax refuses what a shell would read in a command line that no
// shell runs: shellLine says how to write one, home what to write for ~.
func shellSyntax(cmd, ctx string, line int, shellLine, home string) error {
	for _, w := range unquotedWords(cmd) {
		// A shell reads an operator standing alone, or at a word's edge
		// (>log, 2>/dev/null, |tee, cmd&, a;); in the middle of a word
		// (--max=1>2, a|b as a regex) it is the program's argument.
		bare := w == ";" || w == "&&" || w == "||" || w == "&" || w == "|"
		if bare || reRedirect.MatchString(w) || strings.HasPrefix(w, "|") || strings.HasSuffix(w, ";") || strings.HasSuffix(w, "&") || strings.HasSuffix(w, "|") {
			return fmt.Errorf("line %d: %s: %q is shell syntax, and systemd runs no shell: write a shell line as %s", line, ctx, w, shellLine)
		}
		if strings.HasPrefix(w, "~") {
			return fmt.Errorf("line %d: %s: %q: systemd does not expand ~; %s", line, ctx, w, home)
		}
	}
	return nil
}

// FirstWord returns the first word of a command under systemd's quoting:
// a leading "..." or '...' is one word (quoted=true), otherwise up to
// whitespace. An unclosed quote is an error, never a guess.
func FirstWord(cmd string) (word string, quoted bool, err error) {
	cmd = strings.TrimLeft(cmd, " \t")
	if cmd == "" {
		return "", false, nil
	}
	if q := cmd[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(cmd[1:], q)
		if end < 0 {
			return "", true, fmt.Errorf("unclosed %c quote in %q", q, cmd)
		}
		return cmd[1 : end+1], true, nil
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i], false, nil
	}
	return cmd, false, nil
}
