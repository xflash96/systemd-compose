package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ExecLine renders entrypoint then command as one ExecStart= value: a
// string as systemd will parse it, a list quoted word by word, the first
// word resolved to an absolute path, every $ left by interpolation kept.
// made is what the service's build: creates, where its program may be.
func ExecLine(entry, cmd config.Words, workDir string, search []string, made string) (string, error) {
	var parts []string
	for _, w := range []config.Words{entry, cmd} {
		if w.Empty() {
			continue
		}
		program := len(parts) == 0
		if len(w.List) > 0 {
			words := append([]string(nil), w.List...)
			if program {
				abs, err := resolveProgram(words[0], workDir, search, made)
				if err != nil {
					return "", err
				}
				words[0] = ownPath(words[0], abs)
			}
			for i, x := range words {
				if i == 0 && program {
					// systemd collapses $$ in the arguments, never in the
					// program's own path: doubled, it would name no file
					words[i] = quoteWord(x)
				} else {
					words[i] = quoteWord(ExecLiteral(x))
				}
			}
			parts = append(parts, strings.Join(words, " "))
			continue
		}
		line := w.Line
		if program {
			word, abs, rest, err := ProgramOf(line, workDir, search, made)
			if err != nil {
				return "", err
			}
			parts = append(parts, quoteWord(ownPath(word, abs))+ExecLiteral(rest)) // the path's $ as it is
			continue
		}
		parts = append(parts, ExecLiteral(line))
	}
	return strings.Join(parts, " "), nil
}

// ProgramOf resolves a string command's first word to an absolute path
// found on the search path (or relative to workDir when it contains a
// slash), refusing when nothing is found: the unit must not depend on the
// manager's PATH, which is not the shell's. It returns the word as
// written, the path as found (systemd-run takes it so, a unit file through
// ownPath and quoteWord: its directory may hold a space the word did not)
// and the rest of the line.
func ProgramOf(cmd, workDir string, search []string, made string) (word, abs, rest string, err error) {
	first, quoted, err := config.FirstWord(cmd)
	if err != nil {
		return "", "", "", err
	}
	if strings.ContainsAny(first, " \t\"'\\") {
		return "", "", "", fmt.Errorf("%q: a program path with whitespace or quotes in a string command needs systemd quoting; give command: as a list instead", first)
	}
	if abs, err = resolveProgram(first, workDir, search, made); err != nil {
		return "", "", "", err
	}
	rest = strings.TrimLeft(cmd, " \t")
	if quoted {
		rest = rest[len(first)+2:]
	} else {
		rest = rest[len(first):]
	}
	return first, abs, rest, nil
}

func resolveArgv(argv []string, workDir string, search []string, made string) ([]string, error) {
	abs, err := resolveProgram(argv[0], workDir, search, made)
	if err != nil {
		return nil, err
	}
	out := append([]string{ownPath(argv[0], abs)}, argv[1:]...)
	return out, nil
}

// resolveProgram is ResolveWord for a service's own program, which its
// build: may not have made yet: a path in a creates: that is still missing
// is taken as written (up runs the build before it starts anything, and
// its verify gate lets systemd's "not executable" about it pass).
func resolveProgram(word, workDir string, search []string, made string) (string, error) {
	abs, err := ResolveWord(word, workDir, search)
	if err == nil || made == "" || exists(made) || !strings.Contains(word, "/") {
		return abs, err
	}
	p := word
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}
	if p = filepath.Clean(p); p == made || strings.HasPrefix(p, made+"/") {
		return p, nil
	}
	return abs, err
}

// ownPath is the path a program word resolved to, as a unit file reads it
// back: a % the word did not have comes from a directory (the project's, a
// PATH entry), where systemd would take it for a specifier. A word with a
// % of its own is the user's specifier and resolves to itself.
func ownPath(word, abs string) string {
	if strings.Contains(word, "%") {
		return abs
	}
	return config.UnitPath(abs)
}

// ResolveWord resolves a program word to an absolute path: a path is taken
// relative to workDir, a bare word is looked up in search.
func ResolveWord(word, workDir string, search []string) (string, error) {
	if word == "" {
		return "", fmt.Errorf("empty command")
	}
	if strings.Contains(word, "%") {
		// systemd expands it at load; systemd-analyze verify --user, which
		// up runs on the render, refuses a path that does not exist then.
		return word, nil
	}
	if strings.Contains(word, "/") {
		p := word
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		p = filepath.Clean(p)
		if !executable(p) {
			return "", fmt.Errorf("%q: not an executable file (looked at %s)", word, p)
		}
		return p, nil
	}
	for _, dir := range search {
		p := filepath.Join(dir, word)
		if executable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%q: not found on the search path (~/.local/bin and your PATH); the unit will run under systemd's PATH, so name the program by its full path", word)
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// ---- systemd quoting ------------------------------------------------------

// quoteWord spells one word the way a unit file reads it back. Plain words
// pass through; anything else is double-quoted with C-style escapes.
func quoteWord(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\"';\\") {
		return w
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range w {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ExecLiteral writes the $ that interpolation left (from $$) so systemd
// keeps it: an Exec line expands $VAR from the unit's environment at run
// time, and $$ is its literal dollar. Environment= needs no such care.
func ExecLiteral(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func joinWords(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = quoteWord(w)
	}
	return strings.Join(q, " ")
}
