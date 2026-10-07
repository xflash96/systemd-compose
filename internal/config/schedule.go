package config

import (
	"fmt"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schedule is schedule:, rendered as a timer that starts the service.
type Schedule struct {
	Calendar        string // OnCalendar
	Accuracy        string // AccuracySec as written; "" for the default, which unit: may override
	Persistent      bool
	RandomizedDelay string // RandomizedDelaySec, optional
	Never           bool   // systemd finds no next elapse: a date passed, or one that never comes
}

func parseSchedule(n *yaml.Node, ctx string) (*Schedule, error) {
	s := &Schedule{Persistent: Default("schedule.persistent") == "true"}
	switch n.Kind {
	case yaml.ScalarNode:
		s.Calendar = n.Value
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": schedule")
		if err != nil {
			return nil, err
		}
		if err := unknownKeys(m, ctx+": schedule", scheduleMap.names()...); err != nil {
			return nil, err
		}
		cn := m.get("calendar")
		if cn == nil {
			return nil, fmt.Errorf("line %d: %s: schedule: calendar: is required in the map form", n.Line, ctx)
		}
		if s.Calendar, err = scalar(cn, ctx+": schedule: calendar"); err != nil {
			return nil, err
		}
		if an := m.get("accuracy"); an != nil {
			if s.Accuracy, err = duration(an, ctx+": schedule: accuracy"); err != nil {
				return nil, err
			}
		}
		if pn := m.get("persistent"); pn != nil {
			if err := pn.Decode(&s.Persistent); err != nil {
				return nil, fmt.Errorf("line %d: %s: schedule: persistent: %v", pn.Line, ctx, err)
			}
		}
		if rn := m.get("randomized_delay"); rn != nil {
			if s.RandomizedDelay, err = duration(rn, ctx+": schedule: randomized_delay"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: schedule: is an OnCalendar string or %s", n.Line, ctx, scheduleMap.braces())
	}
	if strings.TrimSpace(s.Calendar) == "" {
		return nil, fmt.Errorf("line %d: %s: schedule: calendar is empty", n.Line, ctx)
	}
	if err := specifiers(s.Calendar); err != nil {
		return nil, fmt.Errorf("line %d: %s: schedule: %v", n.Line, ctx, err)
	}
	var err error
	if s.Never, err = calendar(s.Calendar); err != nil {
		return nil, fmt.Errorf("line %d: %s: schedule: %v", n.Line, ctx, err)
	}
	return s, nil
}

// calendar asks this host's systemd whether it reads an OnCalendar=
// expression, so a bad one is named at its line rather than by the verify
// gate's lines about a timer file. Without systemd-analyze, verify is left
// to catch it.
func calendar(expr string) (never bool, err error) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		return false, nil
	}
	out, err := exec.Command("systemd-analyze", "calendar", "--iterations=1", "--", expr).CombinedOutput()
	if err == nil {
		return strings.Contains(string(out), "Next elapse: never"), nil
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return false, fmt.Errorf("%q is not a calendar expression systemd reads (%s); systemd-analyze calendar checks one", expr, first)
}
