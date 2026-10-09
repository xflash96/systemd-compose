package render

import (
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
)

// UnitLine is one Key=value line of a unit file.
type UnitLine struct{ Section, Key, Value string }

// ReadUnit reads a unit file's lines in order, as systemd does: a line
// ending in \ goes on in the next, # and ; start comments, and the spaces
// around a key and its value are not theirs. It reads what unitFile writes
// and what a hand-written unit holds.
func ReadUnit(text string) []UnitLine {
	var out []UnitLine
	section, pending := "", ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && (line[0] == '#' || line[0] == ';') || pending == "" && line == "" {
			continue // a comment, inside a continued line too
		}
		if strings.HasSuffix(line, `\`) {
			pending += strings.TrimSuffix(line, `\`) + " "
			continue
		}
		line, pending = pending+line, ""
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && section != "" {
			out = append(out, UnitLine{section, strings.TrimSpace(k), strings.TrimSpace(v)})
		}
	}
	return out
}

// Directive is every value of key in section of a unit's text, in the
// order written.
func Directive(text, section, key string) []string {
	var values []string
	for _, l := range ReadUnit(text) {
		if l.Section == section && l.Key == key {
			values = append(values, l.Value)
		}
	}
	return values
}

// Marker is what the marker section of a rendered unit names: its
// project, its service (none for the slice and the target), and the yaml
// it came from.
type Marker struct{ Project, Service, Config string }

// The marker section's keys, as marker writes them and ReadMarker reads
// them.
const (
	markerProject = "Project"
	markerService = "Service"
	markerConfig  = "Config"
)

// ReadMarker reads the marker section of a rendered unit's text; a key it
// lacks reads "".
func ReadMarker(text string) Marker {
	first := func(key string) string {
		if values := Directive(text, markerSection, key); len(values) > 0 {
			return values[0]
		}
		return ""
	}
	return Marker{Project: first(markerProject), Service: first(markerService), Config: first(markerConfig)}
}

// markerSection is the section that names, in every rendered unit, the
// project and the yaml it came from. systemd ignores X- sections.
const markerSection = "X-SystemdCompose"

// TriggersKey is the marker key a service with env files carries: the
// files' hash, which changes the unit's text when they change.
const TriggersKey = "RestartTriggers"

func marker(u *unitFile, p *config.Project, service string) {
	u.own(markerSection, markerProject, p.Name, "")
	if service != "" {
		u.own(markerSection, markerService, service, "")
	}
	u.own(markerSection, markerConfig, p.ConfigPath, "")
}
