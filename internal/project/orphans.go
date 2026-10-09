package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/render"
)

// ownRendered are the files in the render directory whose marker names
// this project, with their text.
func (pr *project) ownRendered() []render.Rendered {
	var out []render.Rendered
	entries, _ := os.ReadDir(pr.renderDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // a write in flight (.tmp-), not a unit's file; up sweeps old ones
		}
		if data, err := os.ReadFile(filepath.Join(pr.renderDir, e.Name())); err == nil && render.ReadMarker(string(data)).Project == pr.p.Name {
			out = append(out, render.Rendered{Name: e.Name(), Text: string(data)})
		}
	}
	return out
}

// leftovers are this project's rendered files for units the yaml does not
// declare and nothing registered (a down, then a service dropped): nobody's
// definition, so up and down delete them.
func (pr *project) leftovers(unitDir string) []string {
	declared := map[string]bool{}
	for _, n := range pr.declared() {
		declared[n] = true
	}
	var out []string
	for _, u := range pr.ownRendered() {
		if !declared[u.Name] && pr.registrationOf(unitDir, u.Name).kind == "none" {
			out = append(out, u.Name)
		}
	}
	return out
}

func (pr *project) removeLeftovers(files []string) {
	for _, f := range files {
		if err := os.Remove(filepath.Join(pr.renderDir, f)); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  warning: %s: not removed: %v\n", f, err)
		}
	}
}

// takenAddresses splits off the orphans that must go before the start: a
// dropped socket that listens on an address a socket of this render takes
// (a service renamed with its listen:), with its service, which holds the
// socket's fd. The new socket cannot bind the address while the old one
// holds it.
func (pr *project) takenAddresses(orphans []orphan) (early, later []orphan) {
	wanted := map[string]bool{}
	for _, u := range pr.rendered {
		if strings.HasSuffix(u.Name, ".socket") {
			for _, a := range render.Directive(u.Text, "Socket", "ListenStream") {
				wanted[a] = true
			}
		}
	}
	first := map[string]bool{}
	for _, o := range orphans {
		if !strings.HasSuffix(o.unit, ".socket") {
			continue
		}
		text, err := os.ReadFile(filepath.Join(pr.renderDir, o.unit))
		if err != nil {
			continue
		}
		if slices.ContainsFunc(render.Directive(string(text), "Socket", "ListenStream"), func(a string) bool { return wanted[a] }) {
			first[o.unit] = true
			first[strings.TrimSuffix(o.unit, ".socket")+".service"] = true
		}
	}
	for _, o := range orphans {
		if first[o.unit] {
			early = append(early, o)
		} else {
			later = append(later, o)
		}
	}
	return early, later
}

type orphan struct {
	unit   string
	active bool
	shedBy string // a service still in the yaml that no longer has this socket or timer
}

// refused is an orphan up retires only with --force: one running that no
// service of the yaml shed.
func (o orphan) refused() bool { return o.active && o.shedBy == "" }

// unregister takes units off the manager: a stop of the units that are
// running (timers before the jobs they trigger), then one disable, then
// clear any failed state. The stop goes first, while the manager still has
// the files loaded: after the disable's reload a unit has forgotten its
// TimeoutStopSec= and is stopped on the 90s default, which kills a slow
// drain or waits out a quick one. disable --now would stop after the
// reload, which unloads an idle unit, and systemctl refuses to stop a unit
// that is not loaded. systemd's changed-on-disk warning a stop can raise
// is filtered (Manager.Run).
func (pr *project) unregister(units []string) (stopped int, err error) {
	states, err := pr.m.States(units)
	if err != nil {
		return 0, err
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return 0, err
	}
	// Timers first, then sockets on their own: a socket stopped in one
	// transaction with its service re-triggers it while a connection
	// waits in its backlog, and the whole stop fails ("Job canceled").
	var running []string
	var groups [3][]string // timers, sockets, the rest
	for _, u := range units {
		st := states[u].ActiveState
		if st == "" || st == "inactive" || st == "failed" {
			continue
		}
		g := 2
		switch {
		case strings.HasSuffix(u, ".timer"):
			g = 0
		case strings.HasSuffix(u, ".socket"):
			g = 1
		}
		groups[g] = append(groups[g], u)
		running = append(running, u)
		if strings.HasSuffix(u, ".service") {
			stopped++ // a finished oneshot too: ls counts it as running, and down stops it
		}
	}
	// A stop that fails does not keep the units registered: the disable
	// below goes either way, and what still runs after it is named.
	for _, g := range groups {
		if len(g) > 0 {
			pr.m.Stop(g) // systemd says why, if it fails
		}
	}
	var still []string
	if len(running) > 0 {
		if after, err := pr.m.States(running); err == nil {
			for _, u := range running {
				if after[u].Result == "timeout" {
					fmt.Printf("  %s ignored SIGTERM and was killed at its stop timeout\n", u)
				}
				if after[u].Running() {
					still = append(still, u)
				}
			}
		}
	}
	// systemctl refuses to disable a unit whose file is gone (a git clean
	// took the render directory): its links go by hand, as disable would
	// remove them, and the reload makes the manager forget the file. A
	// copy (registration: copy) is a file of its own, which disable leaves
	// in place: it goes by hand after the disable.
	var present, copies []string
	for _, u := range units {
		if r := pr.registrationOf(unitDir, u); r.kind == "ours" && r.copied {
			present, copies = append(present, u), append(copies, u)
		} else if exists(filepath.Join(pr.renderDir, u)) {
			present = append(present, u)
		} else if err := unlink(unitDir, u, filepath.Join(pr.renderDir, u)); err != nil {
			return 0, err
		}
	}
	verb := append([]string{"disable"}, present...)
	if len(present) == 0 {
		verb = []string{"daemon-reload"}
	}
	if said, err := pr.m.RunQuiet(verb...); err != nil {
		// one file systemd cannot read fails the whole disable, and every
		// unit would stay registered and in the boot set: the links are ours,
		// so they go by hand, as disable would remove them; systemd's words
		// only if that fails too
		for _, u := range present {
			if err := unlink(unitDir, u, filepath.Join(pr.renderDir, u)); err != nil {
				fmt.Fprintln(os.Stderr, said)
				return 0, err
			}
		}
		if err := pr.m.Run("daemon-reload"); err != nil {
			fmt.Fprintln(os.Stderr, said)
			return 0, fmt.Errorf("daemon-reload: %w", err)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(said), "\n")
		fmt.Printf("  (systemctl disable failed: %s; the registrations were removed by hand instead)\n", first)
	}
	if len(copies) > 0 {
		for _, u := range copies {
			if err := os.Remove(filepath.Join(unitDir, u)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err := pr.m.Run("daemon-reload"); err != nil {
			return 0, fmt.Errorf("daemon-reload: %w", err)
		}
	}
	rfErr := pr.m.ResetFailed(units)
	if len(still) > 0 {
		return stopped, fmt.Errorf("unregistered, but %s still running after the stop: systemctl --user stop %s", strings.Join(still, ", "), strings.Join(still, " "))
	}
	return stopped, rfErr
}

// unlink removes the links to target that link and enable made for a unit
// name: the one in unitDir, and any in its .wants and .requires
// directories. A link to anything else is not touched.
func unlink(unitDir, name, target string) error {
	paths := []string{filepath.Join(unitDir, name)}
	entries, _ := os.ReadDir(unitDir)
	for _, e := range entries {
		if e.IsDir() && (strings.HasSuffix(e.Name(), ".wants") || strings.HasSuffix(e.Name(), ".requires")) {
			paths = append(paths, filepath.Join(unitDir, e.Name(), name))
		}
	}
	for _, p := range paths {
		if t, err := os.Readlink(p); err == nil && (t == target || t == paths[0]) {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// retire unregisters orphans and deletes their render files: nothing in the
// yaml describes them any more.
func (pr *project) retire(orphans []orphan) error {
	if len(orphans) == 0 {
		return nil // unregister of nothing would still reload the manager
	}
	var units []string
	for _, o := range orphans {
		units = append(units, o.unit)
	}
	if _, err := pr.unregister(units); err != nil {
		return fmt.Errorf("orphans: %w", err)
	}
	for _, u := range units {
		if err := os.Remove(filepath.Join(pr.renderDir, u)); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  warning: %s: render file not removed: %v\n", u, err)
		}
		fmt.Printf("  retired orphan %s\n", u)
	}
	return nil
}

// orphans finds units registered from this project's render directory that
// the yaml no longer declares. Provenance is the render directory a link
// points into, or a copy's marker names, plus the marker section; a unit
// that merely shares the name prefix is
// somebody else's and is never touched. One whose marker names a service
// still in the yaml is a socket or timer that service shed (shedBy).
func (pr *project) orphans(unitDir string) ([]orphan, error) {
	declared := map[string]bool{}
	for _, n := range pr.declared() {
		declared[n] = true
	}
	links, err := linksInto(unitDir, pr.renderDir)
	if os.IsNotExist(err) {
		return nil, nil // nothing was ever registered on this instance
	}
	if err != nil {
		return nil, fmt.Errorf("orphans: %w", err)
	}
	var found []string
	shedBy := map[string]string{}
	for _, l := range links {
		if declared[l.name] || filepath.Base(l.target) != l.name {
			continue
		}
		// Ownership must be READ, never assumed: several projects may render
		// into this directory, and a file that cannot be read is nobody's.
		m := render.ReadMarker(l.text)
		if l.err != nil || m.Project != pr.p.Name {
			if l.err != nil {
				fmt.Printf("  %-32s registered from here but unreadable (%v); not touched\n", l.name, l.err)
			}
			continue
		}
		found = append(found, l.name)
		if s := pr.p.Service(m.Service); s != nil {
			shedBy[l.name] = s.Name
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	states, err := pr.m.States(found)
	if err != nil {
		return nil, err
	}
	var out []orphan
	for _, n := range found {
		// a oneshot that has run stays active with nothing running, and so
		// does a timer waiting for its next run: there is nothing to
		// protect, and dropping a scheduled job needs no --force. A run in
		// progress is its service's, still refused.
		armed := strings.HasSuffix(n, ".timer") && states[n].SubState == "waiting"
		out = append(out, orphan{n, states[n].Running() && !armed, shedBy[n]})
	}
	return out, nil
}
