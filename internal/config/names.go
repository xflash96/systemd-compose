package config

import (
	"path/filepath"
	"strings"
)

// EscapeName is a project name as its units spell it: each - as \x2d, as
// systemd-escape writes it. In a unit name a - nests slices (my-app.slice
// sits inside my.slice, so stopping my would stop my-app) and would make
// <project>-<service> ambiguous; escaped, the first - ends the project.
func EscapeName(name string) string { return strings.ReplaceAll(name, "-", `\x2d`) }

// unescapeName is the project name an escaped unit prefix spells.
func unescapeName(prefix string) string { return strings.ReplaceAll(prefix, `\x2d`, "-") }

// SplitUnitName is UnitName read back: the project (up to the first -,
// since the project's own are escaped) and the service ("" for the slice
// and the target), for a unit whose file cannot be read for its marker.
func SplitUnitName(unit string) (project, service string) {
	prefix, service, _ := strings.Cut(strings.TrimSuffix(unit, filepath.Ext(unit)), "-")
	return unescapeName(prefix), service
}

// TargetName is the project's target: up starts it, and boot starts it.
func (p *Project) TargetName() string { return EscapeName(p.Name) + ".target" }

// SliceName is the project's slice: the cgroup every service runs in.
func (p *Project) SliceName() string { return EscapeName(p.Name) + ".slice" }

// UnitName is the one spelling of <project>-<service><suffix>.
func (p *Project) UnitName(service, suffix string) string {
	return EscapeName(p.Name) + "-" + service + suffix
}

// LogIdentifier is the tag every line a service prints carries in the
// journal (SyslogIdentifier=): PROJECT-SERVICE, unless the service's unit:
// sets its own. logs matches it, as a line from a child that has already
// exited carries no unit name, only this.
func (p *Project) LogIdentifier(s *Service) string {
	if ids := s.Unit.Values("Service", "SyslogIdentifier"); len(ids) > 0 {
		return ids[len(ids)-1]
	}
	return p.UnitName(s.Name, "")
}

// ServiceUnit is the service unit that runs s.
func (p *Project) ServiceUnit(s *Service) string { return p.UnitName(s.Name, ".service") }

// TimerUnit is the timer of a scheduled service.
func (p *Project) TimerUnit(s *Service) string { return p.UnitName(s.Name, ".timer") }

// SocketUnit is the socket of a service with listen:.
func (p *Project) SocketUnit(s *Service) string { return p.UnitName(s.Name, ".socket") }

// UnitOf is the unit that stands for a service wherever one unit must: a
// scheduled job is represented by its timer (start/stop arm it, restart
// re-arms it), every other service by itself. The job's own service unit
// runs only when the timer fires.
func (p *Project) UnitOf(s *Service) string {
	if s.Schedule != nil {
		return p.TimerUnit(s)
	}
	return p.ServiceUnit(s)
}

// UnitsOf is every unit a service brings up: what the target wants, up
// starts and start/stop act on. A listening service is its socket and
// itself: started together, so up is eager as compose's is, and stopped
// together, since a socket left listening would start the service again at
// the next connection. restart takes UnitOf alone and keeps the socket.
func (p *Project) UnitsOf(s *Service) []string {
	if len(s.Listen) > 0 {
		return []string{p.SocketUnit(s), p.ServiceUnit(s)}
	}
	return []string{p.UnitOf(s)}
}

// UnitNames lists every unit this run renders, in render order: exactly
// the names Render emits, the enabled services' and nothing of a profile
// that is not active.
func (p *Project) UnitNames() []string { return p.UnitNamesOf(p.EnabledServices()) }

// DeclaredNames lists the units of every service in the file, whatever the
// profiles: what the project owns. down, ps and the orphan sweep take it,
// so a service of an inactive profile is never mistaken for an orphan.
func (p *Project) DeclaredNames() []string { return p.UnitNamesOf(p.Services) }

// UnitNamesOf lists the units of services, with the project's slice and
// target, in render order.
func (p *Project) UnitNamesOf(services []*Service) []string {
	names := []string{p.SliceName()}
	for _, s := range services {
		names = append(names, p.ServiceUnit(s))
	}
	for _, s := range services {
		if s.Schedule != nil {
			names = append(names, p.TimerUnit(s))
		}
	}
	for _, s := range services {
		if len(s.Listen) > 0 {
			names = append(names, p.SocketUnit(s))
		}
	}
	return append(names, p.TargetName())
}
