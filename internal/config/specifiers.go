package config

import (
	"fmt"
	"strings"
)

// literalPath is the one rule for a path the tool reads itself (working_dir,
// env_file, build: creates): a % there would never mean what a specifier
// means; for the first two, systemd would also expand it.
func literalPath(s, key, ctx string, line int) error {
	if strings.Contains(s, "%") {
		return fmt.Errorf("line %d: %s: %s: no %% here: the tool reads this path itself (to check it, hash it or build in it), so it must be literal", line, ctx, key)
	}
	if strings.HasPrefix(s, "~") {
		// no shell reads the yaml: ~/app would name a directory called ~
		return fmt.Errorf("line %d: %s: %s: %s: a ~ is not expanded here; write the full path, or one relative to the yaml", line, ctx, key, s)
	}
	return nil
}

// specifiers is the one rule for % in a value systemd expands: a specifier
// of the 248 floor passes through for systemd to expand, %% is a literal
// percent, and so is a % before anything but a letter or digit: systemd
// keeps %{, "100% done" and a % at the end as written. A % before a letter
// or digit that is no specifier is refused here, because systemd refuses
// the line, and for Environment= or ListenStream= says so only in its log,
// with verify silent.
func specifiers(s string) error {
	var newer, deprecated, unknown, all []string
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		i++
		c := s[i]
		if c == '%' || !isAlnum(c) {
			continue
		}
		p := "%" + string(c)
		all = append(all, "%"+p)
		switch {
		case specifierMeaning[c] != "":
		case strings.IndexByte("dqyY", c) >= 0:
			newer = append(newer, p)
		case strings.IndexByte("crR", c) >= 0:
			deprecated = append(deprecated, p)
		default:
			unknown = append(unknown, p)
		}
	}
	// Every refused letter of the value at once, and the escape for each
	// of its letters: in a date format they all want it.
	var why []string
	say := func(ps []string, one, many string) {
		switch len(ps) {
		case 0:
		case 1:
			why = append(why, ps[0]+" is "+one)
		default:
			why = append(why, strings.Join(ps, ", ")+" are "+many)
		}
	}
	say(newer, "a specifier newer than systemd 248, the oldest this tool supports", "specifiers newer than systemd 248, the oldest this tool supports")
	say(deprecated, "a deprecated systemd specifier", "deprecated systemd specifiers")
	say(unknown, "not a systemd specifier, and systemd refuses the line", "not systemd specifiers, and systemd refuses the line")
	if len(why) == 0 {
		return nil
	}
	return fmt.Errorf("in %q, %s; for a literal percent write %%%%, and in a date or printf format each letter wants it: %s", s, strings.Join(why, "; "), strings.Join(all, " "))
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// specifierMeaning is what systemd fills in for each specifier the 248
// floor has (systemd.unit(5), "Specifiers"): the letters specifiers passes,
// and the note up prints.
var specifierMeaning = map[byte]string{
	'a': "the architecture", 'A': "the OS image version", 'b': "the boot ID", 'B': "the OS build ID",
	'C': "the cache directory", 'E': "the configuration directory", 'f': "the unit's instance or prefix as a path",
	'g': "your group", 'G': "your group ID", 'h': "your home directory", 'H': "the host name",
	'i': "the unit's instance", 'I': "the unit's instance, unescaped", 'j': "the last part of the unit's prefix",
	'J': "the last part of the unit's prefix, unescaped", 'l': "the short host name", 'L': "the log directory",
	'm': "the machine ID", 'M': "the OS image ID", 'n': "the unit's name", 'N': "the unit's name without its suffix",
	'o': "the OS ID", 'p': "the unit's prefix", 'P': "the unit's prefix, unescaped", 's': "your login shell",
	'S': "the state directory", 't': "the runtime directory", 'T': "the temporary directory", 'u': "your user name",
	'U': "your user ID", 'v': "the kernel release", 'V': "the directory for large temporary files",
	'w': "the OS version ID", 'W': "the OS variant ID",
}

// specifierNotes names the specifiers in what a service runs and in its
// environment, once per key: their letters are date's and printf's too,
// and nothing else shows that a +%s became the path of a shell. One that
// leads a path (%h/bin, %t/app.sock) is what specifiers are for, and passes
// without a word.
func specifierNotes(svc *Service) []string {
	var notes []string
	note := func(key string, values []string) {
		var said []string
		seen := map[byte]bool{}
		for _, v := range values {
			for i := 0; i+1 < len(v); i++ {
				if v[i] != '%' {
					continue
				}
				i++
				c := v[i]
				if c == '%' || specifierMeaning[c] == "" || seen[c] || i+1 < len(v) && v[i+1] == '/' {
					continue
				}
				seen[c] = true
				said = append(said, fmt.Sprintf("%%%c (%s)", c, specifierMeaning[c]))
			}
		}
		if len(said) > 0 {
			notes = append(notes, fmt.Sprintf("service %s: %s: systemd fills in %s when it runs the service; for a literal percent sign write %%%%, as in %%%s", svc.Name, key, strings.Join(said, ", "), said[0][:2]))
		}
	}
	words := func(w Words) []string { return append([]string{w.Line}, w.List...) }
	note("entrypoint", words(svc.Entrypoint))
	note("command", words(svc.Command))
	if svc.Healthcheck != nil {
		note("healthcheck", svc.Healthcheck.Test)
	}
	var env []string
	for _, kv := range svc.Environment {
		env = append(env, kv.Value)
	}
	note("environment", env)
	return notes
}

// UsesSpecifier reports a % that systemd would expand: one before a
// letter or digit, not the literal %%.
func UsesSpecifier(s string) bool {
	s = strings.ReplaceAll(s, "%%", "")
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '%' && isAlnum(s[i+1]) {
			return true
		}
	}
	return false
}

// UnitPath writes a path the tool found (the project directory, a file
// in it) so systemd reads it back as written: % is a specifier in a unit
// file, and %% its literal percent.
func UnitPath(p string) string { return strings.ReplaceAll(p, "%", "%%") }
