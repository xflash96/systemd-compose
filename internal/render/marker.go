package render

import (
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
)

// Directive reads every value of key in section of a rendered unit's text,
// in the order written: the reader of what unitFile writes.
func Directive(text, section, key string) []string {
	var values []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "["+section+"]"
			continue
		}
		if v, ok := strings.CutPrefix(line, key+"="); in && ok {
			values = append(values, v)
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
		if values := Directive(text, MarkerSection, key); len(values) > 0 {
			return values[0]
		}
		return ""
	}
	return Marker{Project: first(markerProject), Service: first(markerService), Config: first(markerConfig)}
}

// MarkerSection is the section that names, in every rendered unit, the
// project and the yaml it came from. systemd ignores X- sections.
const MarkerSection = "X-SystemdCompose"

// TriggersKey is the marker key a service with env files carries: the
// files' hash, which changes the unit's text when they change.
const TriggersKey = "RestartTriggers"

func marker(u *unitFile, p *config.Project, service string) {
	u.own(MarkerSection, markerProject, p.Name, "")
	if service != "" {
		u.own(MarkerSection, markerService, service, "")
	}
	u.own(MarkerSection, markerConfig, p.ConfigPath, "")
}
