package project

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/systemd"
)

func (pr *project) logs(args []string) error {
	if (slices.Contains(args, "-t") || slices.Contains(args, "--timestamps")) && slices.Contains(args, "--no-log-prefix") {
		return fmt.Errorf("logs: -t and --no-log-prefix disagree: --no-log-prefix is journalctl -o cat, which drops the timestamp with the prefix; lines carry a timestamp by default, so drop --no-log-prefix to keep it")
	}
	lf, err := systemd.ComposeLogFlags(args, true)
	if err != nil {
		return err
	}
	var idents []string
	flags, units, err := systemd.JournalArgs(lf, func(name string) ([]string, error) {
		if r := pr.registered; r != nil {
			if len(r.services[name]) == 0 {
				return nil, fmt.Errorf("logs: no service %q is registered from this yaml", name)
			}
			idents = append(idents, r.idents[name])
			return r.services[name], nil
		}
		s, err := pr.p.Lookup(name)
		if err != nil {
			return nil, err
		}
		idents = append(idents, pr.p.LogIdentifier(s))
		// the service, and what systemd says about its socket or timer
		// (a port already taken is the socket's line)
		units := []string{pr.p.ServiceUnit(s)}
		if len(s.Listen) > 0 {
			units = append(units, pr.p.SocketUnit(s))
		}
		if s.Schedule != nil {
			units = append(units, pr.p.TimerUnit(s))
		}
		return units, nil
	})
	if err != nil {
		return err
	}
	if len(units) == 0 {
		// The slice holds what the services print (and run and build
		// units); the units themselves add the manager's words about them:
		// why one failed, its restarts.
		units = pr.declared() // the slice first, then every unit
		if r := pr.registered; r != nil {
			for _, svc := range slices.Sorted(maps.Keys(r.idents)) {
				idents = append(idents, r.idents[svc])
			}
		}
		for _, s := range pr.p.Services {
			idents = append(idents, pr.p.LogIdentifier(s))
		}
	}
	return pr.m.Exec("journalctl", append(flags, journalMatches(units, idents)...)...)
}

// journalMatches is what journalctl --user -u UNIT matches for each unit
// (the unit's own lines, the manager's about it, its core dumps; a slice's
// processes), or'ed with each service's journal tag: a line printed by a
// child that exits at once reaches the journal with no unit name, as
// journald finds the child gone. On systemd 249 and 255, every line of a
// date run in a shell loop carries the tag and none carries the unit. -u
// and -t cannot be or'ed, so the matches are spelled out, joined by
// journalctl's "+".
func journalMatches(units, idents []string) []string {
	var terms []string
	for _, u := range units {
		if strings.HasSuffix(u, ".slice") {
			terms = append(terms, "_SYSTEMD_USER_SLICE="+u)
		} else {
			terms = append(terms, "_SYSTEMD_USER_UNIT="+u, "COREDUMP_USER_UNIT="+u)
		}
		terms = append(terms, "USER_UNIT="+u, "OBJECT_SYSTEMD_USER_UNIT="+u)
	}
	for _, id := range idents {
		terms = append(terms, "SYSLOG_IDENTIFIER="+id)
	}
	var out []string
	for i, t := range terms {
		if i > 0 {
			out = append(out, "+")
		}
		out = append(out, t)
	}
	return out
}
