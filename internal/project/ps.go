package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

func (pr *project) ps() error {
	pr.scopeLine()
	pr.staleNote()
	if unitDir, err := pr.m.UnitDir(); err == nil {
		if err := pr.otherNames(unitDir, "note: "); err != nil {
			return err
		}
	}
	return pr.table()
}

// staleNote says when the yaml (or an env_file) has changes up has not
// applied: the units run, and start or restart, their last rendered
// definition. A render that fails here says nothing; up will say why. When
// the project's names are registered from another directory, or its
// rendered files are gone, there is nothing to compare with: it says that.
func (pr *project) staleNote() {
	offProfile := map[string]bool{} // what a profile this run lacks explains
	unitDir, uerr := pr.m.UnitDir()
	if uerr == nil {
		ours, elsewhere, gone := 0, 0, 0
		for _, n := range pr.p.DeclaredNames() {
			switch r := pr.registrationOf(unitDir, n); r.kind {
			case "ours":
				ours++
				if !exists(filepath.Join(pr.renderDir, n)) {
					gone++
				}
			case "project":
				elsewhere++
			}
		}
		switch {
		case ours == 0 && elsewhere > 0:
			fmt.Println("note: this project's names are registered from another directory (each unit says which); if that is this project before a move, see README, \"Moving or deleting a project\"")
			return
		case gone > 0:
			fmt.Printf("note: %s (a git clean?); the units run what systemd loaded before, and up writes the files again\n", goneFiles(gone))
			return
		}
		if cfg, name := pr.movedFrom(unitDir); cfg != "" {
			fmt.Printf("note: this directory was moved from %s without a down, and project %s is still registered from there; README, \"Moving or deleting a project\"\n", filepath.Dir(cfg), name)
			return
		}
		// Registered under a profile this run does not have: the render
		// differs because of the flags, not the yaml.
		var profiles []string
		for _, svc := range pr.p.Services {
			if !pr.p.Enabled(svc) && pr.registrationOf(unitDir, pr.p.ServiceUnit(svc)).kind == "ours" {
				for _, pf := range svc.Profiles {
					if !slices.Contains(profiles, pf) {
						profiles = append(profiles, pf)
					}
				}
				for _, u := range pr.p.UnitNamesOf([]*config.Service{svc}) {
					offProfile[u] = true // its units, and the target's boot set
				}
			}
		}
		delete(offProfile, pr.p.SliceName()) // the slice is the same under any profile
		if len(profiles) > 0 {
			fmt.Printf("note: this project is registered with profile %s active, and this run has it off; up without --profile %s (or SYSTEMD_COMPOSE_PROFILES) renders a different set\n", strings.Join(profiles, ", "), profiles[0])
		}
	}
	// A service that requires a profile this run has off renders without
	// that edge: the difference is the flags', and up refuses without them.
	if pr.p.NeedsProfile != "" {
		return
	}
	// The registrations above need no render; the comparison below does.
	if err := pr.render(); err != nil {
		fmt.Printf("note: the yaml does not render (%v); up refuses until it does, and the units run their last rendered definition\n", err)
		return
	}
	// The same comparison as up's plan. Not "changed (not applied)": up
	// leaves that to a restart by hand when on_change: start-only holds it.
	var names []string
	for _, u := range pr.rendered {
		names = append(names, u.Name)
	}
	states, _ := pr.m.States(names) // none known reads as changed
	stale, written, missing := false, 0, 0
	rendered := map[string]bool{}
	for _, u := range pr.rendered {
		rendered[u.Name] = true
		if offProfile[u.Name] {
			continue // the profile note above says it
		}
		change, err := pr.changeOf(u.Name, u.Text, states[u.Name])
		switch {
		case err != nil:
		case change == changeText || change == changeEnv:
			stale = true
		case change == changeNew || change == changeNoBaseline || change == changeFileGone:
			missing++
		default:
			written++
		}
	}
	stale = stale || missing > 0 && written > 0 // added, in a project already up
	// A registered unit of this project's that the yaml no longer renders:
	// removed. (An unregistered file is a leftover the next up deletes.)
	if uerr == nil && !stale {
		for _, f := range pr.ownFiles() {
			if !rendered[f] && !offProfile[f] && pr.registrationOf(unitDir, f).kind == "ours" {
				stale = true
				break
			}
		}
	}
	if stale {
		// not only the yaml: an env_file, or the path of a program it names
		fmt.Printf("note: this project has changes up has not applied (%s up --dry-run shows them, %s up applies them)\n", pr.cmd(), pr.cmd())
	}
}

func (pr *project) table() error {
	names := pr.declared()
	states, err := pr.m.States(names)
	if err != nil {
		return err
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	// HEALTH only when something has a healthcheck: the probe's verdict at
	// start, read from systemd; nothing probes a running service.
	health := map[string]string{}
	var probed []string
	for _, s := range pr.p.Services {
		if s.Healthcheck != nil {
			probed = append(probed, pr.p.ServiceUnit(s))
		}
	}
	exits, err := pr.probeExits(states)
	if err != nil {
		return err
	}
	for _, u := range probed {
		health[u] = healthOf(states[u], exits[u])
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	row := func(h string, cells ...string) { // h goes before REGISTERED
		if len(probed) > 0 {
			cells = append(cells[:4:4], h, cells[4])
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	row("HEALTH", "UNIT", "LOAD", "ACTIVE", "SUB", "REGISTERED")
	dash := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}
	for _, n := range names {
		s := states[n]
		r := pr.registrationOf(unitDir, n)
		if r.kind == "project" || r.kind == "foreign" {
			row(health[n], n, s.LoadState, s.ActiveState, s.SubState, r.note())
			continue
		}
		load, active, sub := dash(s.LoadState), dash(s.ActiveState), dash(s.SubState)
		path := filepath.Join(pr.renderDir, n)
		if !s.Known() && r.kind != "ours" {
			row(health[n], n, load, active, sub, pr.notRegistered(n))
			continue
		}
		reg := s.UnitFileState
		switch {
		case r.kind == "ours" && r.copied && !exists(path):
			reg = "copied, but its rendered file is gone (up writes it again)"
		case r.kind == "ours" && r.copied && reg != "enabled":
			reg = "copied" // systemd's word for a unit with no [Install] is "static"
		case r.kind == "ours" && !exists(path):
			// systemd keeps running a unit whose file is gone; the state
			// notes below still apply
			reg = "linked, but its rendered file is gone (up writes it again)"
		case r.kind == "ours" && !s.Known():
			// the manager last loaded it while its file was gone (a
			// reload during a move)
			reg = "linked, but systemd has not loaded its file (up loads it)"
		case r.kind == "none":
			reg = pr.notRegistered(n) // a slice systemd made on demand is loaded anyway
		case reg == "":
			reg = "-"
		}
		inactive := ""
		svc := pr.serviceByUnit(n)
		logs := pr.logsCmd(n)
		scheduled := svc != nil && svc.Schedule != nil
		job := scheduled && n == pr.p.ServiceUnit(svc)
		timer := scheduled && n == pr.p.TimerUnit(svc)
		svcDown := svc != nil && n == pr.p.ServiceUnit(svc) && !s.Active()
		switch {
		case svc != nil && !pr.p.Enabled(svc):
			inactive = " (profile " + strings.Join(svc.Profiles, "|") + " not active)"
		case job:
			switch {
			case s.ActiveState == "failed":
				inactive = " (its last run failed: " + logs + ")"
			case s.ActiveState == "activating":
				inactive = " (a run is in progress)"
			}
			if !states[pr.p.TimerUnit(svc)].Active() {
				inactive += " (its timer is stopped, so it does not run; " + pr.cmd() + " start " + svc.Name + " arms it)"
			} else if s.ActiveState != "activating" {
				inactive += " (runs on its timer)"
			}
		case timer && s.NextRun != "":
			inactive = ", next run " + s.NextRun
		case timer && s.SubState == "running":
			inactive = ", running its job now"
		case timer && s.Active():
			// elapsed: active, with nothing more to run; its job has run,
			// or never will
			inactive = ", no next run: its calendar has passed"
		case svc != nil && s.ActiveState == "failed" && s.NRestarts > 0:
			inactive = fmt.Sprintf(" (systemd stopped restarting it after %s; %s, then %s restart %s)", times(s.NRestarts), logs, pr.cmd(), svc.Name)
		case svc != nil && s.ActiveState == "failed":
			inactive = " (" + logs + ")"
		case svc != nil && s.Restarting() && s.NRestarts == 0:
			inactive = " (not running: systemd is about to retry it; " + logs + ")"
		case svc != nil && s.Restarting():
			inactive = fmt.Sprintf(" (not running: systemd is restarting it, restart #%d; %s)", s.NRestarts, logs)
		case svcDown && len(svc.Listen) > 0 && states[pr.p.SocketUnit(svc)].ActiveState == "failed":
			inactive = " (its socket failed: " + logs + ")"
		case svcDown:
			if dep := pr.downDependency(svc, states); dep != "" {
				inactive = " (needs " + dep + ", which is not up)"
			}
		}
		if pr.notApplied(svc, n, s) {
			inactive += " (on an older definition; " + pr.applyCmd(n) + " applies it)"
		}
		if s.NRestarts > 0 && s.Active() && !s.Restarting() {
			inactive += " (restarted " + times(s.NRestarts) + " since it was started)"
		}
		row(health[n], n, load, active, sub, reg+inactive)
	}
	// What an older yaml registered from here, until up or down retires it.
	orphans, err := pr.orphans(unitDir)
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		var units []string
		for _, o := range orphans {
			units = append(units, o.unit)
		}
		ost, err := pr.m.States(units)
		if err != nil {
			return err
		}
		for _, o := range orphans {
			s := ost[o.unit]
			fate := "orphan: not in the yaml (the next up or down retires it)"
			if o.refused() {
				fate = "orphan: not in the yaml, and running (down retires it; up refuses until it is stopped, or with --force retires it)"
			}
			row("", o.unit, s.LoadState, s.ActiveState, s.SubState, fate)
		}
	}
	// A run or build in progress, or one whose client went away.
	oneOffs, err := pr.oneOffs()
	if err != nil {
		return err
	}
	if len(oneOffs) > 0 {
		ost, err := pr.m.States(oneOffs)
		if err != nil {
			return err
		}
		for _, n := range oneOffs {
			s := ost[n]
			row("", n, s.LoadState, s.ActiveState, s.SubState, "a one-off (systemd-compose run or build) still running; systemctl --user stop "+n+" ends it")
		}
	}
	return w.Flush()
}

// didNotStart starts the line of a service whose start failed for a
// reason outside it; up's report puts those lines after their causes'.
const didNotStart = "did not start"

// needsNote says a service did not start for a dependency that is not up.
func needsNote(dep string) string { return ": it needs " + dep + ", which is not up" }

// downDependency names a service this one Requires= that is not up (active
// and, with a healthcheck, past it): systemd starts this one only after
// that one, and stops it with that one.
func (pr *project) downDependency(svc *config.Service, states map[string]systemd.UnitState) string {
	for _, d := range svc.DependsOn {
		dep := pr.p.Service(d.Service)
		if d.Required && dep != nil && states[pr.p.UnitOf(dep)].ActiveState != "active" {
			return d.Service
		}
	}
	return ""
}
