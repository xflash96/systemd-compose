package project

import (
	"strings"
)

// Flags are the global flags that name a project, which a hint repeats.
type Flags struct {
	File     string   // -f: the yaml, instead of discovery
	Name     string   // -p
	Profiles []string // --profile, repeatable
}

// selfCmd is the command that reaches this project again: with the -f, -p
// and --profile it was given, since a printed hint without them reads
// another project's journal, or none (the environment and the .env stay).
func selfCmd(f Flags, cfg string) string {
	c := "systemd-compose"
	if f.File != "" {
		c += " -f " + shellWord(cfg)
	}
	if f.Name != "" {
		c += " -p " + f.Name
	}
	for _, pf := range f.Profiles {
		c += " --profile " + shellWord(pf)
	}
	return c
}

// shellWord quotes a word for a shell when it needs it.
func shellWord(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t'\"$\\`!*?[]{}()<>|&;#~") {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}
