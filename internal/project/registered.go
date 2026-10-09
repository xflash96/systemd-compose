package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// registeredUnits are a project's units as registered from its yaml, read
// from their markers, for the verbs that act while the yaml does not load.
type registeredUnits struct {
	all      []string            // every unit, as the unit directory lists them
	services map[string][]string // service -> its units
	idents   map[string]string   // service -> its log tag (SyslogIdentifier=)
}

// registeredVerbs are the verbs asRegistered answers.
var registeredVerbs = strings.Fields("down ps logs stop")

// asRegistered answers registeredVerbs while the yaml does not load (a bad
// edit, a teammate's broken file): the units registered from it carry their
// project and service in their markers, which is all these verbs need, and
// the verbs' own code acts on them. It is not done when no unit is
// registered from the yaml, and the load error stands.
func asRegistered(cfg, verb string, args []string, f Flags, loadErr error) (bool, error) {
	abs, err := filepath.Abs(cfg)
	if err != nil {
		return false, nil
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs)) // as Load: the yaml's own link is not followed
	if err != nil {
		// the directory is gone: copies (registration: copy) still name
		// it, as it was resolved when they were written
		dir = filepath.Dir(abs)
	}
	abs = filepath.Join(dir, filepath.Base(abs))
	m := &systemd.Manager{User: true}
	unitDir, err := m.UnitDir()
	if err != nil {
		return false, nil
	}
	renderDir := filepath.Join(filepath.Dir(abs), config.RenderDirName)
	byProject := map[string]*registeredUnits{}
	rendered := map[string][]render.Rendered{} // project -> the files its units were registered from
	var projects []string
	links, _ := linksInto(unitDir, renderDir)
	for _, l := range links {
		m := render.ReadMarker(l.text)
		if l.err != nil || m.Config != abs {
			continue
		}
		proj := m.Project
		r := byProject[proj]
		if r == nil {
			r = &registeredUnits{services: map[string][]string{}, idents: map[string]string{}}
			byProject[proj] = r
			projects = append(projects, proj)
		}
		r.all = append(r.all, l.name)
		rendered[proj] = append(rendered[proj], render.Rendered{Name: l.name, Text: l.text})
		if svc := m.Service; svc != "" {
			r.services[svc] = append(r.services[svc], l.name)
			if ids := render.Directive(l.text, "Service", "SyslogIdentifier"); len(ids) > 0 {
				r.idents[svc] = ids[len(ids)-1]
			}
		}
	}
	proj := f.Name
	switch {
	case proj != "" && byProject[proj] == nil, len(projects) == 0:
		return false, nil
	case proj == "" && len(projects) > 1:
		return true, fmt.Errorf("%v\n(units of several projects are registered from this yaml: %s; -p NAME picks one)", loadErr, strings.Join(projects, ", "))
	case proj == "":
		proj = projects[0]
	}
	if needsBus(verb) {
		if err := m.Reachable(); err != nil {
			return true, err
		}
	}
	r := byProject[proj]
	fmt.Fprintf(os.Stderr, "systemd-compose: %v\n", loadErr)
	fmt.Printf("note: the yaml does not load, so %s takes project %s as registered from it (%d units)\n", verb, proj, len(r.all))
	pr := &project{p: &config.Project{Name: proj, Dir: filepath.Dir(abs), ConfigPath: abs}, m: m, renderDir: renderDir,
		rendered: rendered[proj], registered: r, sel: f}
	switch verb {
	case "ps":
		return true, pr.table()
	case "down":
		return true, pr.down(args)
	case "stop":
		units := r.all
		if len(args) > 0 {
			units = nil
			for _, a := range args {
				if r.services[a] == nil {
					return true, fmt.Errorf("stop: no service %q is registered from this yaml", a)
				}
				units = append(units, r.services[a]...)
			}
		}
		return true, pr.act("stop", units, nil)
	case "logs":
		return true, pr.logs(args)
	}
	return false, nil
}

// movedFrom finds the yaml this project's directory was moved away from
// without a down: its render directory holds files whose marker names a
// yaml that is gone, and the units of that name are still linked to the
// files' old place. "" when there is none.
func (pr *project) movedFrom(unitDir string) (yaml, name string) {
	entries, err := os.ReadDir(pr.renderDir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(pr.renderDir, e.Name()))
		m := render.ReadMarker(string(data))
		cfg := m.Config
		// a yaml beside this one that is gone (renamed) is no move
		if err != nil || cfg == "" || filepath.Dir(cfg) == pr.p.Dir || exists(cfg) {
			continue
		}
		if l, ok := registered(unitDir, e.Name()); ok && l.target == filepath.Join(filepath.Dir(cfg), config.RenderDirName, e.Name()) {
			return cfg, m.Project
		}
	}
	return "", ""
}

// registration says who owns the search-path entry for a unit name.
type registration struct {
	kind   string // "none" | "ours" | "project" | "foreign"
	owner  string // for "project": the other yaml; for "foreign": what sits there
	gone   bool   // for "project": its files are gone (moved or deleted)
	masked bool   // for "foreign": a link to /dev/null
	copied bool   // for "ours": a copy, not a link (registration: copy)
}

// notOurs starts the note on a name registered by someone else.
const notOurs = "NOT OURS: "

// note is a "project" or "foreign" registration as ps and the verbs print
// it.
func (r registration) note() string { return notOurs + r.owner }

// link is a unit a project registered, as the unit directory holds it: a
// link into a render directory, or a copy of a rendered file
// (registration: copy). target is the render file it stands for, and text
// the unit's text, read through the link or from the copy (err when it
// cannot be read).
type link struct {
	name, target, text string
	err                error
}

// registered reads <unitDir>/<name> as a unit a project registered, under
// the rule registrationOf states; false for anything else (nothing there,
// a hand-written file, a link that points elsewhere).
func registered(unitDir, name string) (link, bool) {
	p := filepath.Join(unitDir, name)
	st, err := os.Lstat(p)
	if err != nil {
		return link{}, false
	}
	if st.Mode().IsRegular() {
		data, err := os.ReadFile(p)
		m := render.ReadMarker(string(data))
		if err != nil || m.Project == "" || m.Config == "" {
			return link{}, false
		}
		return link{name, filepath.Join(filepath.Dir(m.Config), config.RenderDirName, name), string(data), nil}, true
	}
	target, err := os.Readlink(p)
	if err != nil || filepath.Base(filepath.Dir(target)) != config.RenderDirName {
		return link{}, false
	}
	data, err := os.ReadFile(target)
	return link{name, target, string(data), err}, true
}

// renderLinks are the units of every project registered in unitDir. err
// is the unit directory's.
func renderLinks(unitDir string) ([]link, error) {
	entries, err := os.ReadDir(unitDir)
	if err != nil {
		return nil, err
	}
	var out []link
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") { // a copy being written
			continue
		}
		if l, ok := registered(unitDir, e.Name()); ok {
			out = append(out, l)
		}
	}
	return out, nil
}

// linksInto are the units in unitDir registered from renderDir's files,
// links and copies. err is the unit directory's.
func linksInto(unitDir, renderDir string) ([]link, error) {
	all, err := renderLinks(unitDir)
	var out []link
	for _, l := range all {
		if filepath.Dir(l.target) == renderDir {
			out = append(out, l)
		}
	}
	return out, err
}

// RenderDirs maps each render directory units are registered from to its
// project's name: the marker's, or the unit name's when the file cannot
// be read.
func RenderDirs(unitDir string) map[string]string {
	out := map[string]string{}
	links, _ := renderLinks(unitDir)
	for _, l := range links {
		name := render.ReadMarker(l.text).Project
		if name == "" {
			name, _ = config.SplitUnitName(l.name)
		}
		out[filepath.Dir(l.target)] = name
	}
	return out
}

// RegisteredConfig is the yaml project name is registered from, as its
// units' markers say, for -p NAME outside a project: compose's -p reaches
// a project from anywhere. "" when no project of that name is registered.
func RegisteredConfig(name string) (string, error) {
	unitDir, err := (&systemd.Manager{User: true}).UnitDir()
	if err != nil {
		return "", err
	}
	links, err := renderLinks(unitDir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var configs []string
	unreadable := ""
	for _, l := range links {
		if m := render.ReadMarker(l.text); l.err == nil && m.Project != "" {
			if m.Project == name && !slices.Contains(configs, m.Config) {
				configs = append(configs, m.Config)
			}
			continue
		}
		// no marker to read: the unit's name says the project
		if project, _ := config.SplitUnitName(l.name); project == name {
			unreadable = filepath.Dir(filepath.Dir(l.target))
		}
	}
	switch {
	case len(configs) > 1:
		return "", fmt.Errorf("project %s is registered from %s; -f names the yaml to act on", name, strings.Join(configs, " and "))
	case len(configs) == 1:
		return configs[0], nil
	case unreadable != "":
		return "", fmt.Errorf("project %s is registered from %s, whose files cannot be read now (moved, deleted, or on a filesystem that is not mounted); README, \"Moving or deleting a project\"", name, unreadable)
	}
	return "", nil
}

// registrationOf classifies <unitDir>/<name>, the provenance gate: ours is
// a symlink to this project's render file, or a regular file whose marker
// names this project's yaml (a copy, registration: copy); a link into
// another .systemd-compose directory, or a copy whose marker names
// another yaml, is that project's; anything else is foreign, most often a
// hand-written unit. Only "ours" may ever be acted on. A copy's marker is
// taken at its word: copies must be known while the render directory is
// out of reach (a filesystem not mounted), so a rendered file copied into
// the unit directory by hand counts as the project's.
func (pr *project) registrationOf(unitDir, name string) registration {
	p := filepath.Join(unitDir, name)
	st, err := os.Lstat(p)
	if err != nil {
		return registration{kind: "none"}
	}
	if st.Mode()&os.ModeSymlink == 0 {
		if l, ok := registered(unitDir, name); ok {
			if l.target == filepath.Join(pr.renderDir, name) {
				return registration{kind: "ours", copied: true}
			}
			m := render.ReadMarker(l.text)
			return registration{kind: "project", owner: "project " + m.Project + " from " + m.Config + " (a copy)"}
		}
		return registration{kind: "foreign", owner: "a regular file at " + p + " (hand-written?)"}
	}
	target, err := os.Readlink(p)
	if err != nil {
		return registration{kind: "foreign", owner: p}
	}
	if target == filepath.Join(pr.renderDir, name) {
		return registration{kind: "ours"}
	}
	if filepath.Base(filepath.Dir(target)) == filepath.Base(pr.renderDir) {
		owner := "another project (" + target + ")"
		data, err := os.ReadFile(target)
		m := render.ReadMarker(string(data))
		switch {
		case os.IsNotExist(err):
			owner = "a project whose files are gone (" + filepath.Dir(filepath.Dir(target)) + ": moved or deleted)"
		case err == nil && m.Config != "":
			owner = "project " + m.Project + " from " + m.Config
		}
		return registration{kind: "project", owner: owner, gone: os.IsNotExist(err)}
	}
	if target == "/dev/null" {
		return registration{kind: "foreign", owner: "masked (systemctl --user unmask " + name + " undoes that)", masked: true}
	}
	return registration{kind: "foreign", owner: "a link to " + target}
}

// ownedUnits filters unit names to the ones registered by this project,
// returning the rest with a reason. The mutating verbs act only on the
// first list and print the second.
func (pr *project) ownedUnits(names []string) (ours []string, others map[string]string, err error) {
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return nil, nil, err
	}
	others = map[string]string{}
	for _, n := range names {
		r := pr.registrationOf(unitDir, n)
		switch r.kind {
		case "ours":
			ours = append(ours, n)
		case "none":
			others[n] = pr.notRegistered(n)
		default:
			others[n] = r.note()
		}
	}
	return ours, others, nil
}

// notRegistered says why a unit has no registration and what would make one.
func (pr *project) notRegistered(unit string) string {
	if svc := pr.serviceByUnit(unit); svc != nil && !pr.p.Enabled(svc) {
		return "not registered (its profile " + strings.Join(svc.Profiles, "|") + " is not active)"
	}
	return "not registered (up registers it)"
}

// otherNames notes the units registered from this same yaml under another
// project name: a rename (name:, .env, -p) leaves the old name's copy
// running, and starting at boot, beside the new one. lead starts each line:
// "  note: " in a plan, "note: " above a table, "WARNING: " from an up that
// is about to start the second copy.
func (pr *project) otherNames(unitDir, lead string) error {
	links, err := linksInto(unitDir, pr.renderDir)
	if err != nil {
		return nil // nothing registered at all
	}
	units := map[string][]string{}
	var names []string
	for _, l := range links {
		m := render.ReadMarker(l.text)
		other := m.Project
		if l.err != nil || other == "" || other == pr.p.Name || m.Config != pr.p.ConfigPath {
			continue
		}
		if units[other] == nil {
			names = append(names, other)
		}
		units[other] = append(units[other], l.name)
	}
	for _, other := range names {
		states, err := pr.m.States(units[other])
		if err != nil {
			return err
		}
		running := 0
		for _, u := range units[other] {
			if states[u].Active() {
				running++
			}
		}
		// A name given for this run (-p, the environment) is a variant run
		// on purpose (the README's two copies of one yaml); a name from the
		// yaml, the .env or the directory that differs from a registered
		// one is a rename.
		switch {
		case pr.p.NameFrom == "-p" || pr.p.NameFrom == "environment":
			fmt.Printf("%sthis yaml is also registered as project %s (%d units, %d running)\n", strings.Replace(lead, "WARNING: ", "  note: ", 1), other, len(units[other]), running)
		case lead == "WARNING: ":
			fmt.Printf("%sthis yaml is also registered as project %s (%d units, %d running): this up starts a second copy, as project %s. A rename? %s down takes the old one down\n", lead, other, len(units[other]), running, pr.p.Name, pr.cmdAs(other))
		default:
			fmt.Printf("%sthis yaml is also registered as project %s (%d units, %d running), which keeps running and starting at boot; %s down takes it down\n", lead, other, len(units[other]), running, pr.cmdAs(other))
		}
	}
	return nil
}
