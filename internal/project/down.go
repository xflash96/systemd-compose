package project

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

func (pr *project) down(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("down acts on the whole project (stop SERVICE stops one service)")
	}
	pr.scopeLine()
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	names := pr.declared() // every profile: down leaves nothing of the project registered
	registered, others, err := pr.ownedUnits(names)
	if err != nil {
		return err
	}
	held := false         // a name of ours registered by someone or something else
	var gone registration // one of them, from files that are gone
	for _, n := range names {
		if why, skip := others[n]; skip && strings.HasPrefix(why, notOurs) {
			fmt.Printf("  %-32s %s\n", n, why)
			held = true
			if r := pr.registrationOf(unitDir, n); r.gone {
				gone = r
			}
		}
	}
	orphans, err := pr.orphans(unitDir)
	if err != nil {
		return err
	}
	// A run or build still going is part of what down stops; while the
	// project's names are held from elsewhere, so are its one-offs, and down
	// touches nothing. One that ended since it was listed is no longer
	// loaded, which is nothing to stop.
	var offs []string
	if !held {
		if offs, err = pr.oneOffs(); err != nil {
			return err
		}
	}
	if len(offs) > 0 {
		if said, err := pr.m.RunQuiet(append([]string{"stop"}, offs...)...); err != nil && !onlyNotLoaded(said) {
			return fmt.Errorf("stop: %s", said)
		}
	}
	stopped := 0
	if len(registered) > 0 {
		if stopped, err = pr.unregister(registered); err != nil {
			return err
		}
	}
	// What an older yaml registered from here goes too, active or not: down
	// is the verb that stops things, and a project that is never upped again
	// would otherwise keep those links forever.
	if err := pr.retire(orphans); err != nil {
		return err
	}
	systemd.SweepEmptyWants(unitDir)
	pr.removeLeftovers(pr.leftovers(unitDir))
	// A start still queued when down began (an up interrupted while it
	// waited) can bring the target back after the stop; stop what came
	// back, of what down unregistered: a target or slice of this name that
	// is not ours (another copy's, a hand-written one) is not down's.
	var mine []string
	for _, u := range []string{pr.p.TargetName(), pr.p.SliceName()} {
		if slices.Contains(registered, u) {
			mine = append(mine, u)
		}
	}
	if len(mine) > 0 {
		back, err := pr.m.States(mine)
		if err != nil {
			return err
		}
		var again []string
		for _, u := range mine {
			if back[u].Active() {
				again = append(again, u)
			}
		}
		if len(again) > 0 {
			if err := pr.m.Stop(again); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
		}
	}
	if err := pr.otherNames(unitDir, "  note: "); err != nil {
		return err
	}
	// A unit of this project still running that nothing registers (its
	// link removed by hand, a mask) is no unit down may take, a name being
	// no ownership; but down must not say the project is down.
	strays := 0
	st, err := pr.m.States(names)
	if err != nil {
		return err // unknown is no "the project is down"
	}
	for _, n := range names {
		r := pr.registrationOf(unitDir, n)
		if strings.HasSuffix(n, ".service") && !st[n].Active() && pr.leftRunning(n) {
			// SendSIGKILL=no: its stop timed out and systemd gave up on
			// it, so the project is not down
			fmt.Printf("  %-32s still running: it outlived its stop timeout, and SendSIGKILL=no left it; systemctl --user kill -s KILL %s ends it\n", n, unitWords(n))
			strays++
			continue
		}
		// the slice runs only while something in it does, which is named
		if n == pr.p.SliceName() || !st[n].Running() || r.kind != "none" && !r.masked {
			continue
		}
		why := "its registration was removed by hand"
		if r.masked {
			why = "it is masked"
		}
		fmt.Printf("  %-32s still running, though nothing registers it (%s): systemctl --user stop %s\n", n, why, unitWords(n))
		strays++
	}
	if strays > 0 {
		if len(registered) > 0 { // what down did, which the stray's error alone leaves out
			fmt.Println(pr.downLine(stopped, len(offs), len(registered)))
		}
		return fmt.Errorf("%s of project %s still running, unregistered (above)", plural(strays, "unit"), pr.p.Name)
	}
	if len(registered) == 0 && len(orphans) == 0 {
		if held {
			return fmt.Errorf("nothing disabled: the names above are registered from somewhere else. If that is this project before a move, %s", gone.retire())
		}
		// Moved under a new name (the directory's): the old one runs on.
		if cfg, r := pr.movedFrom(unitDir); cfg != "" {
			return fmt.Errorf("nothing of project %s is registered; but this directory was moved from %s without a down, and project %s is still registered from there (ls shows what of it runs): %s", pr.p.Name, filepath.Dir(cfg), r.project, r.retire())
		}
		if len(offs) > 0 {
			fmt.Printf("down: %s stopped; nothing of project %s is registered\n", plural(len(offs), "one-off command"), pr.p.Name)
			return nil
		}
		fmt.Printf("down: nothing of project %s is registered\n", pr.p.Name)
		return nil
	}
	fmt.Println(pr.downLine(stopped, len(offs), len(registered)))
	return nil
}

// onlyNotLoaded reports systemd's words on a stop that failed only for
// units that are no longer loaded.
func onlyNotLoaded(said string) bool {
	for _, l := range strings.Split(said, "\n") {
		if l != "" && !strings.Contains(l, "not loaded") {
			return false
		}
	}
	return said != ""
}

// downLine is down's last word: what it stopped and unregistered, and where
// the rendered files are, if they are anywhere.
func (pr *project) downLine(stopped, oneOffs, units int) string {
	files := "their rendered files stay in " + pr.renderDir
	if !exists(pr.renderDir) {
		files = "the rendered files were already gone (" + config.RenderDirName + "/)"
	}
	what := plural(stopped, "service")
	if oneOffs > 0 {
		what += " and " + plural(oneOffs, "one-off command")
	}
	return fmt.Sprintf("down: %s stopped, %s unregistered; %s", what, plural(units, "unit"), files)
}

// leftRunning reports processes still in a service's cgroup after its
// stop: SendSIGKILL=no makes systemd give up at the stop timeout, and leave
// them running.
func (pr *project) leftRunning(unit string) bool {
	return systemd.Leftover(pr.m.CGroupPath(pr.p.SliceName()+"/"+unit)) > 0
}
