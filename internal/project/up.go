package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// undelegated refuses a limit that renders fine and does nothing: a CPU one
// where the user manager has no cpu controller (systemd 249 delegates it
// memory and pids alone), an AllowedCPUs= where it has no cpuset. The io
// controller is refused at load, as no user manager has it. have is nil
// when it cannot be read.
func undelegated(p *config.Project, have []string) error {
	if have == nil {
		return nil
	}
	controller := func(key string) string {
		switch key {
		case "CPUWeight", "StartupCPUWeight", "CPUQuota", "CPUQuotaPeriodSec", "CPUShares", "StartupCPUShares":
			return "cpu"
		case "AllowedCPUs", "StartupAllowedCPUs", "AllowedMemoryNodes", "StartupAllowedMemoryNodes":
			return "cpuset"
		}
		return ""
	}
	refuse := func(what, c string) error {
		if c == "" || slices.Contains(have, c) {
			return nil
		}
		return fmt.Errorf("%s: the %s cgroup controller is not delegated to your user manager (it has %s), so this would render fine and do nothing; drop it, or have root delegate it with a drop-in for user@.service: [Service] Delegate=cpu cpuset io memory pids", what, c, strings.Join(have, " "))
	}
	if r := p.Resources; r != nil && r.CPUs > 0 {
		if err := refuse("resources: cpus", "cpu"); err != nil {
			return err
		}
	}
	for _, s := range p.EnabledServices() {
		if r := s.Resources; r != nil && r.CPUs > 0 {
			if err := refuse("service "+s.Name+": resources: cpus", "cpu"); err != nil {
				return err
			}
		}
		for _, sec := range s.Unit.Sections {
			for _, k := range sec.Keys {
				if err := refuse(fmt.Sprintf("service %s: line %d: unit: %s: %s", s.Name, k.Line, sec.Name, k.Name), controller(k.Name)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// verifyWhere says where the lines of verify's refusal point: the rendered
// units, or (a line that names a full path) a file systemd reads beside
// them, such as a drop-in of the host's own in ~/.config/systemd/user/
// service.d, which has line numbers of its own and is changed there.
func verifyWhere(msg string) string {
	if strings.HasPrefix(msg, "/") || strings.Contains(msg, "\n/") {
		return "a line that names a full path is in a file systemd reads beside the render, such as a drop-in, to change there; the others are the rendered units', which systemd-compose config prints"
	}
	return "its line numbers are the rendered units', which systemd-compose config prints"
}

// planRow is one line of up's plan: a unit, how its render compares with
// what is registered, and what up does about it.
type planRow struct {
	unit    string
	change  string // changeOf's label; every "changed…" restarts or warns
	actions []string
}

// upOptions are up's flags.
type upOptions struct {
	force, buildAll, recreateAll, noRecreate, dryRun bool
}

// upPlan is what up has decided before it acts: a row per unit, and the
// units each of its steps takes.
type upPlan struct {
	upOptions
	unitDir string
	names   []string                     // the units this render brings up
	before  map[string]systemd.UnitState // their states before up
	rows    []planRow

	builds, skipped, done []*config.Service // builds to run, without creates:, and made already

	toRestart, bounced, toStop []string
	spared                     []string        // left running by design: on_change: start-only, and what would take one along
	envRead                    map[string]bool // units whose new env file text their run has read
	warnings                   []string

	orphans, shed, gone []orphan
	refused             string   // the active orphan that refuses up; "" when none does
	idle                []string // registered, of a profile not active
	leftover            []string
}

// up renders the project, prints the plan, and carries it out: compose's up.
func (pr *project) up(args []string) error {
	var o upOptions
	for _, a := range args {
		switch a {
		case "--dry-run":
			o.dryRun = true
		case "--force":
			o.force = true
		case "--build":
			o.buildAll = true
		case "--force-recreate":
			o.recreateAll = true
		case "--no-recreate":
			o.noRecreate = true
		}
		if v, ok := strings.CutPrefix(a, "--wait-timeout="); ok {
			n, _ := config.Seconds(v) // CheckArgs checked it
			pr.waitFor = time.Duration(n) * time.Second
		} // CheckArgs has refused anything else
	}
	if o.recreateAll && o.noRecreate {
		return fmt.Errorf("--force-recreate and --no-recreate contradict each other; pick one")
	}
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	if err := pr.upChecks(o.dryRun); err != nil {
		return err
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	pr.upNotes(unitDir)
	pl := &upPlan{upOptions: o, unitDir: unitDir, names: pr.p.UnitNames()}
	if pl.before, err = pr.claimNames(unitDir, pl.names); err != nil {
		return err
	}
	if err := pr.verify(); err != nil {
		return err
	}
	if err := pr.plan(pl); err != nil {
		return err
	}
	pr.printPlan(pl)
	if pl.refused != "" {
		return fmt.Errorf("orphan %s is active; stop it first (systemd-compose stop is by service name; use systemctl --user stop %s) or pass --force", pl.refused, pl.refused)
	}
	if o.dryRun {
		fmt.Println("dry run: nothing built, written, registered, started, retired or removed")
		return nil
	}
	if err := pr.prepareRenderDir(unitDir); err != nil {
		return err
	}
	return pr.apply(pl)
}

// upChecks refuses what up cannot do, and says what it notices, before
// the plan.
func (pr *project) upChecks(dryRun bool) error {
	// Before the plan reads it, so another user's directory is named as
	// such, and a dry run refuses what up would.
	if err := systemd.CheckPrivateDir(pr.renderDir); err != nil {
		return err
	}
	if err := undelegated(pr.p, pr.m.Controllers()); err != nil {
		return err
	}
	if !dryRun && slices.ContainsFunc(pr.p.EnabledServices(), func(s *config.Service) bool { return s.Healthcheck != nil }) {
		if err := ensureProbe(pr.opt); err != nil {
			return fmt.Errorf("the healthcheck probe's copy %s: %w", pr.opt.Exe, err)
		}
	}
	for _, n := range pr.p.Notes {
		fmt.Println("note: " + n)
	}
	// set-property's drop-ins win over the render, resources: and all, and
	// outlast down, so a yaml edit to a limit would do nothing unannounced.
	if ctl, err := pr.m.ControlDropIns(pr.p.DeclaredNames()); err == nil {
		for _, u := range pr.p.DeclaredNames() {
			if files := ctl[u]; len(files) > 0 {
				fmt.Printf("note: %s has settings from systemctl set-property, which win over the yaml (resources: and the rest) and outlast down: %s; delete it and run up to drop them\n", u, strings.Join(files, ", "))
			}
		}
	}
	if linger, err := systemd.Linger(); err != nil || !linger {
		fmt.Println("WARNING: lingering is off for this user (or loginctl cannot tell), so the project stops at your last logout and does not start at boot. Fix: loginctl enable-linger")
	}
	return nil
}

// upNotes says what up finds of an earlier up that went missing: rendered
// files gone, or the directory moved without a down.
func (pr *project) upNotes(unitDir string) {
	missing := 0
	for _, n := range pr.p.UnitNames() {
		if pr.registrationOf(unitDir, n).kind == "ours" && !exists(filepath.Join(pr.renderDir, n)) {
			missing++
		}
	}
	if missing > 0 {
		fmt.Printf("note: %s (a git clean?): up writes them again, and with nothing to compare against, what runs reads changed (no baseline)\n", goneFiles(missing))
	}
	if kind := systemd.LateFS(pr.p.Dir, unitDir); kind != "" && pr.p.Registration != "copy" {
		fmt.Printf("WARNING: this project is on %s, and its units are links into it: when the user manager starts before %s is mounted (at boot), they are missing, and the project does not start, then or once it is mounted. registration: copy in the yaml registers copies instead; README, \"A project on a network filesystem\"\n", kind, kind)
	}
	if cfg, name := pr.movedFrom(unitDir); cfg != "" && name != pr.p.Name {
		fmt.Printf("WARNING: this directory was moved from %s without a down, and project %s is still registered from there: this up starts a second copy, as project %s. README, \"Moving or deleting a project\", moves one\n", filepath.Dir(cfg), name, pr.p.Name)
	}
}

// claimNames checks that every name is free or ours before anything is
// written or linked, and returns the units' states. A batched link that
// failed halfway would leave a partial registration, and a hand-written
// unit under one of our names must never be replaced.
func (pr *project) claimNames(unitDir string, names []string) (map[string]systemd.UnitState, error) {
	for _, n := range names {
		if r := pr.registrationOf(unitDir, n); r.kind == "project" || r.kind == "foreign" {
			if r.gone {
				return nil, fmt.Errorf("%s is already registered by %s. If that is this project before a move, move it back and run down there (README, \"Moving or deleting a project\", has the way by hand); or run this copy under another name: %s up", n, r.owner, pr.cmdAs("NAME"))
			}
			if r.kind == "project" {
				return nil, fmt.Errorf("%s is already registered by %s. Run this copy under another name (%s up, or SYSTEMD_COMPOSE_PROJECT_NAME=NAME in a .env beside the yaml), or take that one down in its own directory", n, r.owner, pr.cmdAs("NAME"))
			}
			if r.masked {
				return nil, fmt.Errorf("%s is masked: unmask it (%s unmask %s, or systemctl --user unmask %s), then up", n, pr.cmd(), n, n)
			}
			return nil, fmt.Errorf("%s is already %s; give this project another name (name:, -p NAME, or the .env) or retire that unit", n, r.owner)
		}
	}
	before, err := pr.m.States(names)
	if err != nil {
		return nil, err
	}
	// A name free in the user's unit directory can still be a unit systemd
	// loads from elsewhere (app.slice and default.target from /usr/lib, one
	// in /etc/systemd/user): a link would shadow it, the project's limits
	// and all, and down would stop it. Checked before verify, which reads
	// a project named after a target that basic.target orders itself after
	// (sockets, timers) as an ordering cycle.
	for _, n := range names {
		if f := before[n].FragmentPath; pr.registrationOf(unitDir, n).kind == "none" && f != "" && f != filepath.Join(unitDir, n) && exists(f) {
			if f == "/dev/null" { // otherwise "a unit of its own, loaded from /dev/null"
				return nil, fmt.Errorf("%s is masked outside your unit directory (in /etc/systemd/user or /run/systemd/user, by root or at runtime); give this project another name (name:, -p NAME, or the .env)", n)
			}
			return nil, fmt.Errorf("%s is already a unit of its own, loaded from %s; give this project another name (name:, -p NAME, or the .env)", n, f)
		}
	}
	return before, nil
}

// verify runs systemd-analyze verify on the render, in a staging directory
// so a rejected render never replaces a live file, and outside the project
// so a dry run writes nothing there. Any output refuses: a warning is a
// typo systemd would ignore.
func (pr *project) verify() error {
	scratch, err := scratchDir()
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(scratch, scratchPrefix(verifyScratch))
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	var stagingPaths []string
	for _, u := range pr.rendered {
		p := filepath.Join(staging, u.Name)
		if err := os.WriteFile(p, []byte(u.Text), 0o644); err != nil {
			return err
		}
		stagingPaths = append(stagingPaths, p)
	}
	out, verr := pr.m.Verify(stagingPaths)
	out, verr = pr.pendingPrograms(out, verr)
	var deprecated []string
	out, deprecated, verr = othersDeprecations(out, verr, staging)
	for _, l := range deprecated {
		fmt.Println("note: a file beside the render uses a setting systemd deprecates, but still honours: " + l)
	}
	if out != "" || verr != nil {
		// a drop-in for every service is read once per unit, and its line
		// is said once
		msg := uniqLines(strings.ReplaceAll(out, staging+"/", ""))
		if msg == "" {
			msg = verr.Error()
		}
		return fmt.Errorf("systemd-analyze verify refused the render (%s):\n%s", verifyWhere(msg), msg)
	}
	return nil
}

// plan fills in the plan: the builds, a row per unit, what restarts
// along, the orphans and leftovers.
func (pr *project) plan(pl *upPlan) error {
	for _, s := range pr.p.EnabledServices() {
		if s.Build == nil {
			continue
		}
		switch creates := s.Build.Creates; {
		case pl.buildAll || creates != "" && !exists(creates):
			pl.builds = append(pl.builds, s)
		case creates == "":
			pl.skipped = append(pl.skipped, s)
		default:
			pl.done = append(pl.done, s)
		}
	}
	pl.envRead = map[string]bool{}
	for _, u := range pr.rendered {
		row, err := pr.planUnit(pl, u.Name, u.Text)
		if err != nil {
			return err
		}
		pl.rows = append(pl.rows, row)
	}
	var held, warned []string
	pl.toRestart, pl.bounced, held, warned = pr.alongside(pl.rows, pl.toRestart, pl.before)
	pl.spared = append(pl.spared, held...)
	pl.warnings = append(pl.warnings, warned...)
	var err error
	if pl.orphans, err = pr.orphans(pl.unitDir); err != nil {
		return err
	}
	// Under a name not registered yet, this up is the one that would start
	// a second copy of a project registered under another.
	lead := "WARNING: "
	for _, n := range pl.names {
		if pr.registrationOf(pl.unitDir, n).kind == "ours" {
			lead = "  note: "
			break
		}
	}
	if err := pr.otherNames(pl.unitDir, lead); err != nil {
		return err
	}
	// A service of an inactive profile is neither rendered nor touched: one
	// an earlier up registered keeps running, outside the boot set.
	enabled := map[string]bool{}
	for _, n := range pl.names {
		enabled[n] = true
	}
	for _, n := range pr.p.DeclaredNames() {
		if !enabled[n] && pr.registrationOf(pl.unitDir, n).kind == "ours" {
			pl.idle = append(pl.idle, n)
		}
	}
	// An active orphan up may not retire refuses the whole up.
	for _, o := range pl.orphans {
		if pl.blocks(o) && pl.refused == "" {
			pl.refused = o.unit
		}
		if o.shedBy != "" {
			pl.shed = append(pl.shed, o)
		} else {
			pl.gone = append(pl.gone, o)
		}
	}
	pl.leftover = pr.leftovers(pl.unitDir)
	return nil
}

// blocks is an active orphan up may not retire.
func (pl *upPlan) blocks(o orphan) bool { return o.refused() && !pl.force }

// planUnit compares one rendered unit with the file up wrote last time
// and with what runs, and decides what up does about it.
func (pr *project) planUnit(pl *upPlan, name, text string) (planRow, error) {
	st := pl.before[name]
	svc := pr.serviceByUnit(name)
	change, err := pr.changeOf(name, text, st)
	if err != nil {
		return planRow{}, err
	}
	row := planRow{unit: name, change: change}
	if change == changeEnvRead {
		pl.envRead[name] = true
	}
	isRestartable := pr.restartable(svc, name)
	switch r, copies := pr.registrationOf(pl.unitDir, name), pr.p.Registration == "copy"; {
	case r.kind == "ours" && r.copied != copies && copies:
		row.actions = append(row.actions, "copy, in place of its link")
	case r.kind == "ours" && r.copied != copies:
		row.actions = append(row.actions, "link, in place of its copy")
	case copies && r.kind != "ours":
		row.actions = append(row.actions, "copy")
	case !copies && (!st.Known() || st.UnitFileState == ""):
		row.actions = append(row.actions, "link")
	}
	changed := strings.HasPrefix(row.change, changeText)
	// A build that runs makes what the service runs new, as compose
	// recreates a container it rebuilt; a job's next run takes it.
	rebuilt := svc != nil && slices.Contains(pl.builds, svc) && svc.Schedule == nil && pr.p.UnitOf(svc) == name
	switch {
	case changed && name == pr.p.SliceName():
		if row.change == changeNoBaseline {
			row.actions = append(row.actions, "applied in place by the reload, nothing restarts")
		} else {
			row.actions = append(row.actions, "applied in place by the reload (new limits), nothing restarts")
		}
	case changed && name == pr.p.TargetName():
		row.actions = append(row.actions, "applied by the reload (the boot set)")
	case svc != nil && !isRestartable && strings.HasSuffix(name, ".service") && st.ActiveState == "active":
		// A job's own unit runs only while its timer fires it (a run is
		// "activating"). Still active from a definition without
		// schedule:, it would keep the timer from ever starting it.
		row.actions = append(row.actions, "stop: it runs only when its timer fires")
		pl.toStop = append(pl.toStop, name)
	case svc != nil && !isRestartable && strings.HasSuffix(name, ".service"):
		row.actions = append(row.actions, "runs on its timer")
	case isRestartable && !st.Active():
		row.actions = append(row.actions, "start")
	case isRestartable && (changed || pl.recreateAll || rebuilt) && svc.OnChange == "start-only":
		// --force-recreate does not outrank it: up never restarts a
		// start-only service, which is what the setting promises.
		row.actions = append(row.actions, "running, NOT restarted (on_change: start-only); when convenient: "+pr.applyCmd(name))
		pl.spared = append(pl.spared, name)
		switch {
		case row.change == changeNoBaseline:
			pl.warnings = append(pl.warnings, name+" was left running (on_change: start-only); its rendered file was gone, so up takes the file it wrote as what it runs")
		case changed:
			pl.warnings = append(pl.warnings, name+" changed on disk but was left running (on_change: start-only)")
		case rebuilt:
			pl.warnings = append(pl.warnings, name+" was rebuilt but left running its previous build (on_change: start-only)")
		default:
			pl.warnings = append(pl.warnings, name+" was not recreated (on_change: start-only outranks --force-recreate)")
		}
	case isRestartable && (changed || rebuilt) && pl.noRecreate:
		row.actions = append(row.actions, "running, NOT restarted (--no-recreate)")
		switch {
		case row.change == changeNoBaseline:
			pl.warnings = append(pl.warnings, name+" was left running (--no-recreate); its rendered file was gone, so up cannot tell what it runs, and the next up restarts it")
		case changed:
			pl.warnings = append(pl.warnings, name+" changed on disk but was left running (--no-recreate)")
		default:
			pl.warnings = append(pl.warnings, name+" was rebuilt but left running its previous build (--no-recreate)")
		}
	case isRestartable && changed:
		row.actions = append(row.actions, "try-restart")
		pl.toRestart = append(pl.toRestart, name)
	case isRestartable && rebuilt:
		row.actions = append(row.actions, "try-restart (rebuilt)")
		pl.toRestart = append(pl.toRestart, name)
	case isRestartable && pl.recreateAll:
		row.actions = append(row.actions, "try-restart (--force-recreate)")
		pl.toRestart = append(pl.toRestart, name)
	}
	return row, nil
}

// changeOf's labels, as up's plan prints them.
const (
	changeNew        = "new"
	changeNone       = "unchanged"
	changeEnvRead    = "unchanged (env_file read)"
	changeText       = "changed"
	changeEnv        = "changed (env_file)"
	changeNotApplied = "changed (not applied)"
	changeNoBaseline = "changed (no baseline)"
	changeFileGone   = "file gone, written again"
)

// changeOf compares a rendered unit with the file up wrote last time, and
// with what runs, as one of the labels above. up's plan and ps's note read
// it alike.
func (pr *project) changeOf(name, text string, st systemd.UnitState) (string, error) {
	svc := pr.serviceByUnit(name)
	old, err := os.ReadFile(filepath.Join(pr.renderDir, name))
	switch {
	case os.IsNotExist(err) && st.Active():
		// The render file is gone but the unit runs: there is no baseline
		// to compare against, so the safe reading is "changed".
		return changeNoBaseline, nil
	case os.IsNotExist(err) && st.Known() && st.UnitFileState != "":
		// registered, its file gone (a git clean): not new, and not
		// running, so nothing to restart
		return changeFileGone, nil
	case os.IsNotExist(err):
		return changeNew, nil
	case err != nil:
		return "", err
	case string(old) != text && onlyTriggers(string(old), text) && svc != nil && pr.p.UnitOf(svc) == name && readEnvFilesSince(st, svc):
		// The program started after its env files were last written (a
		// restart by hand after the edit): it has read them, and a
		// restart, with its dependents', would be for nothing.
		return changeEnvRead, nil
	case string(old) != text && onlyTriggers(string(old), text):
		return changeEnv, nil
	case string(old) != text:
		return changeText, nil
	case pr.notApplied(svc, name, st):
		// The file is the baseline, so only the start time can tell.
		return changeNotApplied, nil
	}
	return changeNone, nil
}

// notApplied is a unit up restarts whose file says what it runs, but whose
// run predates the file: an earlier up wrote it and never restarted the
// unit (a start that failed elsewhere, an interrupt, on_change:
// start-only).
func (pr *project) notApplied(svc *config.Service, name string, st systemd.UnitState) bool {
	return pr.restartable(svc, name) && st.Active() && st.StartedBefore(mtime(filepath.Join(pr.renderDir, name)))
}

// restartable is a unit up restarts when it changes: the unit that stands
// for a service, or a listening service's socket, since try-restart on it
// rebinds a changed address and, through the service's
// Requires=<svc>.socket, takes the service along.
func (pr *project) restartable(svc *config.Service, name string) bool {
	return svc != nil && (pr.p.UnitOf(svc) == name || len(svc.Listen) > 0 && pr.p.SocketUnit(svc) == name)
}

// printPlan prints the plan, every row before any refusal. Each row naming
// a unit starts with two spaces and the unit ("  <unit> <label> <actions>";
// "profile not active" for an idle one; an orphan's is "  <unit> orphan
// ACTIVE|inactive, <why>: <fate>"), each build row with "  build <service>: "
// and "will run, N steps", "skipped" or "up to date". The e2e checks marked
// "row grammar" read them.
func (pr *project) printPlan(pl *upPlan) {
	for _, r := range pl.rows {
		act := "-"
		if len(r.actions) > 0 {
			act = strings.Join(r.actions, ", ")
		}
		fmt.Printf("  %-32s %-25s %s\n", r.unit, r.change, act)
	}
	for _, n := range pl.idle {
		fmt.Printf("  %-32s %-25s %s\n", n, "profile not active", "left as is, out of the boot set")
	}
	for _, s := range pl.builds {
		fmt.Printf("  build %s: will run, %s\n", s.Name, plural(len(s.Build.Run), "step"))
	}
	for _, s := range pl.skipped {
		fmt.Printf("  build %s: skipped, no creates: (run `%s build %s` or `up --build`)\n", s.Name, pr.cmd(), s.Name)
	}
	for _, s := range pl.done {
		fmt.Printf("  build %s: up to date, %s exists (`up --build` reruns it)\n", s.Name, s.Build.Creates)
	}
	for _, o := range pl.orphans {
		state, why, fate := "inactive", "not in the yaml", "will be disabled and removed"
		if o.active {
			state = "ACTIVE"
		}
		if o.shedBy != "" {
			why = o.shedBy + " no longer declares it"
		}
		switch {
		case pl.blocks(o):
			fate = "refused without --force"
		case pl.refused != "":
			fate = "left as is: up is refused"
		}
		fmt.Printf("  %-32s orphan     %s, %s: %s\n", o.unit, state, why, fate)
	}
	for _, f := range pl.leftover {
		fate := "will be removed"
		if pl.refused != "" {
			fate = "left as is: up is refused"
		}
		fmt.Printf("  %-32s leftover   rendered for a service no longer in the yaml, registered by nothing: %s\n", f, fate)
	}
}

// prepareRenderDir makes the render directory, private, and ignored by git
// from inside: nothing generated ever lands in a commit, and the project's
// own .gitignore is never touched.
func (pr *project) prepareRenderDir(unitDir string) error {
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	if err := systemd.PrivateDir(pr.renderDir); err != nil {
		return err
	}
	// an interrupted write's temp file (another name's up may be writing
	// its own this moment, so only an old one)
	if old, err := filepath.Glob(filepath.Join(pr.renderDir, ".tmp-*")); err == nil {
		for _, f := range old {
			if st, err := os.Lstat(f); err == nil && time.Since(st.ModTime()) > time.Minute {
				os.Remove(f)
			}
		}
	}
	return systemd.WriteUnit(filepath.Join(pr.renderDir, ".gitignore"), "*\n")
}

// apply carries out the plan, its steps in the order written here.
func (pr *project) apply(pl *upPlan) error {
	if err := pr.runBuilds(pl.builds); err != nil {
		return err
	}
	var known []string
	for _, n := range pl.names {
		// A job whose last run failed stays failed, for ps to show, until
		// its next run; its timer starts a failed unit all the same. With
		// a restart: policy it is reset, since systemd's start limit
		// counts the restarts too, and nothing else would clear it.
		if svc := pr.serviceByUnit(n); svc != nil && svc.Schedule != nil && n == pr.p.ServiceUnit(svc) && (svc.Restart == nil || svc.Restart.Policy == "no") {
			continue
		}
		if pl.before[n].Known() {
			known = append(known, n)
		}
	}
	if err := pr.m.ResetFailed(known); err != nil {
		return err
	}
	if err := pr.writeAndRegister(pl); err != nil {
		return err
	}
	pr.removeLeftovers(pl.leftover)
	// Enabled, not started here; the start below is of the target alone.
	// Its Wants= names every unit the services bring up, and starting it
	// starts each one that is down, even with the target already active.
	// Naming the services too would run a quick one twice: systemctl sends
	// one start per unit, and the second finds it already finished.
	if err := pr.m.Enable(pr.p.TargetName()); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	// A socket or timer a service shed goes before that service restarts:
	// a socket still listening hands its fd to the new run, so the dropped
	// listen: would never take effect.
	early, later := pr.takenAddresses(pl.gone)
	shed, gone := append(pl.shed, early...), later
	if err := pr.retire(shed); err != nil {
		return err
	}
	if len(pl.toStop) > 0 {
		if err := pr.m.Run(append([]string{"stop"}, pl.toStop...)...); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		// What the stop of a run from the old definition leaves is not a
		// run of the job that failed.
		if err := pr.m.ResetFailed(pl.toStop); err != nil {
			return err
		}
	}
	res, err := pr.startAll(pl)
	if err != nil {
		return err
	}
	// An orphan is no part of whether the project came up: the plan said
	// it goes (an active one only with --force), and it goes either way.
	if err := pr.retire(gone); err != nil {
		return err
	}
	systemd.SweepEmptyWants(pl.unitDir)
	for _, w := range pl.warnings {
		fmt.Println("WARNING: " + w)
	}
	fmt.Println()
	if err := pr.table(); err != nil {
		return err
	}
	return res.report()
}

// runBuilds runs the builds the plan names, in the project's slice and
// under its limits (sliceProp). So the slice goes first, written,
// registered (linked or copied, as registration: says) and reloaded:
// registered on a first up (or the first after a down), and loaded with
// this render's text on any up (e2e: "and the build saw the new cap"). Reloading only when the file
// changed would miss a slice file an interrupted up wrote but never
// loaded; one reload is small beside a build. A build that then fails
// leaves the slice so; the next up or down settles it.
func (pr *project) runBuilds(builds []*config.Service) error {
	if len(builds) > 0 {
		for _, u := range pr.rendered {
			if u.Name != pr.p.SliceName() {
				continue
			}
			if err := systemd.WriteUnit(filepath.Join(pr.renderDir, u.Name), u.Text); err != nil {
				return err
			}
			unitDir, err := pr.m.UnitDir()
			if err != nil {
				return err
			}
			if err := pr.register(unitDir, []render.Rendered{u}); err != nil {
				return err
			}
			if err := pr.m.Run("daemon-reload"); err != nil {
				return fmt.Errorf("daemon-reload: %w", err)
			}
			break
		}
	}
	for _, s := range builds {
		if err := pr.runBuild(s); err != nil {
			return fmt.Errorf("build %s: %w", s.Name, err)
		}
	}
	return nil
}

// writeAndRegister writes the render and registers its units.
func (pr *project) writeAndRegister(pl *upPlan) error {
	baseline := map[string]bool{}
	for _, r := range pl.rows {
		baseline[r.unit] = r.change != changeNoBaseline
	}
	// Only a unit left running by design: one --no-recreate held stays
	// "changed (not applied)" for the next up, as the README says.
	byDesign := map[string]bool{}
	for _, u := range pl.spared {
		byDesign[u] = true
	}
	for _, u := range pr.rendered {
		path := filepath.Join(pr.renderDir, u.Name)
		if err := systemd.WriteUnit(path, u.Text); err != nil {
			return err
		}
		// A running unit whose file was gone, and that this up may not
		// restart, runs what systemd loaded before: the file written now
		// is taken as that, or "changed (not applied)" would hold it, and
		// what waits on it, forever.
		if st := pl.before[u.Name]; (!baseline[u.Name] && byDesign[u.Name] || pl.envRead[u.Name]) && st.Active() && !st.Started.IsZero() {
			if err := os.Chtimes(path, st.Started, st.Started); err != nil {
				return err
			}
		}
	}
	return pr.register(pl.unitDir, pr.rendered)
}

// register puts rendered units in the unit directory as registration:
// says, without a reload: links into the render directory (systemctl
// link), or copies of the files. A unit of ours registered the other way
// gives way first, with the links enable made to it, so the target's
// next enable links the new file.
func (pr *project) register(unitDir string, units []render.Rendered) error {
	copies := pr.p.Registration == "copy"
	var paths []string
	for _, u := range units {
		path := filepath.Join(pr.renderDir, u.Name)
		if r := pr.registrationOf(unitDir, u.Name); r.kind == "ours" && r.copied != copies {
			if err := unlink(unitDir, u.Name, path); err != nil {
				return err
			}
			if r.copied {
				if err := os.Remove(filepath.Join(unitDir, u.Name)); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
		if !copies {
			paths = append(paths, path)
		} else if err := systemd.WriteUnit(filepath.Join(unitDir, u.Name), u.Text); err != nil {
			return err
		}
	}
	if len(paths) > 0 {
		if err := pr.m.Link(paths); err != nil {
			return fmt.Errorf("link: %w", err)
		}
	}
	return nil
}

// upResult is how the start and the restarts went.
type upResult struct {
	startOut, restartOut string
	startErr, restartErr error
	failed               map[string]string // unit: why it is not up
}

// startAll restarts what changed and starts the target, then waits for
// every unit up acted on to settle.
func (pr *project) startAll(pl *upPlan) (upResult, error) {
	var res upResult
	restarting := map[string]bool{}
	for _, u := range append(slices.Clone(pl.toRestart), pl.bounced...) {
		restarting[u] = true
	}
	var startable, acted []string
	for _, s := range pr.p.EnabledServices() {
		for _, u := range pr.p.UnitsOf(s) {
			startable = append(startable, u)
			// Down, or in a crash loop (mid restart, or restarted within
			// the last half minute): the start below acts on it, and
			// resets the restart count that showed the loop.
			if b := pl.before[u]; !b.Active() || b.Restarting() || b.NRestarts > 0 && time.Since(b.Started) < 30*time.Second {
				acted = append(acted, u)
			}
		}
		// A healthcheck makes the start wait; say for what, and how long.
		// One already starting before this up is waited for too (it said
		// nothing then).
		switch u := pr.p.ServiceUnit(s); {
		case s.Healthcheck == nil:
		case !pl.before[u].Active() || restarting[u]:
			fmt.Printf("  %s: waiting for its healthcheck, %s\n", u, healthWait(s))
		case pl.before[u].ActiveState == "activating":
			fmt.Printf("  %s: starting already; waiting for its healthcheck, %s\n", u, healthWait(s))
		}
	}
	acted = append(append(acted, pl.toRestart...), pl.bounced...)
	slices.Sort(acted)
	acted = slices.Compact(acted)
	// Restarts first, so they reach only what ran before this up, as the
	// plan says (a start-only service this up starts is not then
	// restarted through a dependency); then the start. Both, whatever the
	// first says: a unit that fails to start must not leave the changed
	// ones running their old definition.
	//
	// try-restart passes over a unit still starting (a job's run, a probe
	// waiting), which would then run its old definition, reported as the
	// new one: such a unit is restarted instead.
	var running, starting []string
	for _, u := range pl.toRestart {
		if pl.before[u].ActiveState == "activating" {
			starting = append(starting, u)
		} else {
			running = append(running, u)
		}
	}
	if len(running) > 0 {
		res.restartOut, res.restartErr = pr.m.TryRestart(running)
	}
	if len(starting) > 0 {
		// queued, not waited on here: the start below waits for them and
		// says what it waits for, as it does for any start
		out, err := pr.m.RunQuiet(append([]string{"restart", "--no-block"}, starting...)...)
		res.restartOut = strings.TrimSpace(res.restartOut + "\n" + out)
		if res.restartErr == nil {
			res.restartErr = err
		}
	}
	// A unit with a healthcheck has said what it waits for (above).
	watch := slices.DeleteFunc(slices.Clone(startable), func(u string) bool { return pr.healthchecked(u) != nil })
	res.startOut, res.startErr = pr.m.Start([]string{pr.p.TargetName()}, watch)
	var err error
	res.failed, err = pr.settle(startable, acted)
	return res, err
}

// report says why up failed, after the table: a line per unit that is not
// up, or systemd's own words when the states explain nothing.
func (res upResult) report() error {
	if res.startErr == nil && res.restartErr == nil && len(res.failed) == 0 {
		return nil
	}
	if len(res.failed) == 0 {
		for _, out := range []string{res.restartOut, res.startOut} {
			if out != "" {
				fmt.Fprintln(os.Stderr, out)
			}
		}
	}
	units := make([]string, 0, len(res.failed))
	for u := range res.failed {
		units = append(units, u)
	}
	// Causes before consequences: one that did not start because another
	// is not up comes after that one's own line.
	sort.Slice(units, func(i, j int) bool {
		di, dj := strings.HasPrefix(res.failed[units[i]], didNotStart), strings.HasPrefix(res.failed[units[j]], didNotStart)
		if di != dj {
			return dj
		}
		return units[i] < units[j]
	})
	for _, u := range units {
		fmt.Fprintf(os.Stderr, "systemd-compose: %s %s\n", u, res.failed[u])
	}
	if len(units) > 0 {
		return systemd.ExitError{Code: 1}
	}
	return errors.Join(res.startErr, res.restartErr)
}

// alongside settles what restarts with the units up restarts: systemd
// restarts a unit along with one it Requires=, BindsTo= or is PartOf=, and
// that unit's own dependents with it. A restart that would take
// a running start-only service along is held back, so up never restarts
// one, not even through a dependency; every other running unit it reaches
// says that it restarts too. It returns the restarts to run, the units they
// take along, and a warning for each restart held back.
func (pr *project) alongside(rows []planRow, toRestart []string, before map[string]systemd.UnitState) (keep, bounced, held, warnings []string) {
	dependents := dependentsOf(pr.rendered) // a restart takes their closure along
	row := func(unit string) *planRow {
		for i := range rows {
			if rows[i].unit == unit {
				return &rows[i]
			}
		}
		return nil
	}
	restarting := map[string]bool{}
	for _, u := range toRestart {
		order, _ := closure(dependents, u)
		spared := ""
		for _, d := range order {
			if svc := pr.serviceByUnit(d); svc != nil && svc.OnChange == "start-only" && before[d].Active() {
				spared = svc.Name
				break
			}
		}
		if spared == "" {
			keep = append(keep, u)
			restarting[u] = true
			continue
		}
		held = append(held, u)
		r := row(u)
		r.actions = slices.DeleteFunc(r.actions, func(a string) bool { return strings.HasPrefix(a, "try-restart") })
		r.actions = append(r.actions, "running, NOT restarted: it would take "+spared+" along (on_change: start-only); when convenient: "+pr.applyCmd(u))
		if r.change == changeNoBaseline {
			warnings = append(warnings, u+" was left running, as a restart of it restarts "+spared+" too (on_change: start-only); its rendered file was gone, so up takes the file it wrote as what it runs")
		} else {
			warnings = append(warnings, u+" was left running: a restart of it restarts "+spared+" too, which is on_change: start-only")
		}
	}
	for _, u := range keep {
		order, via := closure(dependents, u)
		for _, d := range order {
			if restarting[d] || !before[d].Active() || d == pr.p.TargetName() {
				continue
			}
			restarting[d] = true
			bounced = append(bounced, d)
			if r := row(d); r != nil {
				r.actions = append(r.actions, "restarts along with "+via[d]+", which it depends on")
			}
		}
	}
	return keep, bounced, held, warnings
}

// reNotExecutable is systemd-analyze verify on a program that is not there.
var reNotExecutable = regexp.MustCompile(`^\S+: Command (.+) is not executable: No such file or directory$`)

// othersDeprecations takes out of verify's words the lines about a file
// beside the render (a drop-in of the user's) that name a setting systemd
// deprecates but honours (KillMode=none, StandardOutput=syslog): such a
// line would refuse every up of every project. A typo there still refuses,
// and so does any line about the rendered units.
func othersDeprecations(out string, verr error, staging string) (string, []string, error) {
	var kept, notes []string
	for _, l := range strings.Split(out, "\n") {
		low := strings.ToLower(l)
		if strings.HasPrefix(l, "/") && !strings.HasPrefix(l, staging+"/") && (strings.Contains(low, "deprecated") || strings.Contains(low, "obsolete")) {
			notes = append(notes, l)
			continue
		}
		kept = append(kept, l)
	}
	if len(notes) == 0 {
		return out, nil, verr
	}
	if rest := strings.TrimSpace(strings.Join(kept, "\n")); rest != "" {
		return rest, notes, verr
	}
	return "", notes, nil
}

// pendingPrograms takes out of verify's words the programs a build: has
// yet to make (a fresh clone: up builds before it starts anything), and
// the failure they alone caused.
func (pr *project) pendingPrograms(out string, verr error) (string, error) {
	var kept []string
	for _, l := range strings.Split(out, "\n") {
		if m := reNotExecutable.FindStringSubmatch(l); m != nil && pr.pending(m[1]) {
			continue
		}
		kept = append(kept, l)
	}
	if rest := strings.TrimSpace(strings.Join(kept, "\n")); rest != out {
		if rest == "" {
			return "", nil
		}
		return rest, verr
	}
	return out, verr
}

// pending reports a path in a creates: of an enabled service that is
// still missing (its build has not run), or the healthcheck probe's copy
// before the first up writes it.
func (pr *project) pending(path string) bool {
	if path == pr.opt.Exe {
		return true // the probe's copy, which only an up that acts writes
	}
	for _, s := range pr.p.EnabledServices() {
		if c := s.Build; c != nil && c.Creates != "" && !exists(c.Creates) && (path == c.Creates || strings.HasPrefix(path, c.Creates+"/")) {
			return true
		}
	}
	return false
}

// applyCmd is the command that applies a new definition to a running unit
// by hand: restart for a service or timer, a stop and start for a socket,
// which restart would leave bound to its old address.
func (pr *project) applyCmd(unit string) string {
	svc := pr.serviceByUnit(unit)
	if svc == nil {
		return "systemctl --user restart " + unit
	}
	if len(svc.Listen) > 0 && unit == pr.p.SocketUnit(svc) {
		return pr.cmd() + " stop " + svc.Name + " && " + pr.cmd() + " start " + svc.Name
	}
	return pr.cmd() + " restart " + svc.Name
}

// unitDeps are the units a rendered unit names in these [Unit] keys.
func unitDeps(text string, keys ...string) []string {
	var out []string
	for _, k := range keys {
		for _, v := range render.Directive(text, "Unit", k) {
			out = append(out, strings.Fields(v)...)
		}
	}
	return out
}

// settle says which of the project's units are not running as they should
// once up is done: failed, restarting, or never started because a required
// dependency is not up. A plain service counts as started once its process
// forks, so when up started or restarted anything (acted) it first gives a
// program that exits at once (a typo, a missing file) a moment to show it.
// A restart counts from up's own start of a unit (systemd resets the count
// then); a unit up did not start is in a loop only after a few, otherwise
// it is mid-way through one automatic restart, which up only notes. The
// result says, per unit, what went wrong and where to look.
func (pr *project) settle(units, acted []string) (map[string]string, error) {
	failed := map[string]string{}
	if len(units) == 0 {
		return failed, nil
	}
	if len(acted) > 0 {
		time.Sleep(showFailure)
	}
	states, err := pr.m.States(units)
	if err != nil {
		return nil, err
	}
	if err := pr.watchRestarts(acted, states); err != nil {
		return nil, err
	}
	exits, err := pr.probeExits(states)
	if err != nil {
		return nil, err
	}
	all, err := pr.m.States(pr.p.UnitNames()) // for the dependencies
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		st := states[u]
		svc := pr.serviceByUnit(u)
		dep := ""
		if svc != nil && u == pr.p.ServiceUnit(svc) && st.ActiveState == "inactive" {
			dep = pr.downDependency(svc, all)
		}
		// a service up started that has ended, though it is not a job
		ran := slices.Contains(acted, u) && svc != nil && u == pr.p.ServiceUnit(svc) && !svc.Oneshot && svc.Schedule == nil && st.ActiveState == "inactive"
		var what string
		switch {
		case dep != "":
			what = didNotStart + needsNote(dep)
		case ran && st.ExitCode > 1:
			// a signal systemd counts as a clean end (a stop from elsewhere
			// while up waited): the project is not up, whatever systemd says
			what = fmt.Sprintf("was stopped (signal %d) after up started it, so nothing of it is running", st.Exit)
		case ran && st.ExitCode > 0:
			// not a failure to systemd, but nothing of it runs: a daemon
			// that forks, a job without oneshot: true
			fmt.Printf("  note: %s ran and exited (status %d), so nothing of it is running; a job that ends wants oneshot: true\n", u, st.Exit)
			continue
		case slices.Contains(acted, u) || st.ActiveState == "failed":
			// a program that lives a second is running at the sample, but
			// it has restarted since up started it
			if what = pr.verdictOf(u, st, exits[u]); what == "" {
				continue
			}
		case st.Restarting():
			fmt.Printf("  note: %s is in an automatic restart (systemd restarts it after a crash)\n", u)
			continue
		default:
			continue
		}
		failed[u] = what + "; see " + pr.logsCmd(u)
	}
	return failed, nil
}

// readEnvFilesSince says a running service's program started after each of
// its env files was last written, so it runs with what they hold now.
func readEnvFilesSince(st systemd.UnitState, svc *config.Service) bool {
	if !st.Active() || st.Ran.IsZero() || len(svc.EnvFiles) == 0 {
		return false
	}
	for _, ef := range svc.EnvFiles {
		if t := mtime(ef.Path); !t.IsZero() && !t.Before(st.Ran) {
			return false
		}
	}
	return true
}

// onlyTriggers reports two renders that differ only in the hash of the
// service's env files: the yaml says the same, a file it reads changed.
func onlyTriggers(a, b string) bool {
	strip := func(s string) string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, render.TriggersKey+"=") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	return strip(a) == strip(b)
}

// ensureProbe writes the probe's copy if it is not there yet: in a
// directory only this user can write, through a file renamed into place,
// so a running probe never sees half a program.
func ensureProbe(opt render.RenderOptions) error {
	if exists(opt.Exe) {
		return nil
	}
	dir := filepath.Dir(opt.Exe)
	if err := systemd.PrivateDir(dir); err != nil {
		return err
	}
	src, err := os.Open(opt.Self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone once renamed
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), opt.Exe)
}
