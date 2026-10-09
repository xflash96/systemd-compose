package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// List lists every project registered on the user instance: a link
// into a render directory is a project's unit, and the marker in the file it
// points at names the project, its yaml and the service the unit belongs to.
// The count is of services, not units: the marker groups a service's units
// (its socket is not a second service), and the one that stands for it is
// its timer when it has one, since a job's own service runs only while the
// timer fires it. The project's slice and target carry no service and are
// not counted.
func List(m *systemd.Manager) error {
	unitDir, err := m.UnitDir()
	if err != nil {
		return err
	}
	links, err := renderLinks(unitDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	type project struct {
		name, config string
		gone         bool // no marker could be read

		services map[string][]string // service name -> the units it rendered
		order    []string            // the service names, in the order found
	}
	byKey := map[string]*project{}
	var keys []string
	for _, l := range links {
		target, err := l.target, l.err
		m := render.ReadMarker(l.text)
		name, where, service := m.Project, m.Config, m.Service
		gone := err != nil || name == ""
		if gone {
			// No marker to read: the unit's own name says the project (up
			// to the first dash, as its dashes are escaped) and the service.
			dir := filepath.Dir(filepath.Dir(target))
			why := "files unreadable"
			switch {
			case os.IsNotExist(err) && exists(dir) && !exists(filepath.Join(dir, config.ConfigFileName)):
				why = "yaml and rendered files gone; put the yaml back and run down there" // up there could not write them
			case os.IsNotExist(err) && exists(dir):
				why = "rendered files gone; systemd-compose up there writes them again"
			case os.IsNotExist(err):
				why = "directory gone; README, \"Moving or deleting a project\""
			}
			name, service, _ = strings.Cut(strings.TrimSuffix(l.name, filepath.Ext(l.name)), "-")
			name = config.UnescapeName(name)
			where = dir + " (" + why + ")"
		}
		k := name + "\x00" + where
		if byKey[k] == nil {
			byKey[k] = &project{name: name, config: where, gone: gone, services: map[string][]string{}}
			keys = append(keys, k)
		}
		p := byKey[k]
		if service == "" {
			continue // the project's slice or target
		}
		if _, dup := p.services[service]; !dup {
			p.order = append(p.order, service)
		}
		p.services[service] = append(p.services[service], l.name)
	}
	if len(keys) == 0 {
		fmt.Println("no project is registered on the user instance")
		return nil
	}
	// One unit stands for each service; only those need their state.
	stands := map[string]string{} // project key + service -> unit
	var all []string
	for _, k := range keys {
		p := byKey[k]
		for _, s := range p.order {
			rep := ""
			for _, u := range p.services[s] {
				if strings.HasSuffix(u, ".timer") {
					rep = u
					break
				}
				if rep == "" || strings.HasSuffix(u, ".service") {
					rep = u
				}
			}
			stands[k+"\x00"+s] = rep
			all = append(all, p.services[s]...) // a job's own unit too: its last run
		}
	}
	states := map[string]systemd.UnitState{}
	if len(all) > 0 { // systemctl show with no unit shows the manager's own
		if states, err = m.States(all); err != nil {
			return err
		}
	}
	sort.Strings(keys)
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tCONFIG")
	for _, k := range keys {
		p := byKey[k]
		active, total, restarting, restarts, failed, failedRuns := 0, 0, 0, 0, 0, 0
		for _, s := range p.order {
			total++
			st := states[stands[k+"\x00"+s]]
			restarts += st.NRestarts
			switch {
			case st.Restarting():
				restarting++
			case st.Active():
				active++
			}
			// failed: the service itself, or (its timer standing for it) a
			// scheduled job's last run, which is no service that is down
			for _, u := range p.services[s] {
				if states[u].ActiveState != "failed" {
					continue
				}
				if strings.HasSuffix(stands[k+"\x00"+s], ".timer") && strings.HasSuffix(u, ".service") {
					failedRuns++
				} else {
					failed++
				}
				break
			}
		}
		status := fmt.Sprintf("running %d/%d", active, total)
		if active == 0 {
			status = fmt.Sprintf("stopped 0/%d", total)
		}
		if total == 0 {
			status = "no service registered (an up that stopped early?)"
		}
		if failed > 0 {
			status += fmt.Sprintf(", %d failed", failed)
		}
		if failedRuns > 0 {
			status += ", " + plural(failedRuns, "failed run")
		}
		if restarting > 0 {
			status += fmt.Sprintf(", %d restarting", restarting)
		}
		// A crash loop is running between its crashes; the count tells.
		if restarts > 0 {
			status += fmt.Sprintf(", restarted %s", times(restarts))
		}
		config := p.config
		if _, err := os.Stat(config); err != nil && !p.gone {
			// no project verb reaches it from there until it is back
			config += " (yaml gone: put it back and run down there; README, \"Moving or deleting a project\")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", p.name, status, config)
	}
	return w.Flush()
}
