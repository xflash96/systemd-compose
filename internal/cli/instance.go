package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/systemd"
)

// registerVerbs are up and down outside a project, in systemctl's words.
var registerVerbs = map[string][]string{"up": {"enable", "--now"}, "down": {"disable", "--now"}}

// instanceMode is the verb map outside a project: compose's words for
// systemctl's on one systemd instance.
func instanceMode(m *systemd.Manager, verb string, args []string) error {
	switch verb {
	case "build", "run", "exec":
		return fmt.Errorf("%s needs a project (%s)", verb, noYAML)
	case "up", "down":
		// systemctl would take --force and pass --dry-run along: an up
		// --dry-run UNIT must never enable the unit. up's -d is what
		// enable --now does anyway, and is dropped.
		var units []string
		for _, a := range args {
			switch {
			case verb == "up" && (a == "-d" || a == "--detach"):
			case strings.HasPrefix(a, "-"):
				return fmt.Errorf("%s %s needs a project (%s); outside one, %s UNIT... is systemctl %s and takes no flags", verb, a, noYAML, verb, strings.Join(registerVerbs[verb], " "))
			default:
				units = append(units, a)
			}
		}
		args = units
	}
	fmt.Fprintf(os.Stderr, "(%s: the whole %s)\n", noYAML, m.ScopeName())
	// The journal is there without a manager; everything else asks one.
	if verb != "logs" {
		if err := m.Reachable(); err != nil {
			return err
		}
	}
	needUnit := func() error {
		if len(args) == 0 {
			return fmt.Errorf("%s needs a unit or target", verb) // the scope line above says why
		}
		return nil
	}
	switch verb {
	case "ps":
		return m.Exec("systemctl", append([]string{"list-units", "--type=service,timer"}, args...)...)
	case "logs":
		lf, err := systemd.ComposeLogFlags(args, false)
		if err != nil {
			return err
		}
		flags, units, err := systemd.JournalArgs(lf, func(w string) ([]string, error) { return []string{w}, nil })
		if err != nil {
			return err
		}
		for _, u := range units {
			flags = append(flags, "-u", u)
		}
		return m.Exec("journalctl", flags...)
	case "up", "down":
		if err := needUnit(); err != nil {
			return err
		}
		return m.Exec("systemctl", slices.Concat(registerVerbs[verb], args)...)
	case "start", "stop", "restart", "config":
		if err := needUnit(); err != nil {
			return err
		}
		if verb == "config" {
			verb = "cat"
		}
		return m.Exec("systemctl", append([]string{verb}, args...)...)
	case "top":
		if m.User {
			return systemd.Exec("systemd-cgtop", append(append([]string{"--depth=3"}, args...), m.CGroupPath(""))...)
		}
		return systemd.Exec("systemd-cgtop", append(append([]string{"--depth=2"}, args...), "system.slice")...)
	default:
		return m.Exec("systemctl", append([]string{verb}, args...)...)
	}
}
