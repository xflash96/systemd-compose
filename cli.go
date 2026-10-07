// cli.go — the verbs. Two modes: inside a project (a systemd-compose.yaml
// here or in any parent directory, compose's discovery) every verb is scoped
// to that project on the user instance; outside one, the verbs are the
// docker-compose spelling of systemctl on the user instance too, so leaving
// a project directory never changes which instance answers. --system (-s)
// picks the system instance; running as root defaults to it, since root's
// user manager is rarely what anyone means. Anything unrecognised passes
// through untouched.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const help = `systemd-compose — docker-compose verbs over systemd.

  systemd-compose [--system|-s] [-p NAME] VERB [ARGS...]

INSIDE A PROJECT (a systemd-compose.yaml here or in any parent directory, as
compose finds its file) every verb is scoped to it, on the user instance;
--system is refused. The project NAME is the namespace: every unit, the
target and the slice carry it. It comes from, in order: -p NAME, the
SYSTEMD_COMPOSE_PROJECT_NAME variable in the environment, the same variable
in a .env beside the yaml, name: in the yaml, the directory's name.

  up [--build] [--force] [--force-recreate|--no-recreate]
                           render, verify, register and start the project;
                           restart what changed (on_change: start-only warns);
                           --force-recreate restarts every running service,
                           --no-recreate none; --force retires active orphans
  down                     stop and unregister every unit, and retire what an
                           older yaml left registered; the current files stay
  ps                       the project's units and their state
  logs [-f] [SERVICE...]   the project's journal; no SERVICE = all of it
  start|stop|restart [SERVICE...]   run state only; no SERVICE = all
  build [SERVICE...]       run build: steps in the service's own environment
  config                   the yaml, then every unit as it would be rendered
  top                      systemd-cgtop on the project slice
  VERB [SERVICE...]        systemctl --user VERB, service names mapped to units

OUTSIDE A PROJECT: the same words on the user instance (the system instance
when run as root); --system or -s for the system instance explicitly.

  ps [-a] [PATTERN]        list-units --type=service,timer   (-a: stopped too)
  logs [-f] [UNIT...]      journalctl, one -u per UNIT; no UNIT = the whole journal
  start|stop|restart UNIT...   the same words in systemctl
  up UNIT...               enable --now: register for boot and start
  down UNIT...             disable --now: stop and unregister; the file stays
  config UNIT...           systemctl cat
  top                      systemd-cgtop on the instance's cgroup subtree
  VERB ARGS...             systemctl VERB ARGS
`

type flags struct {
	user, system bool
	name         string // -p
}

func run(args []string) error {
	var f flags
	for len(args) > 0 {
		switch args[0] {
		case "--user":
			f.user = true
		case "--system", "-s":
			f.system = true
		case "-p", "--project-name":
			if len(args) < 2 {
				return fmt.Errorf("%s needs a name", args[0])
			}
			f.name = args[1]
			args = args[1:]
		default:
			if v, ok := strings.CutPrefix(args[0], "--project-name="); ok {
				f.name = v
				args = args[1:]
				continue
			}
			goto parsed
		}
		args = args[1:]
	}
parsed:
	verb := "help"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	switch verb {
	case "help", "-h", "--help":
		fmt.Print(help)
		return nil
	case "probe":
		return probe(args)
	}
	if cfg := FindConfig("."); cfg != "" {
		if f.system {
			return fmt.Errorf("--system is refused inside a project (%s): projects live on the user instance, which cannot link files under /home into /etc", cfg)
		}
		return projectMode(cfg, verb, args, f.name)
	}
	if f.name != "" {
		return fmt.Errorf("-p names a project, and there is no systemd-compose.yaml here or above")
	}
	// User instance by default, system as root; either flag is explicit.
	m := &Manager{User: !f.system && (f.user || os.Getuid() != 0)}
	return instanceMode(m, verb, args)
}

// ---- project mode ---------------------------------------------------------

type project struct {
	p         *Project
	m         *Manager
	renderDir string
	rendered  []Rendered // filled by render()
	opt       RenderOptions
}

func projectMode(cfg, verb string, args []string, nameFlag string) error {
	p, err := Load(cfg, nameFlag)
	if err != nil {
		return err
	}
	pr := &project{p: p, m: &Manager{User: true}, renderDir: filepath.Join(p.Dir, ".systemd-compose")}
	switch verb {
	case "up":
		return pr.up(args)
	case "down":
		return pr.down(args)
	case "ps":
		return pr.ps()
	case "logs":
		return pr.logs(args)
	case "start", "stop", "restart":
		units, err := pr.unitsFor(verb, args)
		if err != nil {
			return err
		}
		ours, others, err := pr.ownedUnits(units)
		if err != nil {
			return err
		}
		pr.scopeLine()
		for _, n := range units {
			if why, skip := others[n]; skip {
				fmt.Printf("  %-32s %s\n", n, why)
			}
		}
		if len(ours) == 0 {
			return fmt.Errorf("%s: none of these units is registered by this project", verb)
		}
		return pr.m.ExecSystemctl(append([]string{verb}, ours...)...)
	case "build":
		return pr.build(args)
	case "config":
		return pr.config()
	case "top":
		return Exec("systemd-cgtop", append(append([]string{"--depth=2"}, args...), pr.m.CGroupPath(p.SliceName()))...)
	default:
		// A service name maps to its unit only when that unit is ours; the
		// name of a unit somebody else registered is never handed to a verb.
		unitDir, err := pr.m.UnitDir()
		if err != nil {
			return err
		}
		mapped := make([]string, 0, len(args))
		for _, a := range args {
			s := pr.p.Service(a)
			if s == nil {
				mapped = append(mapped, a)
				continue
			}
			u := pr.p.UnitOf(s)
			if r := pr.registrationOf(unitDir, u); r.kind != "ours" && r.kind != "none" {
				return fmt.Errorf("%s: %s is %s; this project does not own it", verb, u, r.owner)
			}
			mapped = append(mapped, u)
		}
		return pr.m.ExecSystemctl(append([]string{verb}, mapped...)...)
	}
}

func (pr *project) scopeLine() {
	fmt.Printf("project %s (%s, name from %s): %d units, from %s\n", pr.p.Name, pr.m.ScopeName(), pr.p.NameFrom, len(pr.p.UnitNames()), pr.p.ConfigPath)
}

// unitsFor maps service names to the units start, stop or restart acts on;
// no names means every service. restart takes the one unit that stands for
// a service (a listening service keeps its socket open across it); start
// and stop take every unit the service brings up.
func (pr *project) unitsFor(verb string, args []string) ([]string, error) {
	var services []*Service
	if len(args) == 0 {
		services = pr.p.Services
	}
	for _, a := range args {
		s, err := pr.p.Lookup(a)
		if err != nil {
			return nil, err
		}
		services = append(services, s)
	}
	var units []string
	for _, s := range services {
		if verb == "restart" {
			units = append(units, pr.p.UnitOf(s))
		} else {
			units = append(units, pr.p.UnitsOf(s)...)
		}
	}
	return units, nil
}

func (pr *project) render() error {
	if pr.rendered != nil {
		return nil
	}
	opt, err := DefaultRenderOptions()
	if err != nil {
		return err
	}
	pr.opt = opt
	pr.rendered, err = Render(pr.p, opt)
	return err
}

func (pr *project) config() error {
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	data, err := os.ReadFile(pr.p.ConfigPath)
	if err != nil {
		return err
	}
	fmt.Printf("### %s\n%s\n", pr.p.ConfigPath, data)
	for _, u := range pr.rendered {
		fmt.Printf("### %s\n%s\n", u.Name, u.Text)
	}
	return nil
}

func (pr *project) ps() error {
	pr.scopeLine()
	return pr.table()
}

func (pr *project) table() error {
	names := pr.p.UnitNames()
	states, err := pr.m.States(names)
	if err != nil {
		return err
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "UNIT\tLOAD\tACTIVE\tSUB\tREGISTERED")
	for _, n := range names {
		s := states[n]
		r := pr.registrationOf(unitDir, n)
		if r.kind == "project" || r.kind == "foreign" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\tNOT OURS: %s\n", n, s.LoadState, s.ActiveState, s.SubState, r.owner)
			continue
		}
		if !s.Known() {
			fmt.Fprintf(w, "%s\tnot-found\t-\t-\tnot registered (up registers it)\n", n)
			continue
		}
		reg := s.UnitFileState
		switch {
		case r.kind == "none":
			reg = "not registered (up registers it)" // a slice systemd made on demand is loaded anyway
		case reg == "":
			reg = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n, s.LoadState, s.ActiveState, s.SubState, reg)
	}
	return w.Flush()
}

// journalctl flags that take a separate value; a bare word after one of
// these is its value, not a service.
var journalValueFlags = map[string]bool{"-n": true, "-o": true, "-u": true, "-p": true, "-g": true, "-t": true, "-S": true, "-U": true, "-c": true, "-F": true, "-D": true, "-M": true,
	"--lines": true, "--output": true, "--unit": true, "--priority": true, "--grep": true, "--identifier": true, "--since": true, "--until": true, "--cursor": true, "--after-cursor": true, "--cursor-file": true, "--field": true, "--directory": true, "--file": true, "--root": true, "--image": true, "--machine": true, "--namespace": true, "--facility": true, "--output-fields": true}

// journalArgs turns compose-style `logs [flags] [name...]` into journalctl
// arguments; toUnit maps a bare word to a unit, or returns "" to refuse it.
func journalArgs(args []string, toUnit func(string) (string, error)) ([]string, []string, error) {
	var flags, units []string
	format := true
	want := false
	for _, a := range args {
		if want {
			flags = append(flags, a)
			want = false
			continue
		}
		if a == "-o" || a == "--output" || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output=") {
			format = false
		}
		if journalValueFlags[a] {
			want = true
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			continue
		}
		u, err := toUnit(a)
		if err != nil {
			return nil, nil, err
		}
		units = append(units, u)
	}
	if format {
		flags = append([]string{"-o", "short-iso"}, flags...)
	}
	return flags, units, nil
}

func (pr *project) logs(args []string) error {
	flags, units, err := journalArgs(args, func(name string) (string, error) {
		s, err := pr.p.Lookup(name)
		if err != nil {
			return "", err
		}
		return pr.p.ServiceUnit(s), nil // logs of a job are the service's, not the timer's
	})
	if err != nil {
		return err
	}
	if len(units) == 0 {
		units = []string{pr.p.SliceName()}
	}
	for _, u := range units {
		flags = append(flags, "-u", u)
	}
	return pr.m.ExecJournalctl(flags...)
}

// ---- up ----------------------------------------------------------------------

type planRow struct {
	unit    string
	change  string // new | unchanged | changed | changed (no baseline) | changed (not applied); every "changed…" restarts or warns
	actions []string
}

func (pr *project) up(args []string) error {
	force, buildAll, recreateAll, noRecreate := false, false, false, false
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		case "--build":
			buildAll = true
		case "--force-recreate":
			recreateAll = true
		case "--no-recreate":
			noRecreate = true
		default:
			return fmt.Errorf("up acts on the whole project; %q is not a flag here (start SERVICE for one service)", a)
		}
	}
	if recreateAll && noRecreate {
		return fmt.Errorf("--force-recreate and --no-recreate contradict each other; pick one")
	}
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	if linger, err := Linger(); err == nil && !linger {
		fmt.Println("WARNING: lingering is off for this user, so the project stops at your last logout and does not start at boot. Fix: loginctl enable-linger")
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(pr.renderDir, 0o755); err != nil {
		return err
	}
	// A render directory that ignores itself: nothing to add to the project's
	// own .gitignore, nothing generated ever lands in a commit.
	if err := os.WriteFile(filepath.Join(pr.renderDir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}

	// Verify in a staging directory so a rejected render never replaces a
	// live file. Any output refuses: a warning is a typo systemd would ignore.
	staging := filepath.Join(pr.renderDir, ".staging")
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	var stagingPaths []string
	for _, u := range pr.rendered {
		p := filepath.Join(staging, u.Name)
		if err := os.WriteFile(p, []byte(u.Text), 0o644); err != nil {
			return err
		}
		stagingPaths = append(stagingPaths, p)
	}
	out, verr := pr.m.Verify(stagingPaths)
	os.RemoveAll(staging)
	if out != "" || verr != nil {
		msg := strings.ReplaceAll(out, staging+"/", "")
		if msg == "" {
			msg = verr.Error()
		}
		return fmt.Errorf("systemd-analyze verify refused the render:\n%s", msg)
	}

	names := pr.p.UnitNames()
	// Every name must be free or ours before anything is written or linked:
	// a batched link that fails halfway leaves a partial registration, and a
	// hand-written unit under one of our names must never be replaced.
	for _, n := range names {
		if r := pr.registrationOf(unitDir, n); r.kind == "project" || r.kind == "foreign" {
			return fmt.Errorf("%s is already registered by %s; rename this project (name:) or retire that unit first", n, r.owner)
		}
	}
	before, err := pr.m.States(names)
	if err != nil {
		return err
	}

	// The plan.
	var rows []planRow
	var toRestart, warnings []string
	for _, u := range pr.rendered {
		path := filepath.Join(pr.renderDir, u.Name)
		row := planRow{unit: u.Name, change: "unchanged"}
		st := before[u.Name]
		svc := pr.serviceByUnit(u.Name)
		// A listening service's socket is restartable too: try-restart on it
		// rebinds a changed address and, through PartOf=, restarts the service.
		isRestartable := svc != nil && (pr.p.UnitOf(svc) == u.Name || len(svc.Listen) > 0 && pr.p.SocketUnit(svc) == u.Name)
		old, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err) && isRestartable && st.Active():
			// The render file is gone but the unit runs: there is no baseline
			// to compare against, so the safe reading is "changed".
			row.change = "changed (no baseline)"
		case os.IsNotExist(err):
			row.change = "new"
		case err != nil:
			return err
		case string(old) != u.Text:
			row.change = "changed"
		case isRestartable && st.Active() && st.StartedBefore(mtime(path)):
			// Same text, but the run predates the file: an earlier up wrote it
			// and never restarted the unit (a start that failed elsewhere, an
			// interrupt, on_change: start-only). The file is the baseline, so
			// only the start time can tell.
			row.change = "changed (not applied)"
		}
		if !st.Known() || st.UnitFileState == "" {
			row.actions = append(row.actions, "link")
		}
		changed := strings.HasPrefix(row.change, "changed")
		switch {
		case svc != nil && !isRestartable && strings.HasSuffix(u.Name, ".service"):
			row.actions = append(row.actions, "runs on its timer")
		case isRestartable && !st.Active():
			row.actions = append(row.actions, "start")
		case isRestartable && (changed || recreateAll) && svc.OnChange == "start-only":
			// --force-recreate does not outrank it: up never restarts a
			// start-only service, which is what the setting promises.
			row.actions = append(row.actions, "running, NOT restarted (on_change: start-only); when convenient: systemd-compose restart "+svc.Name)
			if changed {
				warnings = append(warnings, u.Name+" changed on disk but was left running (on_change: start-only)")
			} else {
				warnings = append(warnings, u.Name+" was not recreated (on_change: start-only outranks --force-recreate)")
			}
		case isRestartable && changed && noRecreate:
			row.actions = append(row.actions, "running, NOT restarted (--no-recreate)")
			warnings = append(warnings, u.Name+" changed on disk but was left running (--no-recreate)")
		case isRestartable && changed:
			row.actions = append(row.actions, "try-restart")
			toRestart = append(toRestart, u.Name)
		case isRestartable && recreateAll:
			row.actions = append(row.actions, "try-restart (--force-recreate)")
			toRestart = append(toRestart, u.Name)
		}
		rows = append(rows, row)
	}
	orphans, err := pr.orphans(unitDir, names)
	if err != nil {
		return err
	}
	var builds []*Service
	for _, s := range pr.p.Services {
		if s.Build == nil {
			continue
		}
		creates := s.Build.Creates
		if creates != "" && !filepath.IsAbs(creates) {
			creates = filepath.Join(s.WorkingDir, creates)
		}
		if buildAll || creates == "" || !exists(creates) {
			if s.Build.Creates == "" && !buildAll {
				fmt.Printf("  build %s: skipped, no creates: (run `systemd-compose build %s` or `up --build`)\n", s.Name, s.Name)
				continue
			}
			builds = append(builds, s)
		}
	}
	for _, r := range rows {
		act := "-"
		if len(r.actions) > 0 {
			act = strings.Join(r.actions, ", ")
		}
		fmt.Printf("  %-32s %-22s %s\n", r.unit, r.change, act)
	}
	var shed, gone []orphan
	for _, o := range orphans {
		state, why := "inactive", "not in the yaml"
		if o.active {
			state = "ACTIVE"
		}
		if o.shedBy != "" {
			why = o.shedBy + " no longer declares it"
			shed = append(shed, o)
		} else {
			gone = append(gone, o)
		}
		fmt.Printf("  %-32s orphan     %s, %s: will be disabled and removed\n", o.unit, state, why)
		if o.active && !force && o.shedBy == "" {
			return fmt.Errorf("orphan %s is active; stop it first (systemd-compose stop is by service name; use systemctl --user stop %s) or pass --force", o.unit, o.unit)
		}
	}
	for _, s := range builds {
		fmt.Printf("  build %s: %d step(s)\n", s.Name, len(s.Build.Run))
	}

	// Act, in order: build, reset-failed, write, link, enable the target,
	// start everything, restart what changed, retire orphans.
	for _, s := range builds {
		if err := pr.runBuild(s); err != nil {
			return fmt.Errorf("build %s: %w", s.Name, err)
		}
	}
	var known []string
	for _, n := range names {
		if before[n].Known() {
			known = append(known, n)
		}
	}
	if err := pr.m.ResetFailed(known); err != nil {
		return err
	}
	var paths []string
	for _, u := range pr.rendered {
		path := filepath.Join(pr.renderDir, u.Name)
		if err := writeUnit(path, u.Text); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if err := pr.m.Link(paths); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if err := pr.m.EnableNow(pr.p.TargetName()); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	// A socket or timer a service shed goes before that service restarts:
	// a socket still listening hands its fd to the new run, so the dropped
	// listen: would never take effect.
	if len(shed) > 0 {
		if err := pr.retire(shed); err != nil {
			return err
		}
	}
	var startable []string
	for _, s := range pr.p.Services {
		startable = append(startable, pr.p.UnitsOf(s)...)
	}
	// Both, whatever the first says: a unit that fails to start must not
	// leave the changed ones running their old definition.
	var errs []error
	if err := pr.m.Start(startable); err != nil {
		errs = append(errs, fmt.Errorf("start: %w", err))
	}
	if len(toRestart) > 0 {
		if err := pr.m.TryRestart(toRestart); err != nil {
			errs = append(errs, fmt.Errorf("try-restart: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if len(gone) > 0 {
		if err := pr.retire(gone); err != nil {
			return err
		}
	}
	sweepEmptyWants(unitDir)
	for _, w := range warnings {
		fmt.Println("WARNING: " + w)
	}
	fmt.Println()
	return pr.table()
}

func (pr *project) serviceByUnit(unit string) *Service {
	for _, s := range pr.p.Services {
		if pr.p.ServiceUnit(s) == unit || pr.p.TimerUnit(s) == unit || pr.p.SocketUnit(s) == unit {
			return s
		}
	}
	return nil
}

// registration says who owns the search-path entry for a unit name.
type registration struct {
	kind  string // "none" | "ours" | "project" | "foreign"
	owner string // for "project": the other yaml; for "foreign": what sits there
}

// registrationOf classifies <unitDir>/<name>: a symlink to this project's
// render file is ours; a symlink into another .systemd-compose directory is
// another project's, named by its marker; anything else is foreign, most
// often a hand-written unit. Only "ours" may ever be acted on.
func (pr *project) registrationOf(unitDir, name string) registration {
	p := filepath.Join(unitDir, name)
	st, err := os.Lstat(p)
	if err != nil {
		return registration{kind: "none"}
	}
	if st.Mode()&os.ModeSymlink == 0 {
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
		if data, err := os.ReadFile(target); err == nil {
			if cfg := markerValue(string(data), "Config"); cfg != "" {
				owner = "project " + markerValue(string(data), "Project") + " from " + cfg
			}
		}
		return registration{kind: "project", owner: owner}
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
			others[n] = "not registered (up registers it)"
		default:
			others[n] = "NOT OURS: " + r.owner
		}
	}
	return ours, others, nil
}

func markerValue(text, key string) string {
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "["+MarkerSection+"]"
			continue
		}
		if in && strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

type orphan struct {
	unit   string
	active bool
	shedBy string // a service still in the yaml that no longer has this socket or timer
}

// unregister takes units off the manager: one disable, whose reload also
// clears the "changed on disk" flag a yaml edit raises through SourcePath=,
// then a stop of only the units that were running (timers before the jobs
// they trigger), then clear any failed state. Not disable --now: its stop
// comes after the reload, which has already unloaded an idle unit. Not a
// stop first: it would warn that the unit changed on disk.
func (pr *project) unregister(units []string) error {
	states, err := pr.m.States(units)
	if err != nil {
		return err
	}
	if err := pr.m.run(append([]string{"disable"}, units...)...); err != nil {
		return fmt.Errorf("disable: %w", err)
	}
	var running []string
	for _, timers := range []bool{true, false} {
		for _, u := range units {
			st := states[u].ActiveState
			if strings.HasSuffix(u, ".timer") == timers && st != "" && st != "inactive" && st != "failed" {
				running = append(running, u)
			}
		}
	}
	if len(running) > 0 {
		if err := pr.m.run(append([]string{"stop"}, running...)...); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	return pr.m.ResetFailed(units)
}

// retire unregisters orphans and deletes their render files: nothing in the
// yaml describes them any more.
func (pr *project) retire(orphans []orphan) error {
	var units []string
	for _, o := range orphans {
		units = append(units, o.unit)
	}
	if err := pr.unregister(units); err != nil {
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
// the yaml no longer declares. Provenance is the link target's directory
// plus the marker section; a unit that merely shares the name prefix is
// somebody else's and is never touched. One whose marker names a service
// still in the yaml is a socket or timer that service shed (shedBy).
func (pr *project) orphans(unitDir string, names []string) ([]orphan, error) {
	declared := map[string]bool{}
	for _, n := range names {
		declared[n] = true
	}
	entries, err := os.ReadDir(unitDir)
	if os.IsNotExist(err) {
		return nil, nil // nothing was ever registered on this instance
	}
	if err != nil {
		return nil, fmt.Errorf("orphans: %w", err)
	}
	var found []string
	shedBy := map[string]string{}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 || declared[e.Name()] {
			continue
		}
		target, err := os.Readlink(filepath.Join(unitDir, e.Name()))
		if err != nil || filepath.Dir(target) != pr.renderDir || filepath.Base(target) != e.Name() {
			continue
		}
		// Ownership must be READ, never assumed: several projects may render
		// into this directory, and a file that cannot be read is nobody's.
		data, err := os.ReadFile(target)
		if err != nil || markerValue(string(data), "Project") != pr.p.Name {
			if err != nil {
				fmt.Printf("  %-32s registered from here but unreadable (%v); not touched\n", e.Name(), err)
			}
			continue
		}
		found = append(found, e.Name())
		if s := pr.p.Service(markerValue(string(data), "Service")); s != nil {
			shedBy[e.Name()] = s.Name
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
		out = append(out, orphan{n, states[n].Active(), shedBy[n]})
	}
	return out, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mtime is when p was last written; zero when it cannot be read. writeUnit
// rewrites only on change, so for a render file it is the definition's birth.
func mtime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// ---- down --------------------------------------------------------------------

func (pr *project) down(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("down acts on the whole project (stop SERVICE stops one service)")
	}
	pr.scopeLine()
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	names := pr.p.UnitNames()
	registered, others, err := pr.ownedUnits(names)
	if err != nil {
		return err
	}
	for _, n := range names {
		if why, skip := others[n]; skip {
			fmt.Printf("  %-32s %s\n", n, why)
		}
	}
	orphans, err := pr.orphans(unitDir, names)
	if err != nil {
		return err
	}
	if len(registered) > 0 {
		if err := pr.unregister(registered); err != nil {
			return err
		}
	}
	// What an older yaml registered from here goes too, active or not: down
	// is the verb that stops things, and a project that is never upped again
	// would otherwise keep those links forever.
	if len(orphans) > 0 {
		if err := pr.retire(orphans); err != nil {
			return err
		}
	}
	sweepEmptyWants(unitDir)
	fmt.Printf("down: %d units disabled; their rendered files stay in %s\n", len(registered), pr.renderDir)
	return nil
}

// ---- build -------------------------------------------------------------------

func (pr *project) build(args []string) error {
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	var todo []*Service
	if len(args) == 0 {
		for _, s := range pr.p.Services {
			if s.Build != nil {
				todo = append(todo, s)
			}
		}
		if len(todo) == 0 {
			return fmt.Errorf("no service in project %s declares build:", pr.p.Name)
		}
	}
	for _, a := range args {
		s, err := pr.p.Lookup(a)
		if err != nil {
			return err
		}
		if s.Build == nil {
			return fmt.Errorf("service %s declares no build:", a)
		}
		todo = append(todo, s)
	}
	for _, s := range todo {
		if err := pr.runBuild(s); err != nil {
			return fmt.Errorf("build %s: %w", s.Name, err)
		}
	}
	return nil
}

// runBuild runs a service's build steps through transient units with the
// service's own environment, so a build that passes proves the environment
// the service will run in. Output streams to the terminal.
func (pr *project) runBuild(s *Service) error {
	if err := pr.render(); err != nil {
		return err
	}
	for i, step := range s.Build.Run {
		cmd, err := resolveCommand(step, s.WorkingDir, pr.opt.SearchPath)
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		fmt.Printf("build %s [%d/%d]: %s\n", s.Name, i+1, len(s.Build.Run), cmd)
		args := []string{pr.m.scope(), "--wait", "--pipe", "--collect", "--quiet",
			"--description=" + pr.p.Name + ": build " + s.Name,
			"-p", "WorkingDirectory=" + s.WorkingDir, "-p", "Slice=" + pr.p.SliceName()}
		for _, kv := range s.Environment {
			args = append(args, "-p", "Environment="+envAssignment(kv))
		}
		for _, ef := range s.EnvFiles {
			p := ef.Path
			if !ef.Required {
				p = "-" + p
			}
			args = append(args, "-p", "EnvironmentFile="+p)
		}
		args = append(args, "--")
		args = append(args, splitWords(cmd)...)
		c := exec.Command("systemd-run", args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("step %d failed: %v", i+1, asExit(err))
		}
	}
	return nil
}

// ---- probe (hidden; the healthcheck's ExecStartPost) --------------------------

func probe(args []string) error {
	interval, timeout, startPeriod := "2s", "5s", "60s"
	var argv []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--interval", "--timeout", "--start-period":
			if i+1 >= len(args) {
				return fmt.Errorf("probe: %s needs a value", args[i])
			}
			switch args[i] {
			case "--interval":
				interval = args[i+1]
			case "--timeout":
				timeout = args[i+1]
			default:
				startPeriod = args[i+1]
			}
			i++
		case "--":
			argv = args[i+1:]
			i = len(args)
		default:
			return fmt.Errorf("probe: unexpected %q", args[i])
		}
	}
	if len(argv) == 0 {
		return fmt.Errorf("probe: no test command after --")
	}
	iv, err := seconds(interval)
	if err != nil {
		return err
	}
	to, err := seconds(timeout)
	if err != nil {
		return err
	}
	sp, err := seconds(startPeriod)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(sp) * time.Second)
	attempt := 0
	for {
		attempt++
		c := exec.Command(argv[0], argv[1:]...)
		done := make(chan error, 1)
		if err := c.Start(); err != nil {
			return fmt.Errorf("probe: %v", err)
		}
		go func() { done <- c.Wait() }()
		var perr error
		select {
		case perr = <-done:
		case <-time.After(time.Duration(to) * time.Second):
			c.Process.Kill()
			<-done // reap it; a killed probe must not linger as a zombie
			perr = fmt.Errorf("timed out after %s", timeout)
		}
		if perr == nil {
			fmt.Printf("probe: healthy after %d attempt(s)\n", attempt)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("probe: not healthy within %s (%d attempts, last: %v)", startPeriod, attempt, perr)
		}
		time.Sleep(time.Duration(iv) * time.Second)
	}
}

// ---- instance mode (no yaml): the verb map over one systemd instance --------

func instanceMode(m *Manager, verb string, args []string) error {
	needUnit := func() error {
		if len(args) == 0 {
			return fmt.Errorf("%s needs a unit or target (no project here: no systemd-compose.yaml in this directory or above)", verb)
		}
		return nil
	}
	switch verb {
	case "ps":
		for i, a := range args {
			if a == "-a" {
				args[i] = "--all"
			}
		}
		return m.ExecSystemctl(append([]string{"list-units", "--type=service,timer"}, args...)...)
	case "logs":
		flags, units, err := journalArgs(args, func(w string) (string, error) { return w, nil })
		if err != nil {
			return err
		}
		for _, u := range units {
			flags = append(flags, "-u", u)
		}
		return m.ExecJournalctl(flags...)
	case "up":
		var units []string
		for _, a := range args {
			if a != "-d" && a != "--detach" {
				units = append(units, a)
			}
		}
		args = units
		if err := needUnit(); err != nil {
			return err
		}
		return m.ExecSystemctl(append([]string{"enable", "--now"}, args...)...)
	case "down":
		if err := needUnit(); err != nil {
			return err
		}
		return m.ExecSystemctl(append([]string{"disable", "--now"}, args...)...)
	case "start", "stop", "restart", "config":
		if err := needUnit(); err != nil {
			return err
		}
		if verb == "config" {
			verb = "cat"
		}
		return m.ExecSystemctl(append([]string{verb}, args...)...)
	case "top":
		if m.User {
			return Exec("systemd-cgtop", append(append([]string{"--depth=3"}, args...), m.CGroupPath(""))...)
		}
		return Exec("systemd-cgtop", append(append([]string{"--depth=2"}, args...), "system.slice")...)
	default:
		return m.ExecSystemctl(append([]string{verb}, args...)...)
	}
}
