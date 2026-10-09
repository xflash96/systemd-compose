// Package project carries out the verbs inside a project: up, down, ps,
// logs, start, stop, restart, kill, build, run, exec, config and top, and
// systemctl's own verbs scoped to the project's units. One file per verb;
// project.go chooses the verb and checks what every verb needs first.
package project

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

type project struct {
	signal    string        // kill's, for its report
	waitFor   time.Duration // up --wait-timeout: how long a restarted unit is watched
	p         *config.Project
	m         *systemd.Manager
	renderDir string
	rendered  []render.Rendered // filled by render()
	opt       render.RenderOptions
	self      string // this program as invoked for the project (selfCmd)
	sel       Flags  // the flags that made self, for a hint under another name
	atExit    func() // what a one-off's second ^C, which exits at once, must still do
	// registered stands for the yaml while it does not load: the units
	// registered from it (asRegistered).
	registered *registeredUnits
}

// cmdAs is cmd under another project name: the -f and --profile given
// stay, since a -p alone outside the project's directory finds the
// project registered under that name, not this yaml.
func (pr *project) cmdAs(name string) string {
	f := pr.sel
	f.Name = name
	return selfCmd(f, pr.p.ConfigPath)
}

// cmd is selfCmd for this project, or the bare name when the yaml did not
// load (asRegistered).
func (pr *project) cmd() string {
	if pr.self == "" {
		return "systemd-compose"
	}
	return pr.self
}

// Run carries out verb on the project whose yaml is cfg, with the verb's
// arguments as CheckArgs left them.
func Run(cfg, verb string, args []string, f Flags) error {
	if owner := otherOwner(cfg, os.Getuid()); owner != "" && (slices.Contains(runsSomething, verb) || slices.Contains(changesUnits, verb)) {
		return fmt.Errorf("this project's yaml belongs to %s, and a project runs on its owner's user instance: run this as %s (sudo -u %s -i, or machinectl shell %s@); as root, %s would act on root's own instance instead", owner, owner, owner, owner, verb)
	}
	p, err := config.Load(cfg, config.Options{Name: f.Name, Profiles: f.Profiles})
	if err != nil {
		var typo config.UnknownProfile
		switch {
		case errors.As(err, &typo):
		case registeredVerbs[verb] != nil:
			if done, ferr := asRegistered(cfg, verb, args, f, err); done {
				return ferr
			}
		}
		return err
	}
	if p.NeedsProfile != "" && slices.Contains([]string{"up", "start", "restart", "run", "exec", "build", "config"}, verb) {
		return fmt.Errorf("%s: %s", filepath.Base(cfg), p.NeedsProfile)
	}
	pr := &project{p: p, m: &systemd.Manager{User: true}, renderDir: filepath.Join(p.Dir, config.RenderDirName), self: selfCmd(f, cfg), sel: f}
	if needsBus(verb) {
		if err := pr.m.Reachable(); err != nil {
			return err
		}
	}
	switch {
	case verb == "up" && slices.Contains(args, "--dry-run"):
		// writes nothing, so it waits for no one; it says when its plan
		// may be about to change
		if holder := pr.lockHolder(); holder != "" {
			fmt.Printf("note: %s is working on project %s, so this plan may be stale\n", holder, p.Name)
		}
	// start and restart wait for an up, down or build too: a start racing
	// a down would report a unit started, exit 0, that the down then stops
	case verb == "up" || verb == "down" || verb == "build" || verb == "start" || verb == "restart":
		unlock, err := pr.lock(verb)
		if err != nil {
			return err
		}
		defer unlock()
	}
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
		pr.staleNote()
		printOthers(units, others)
		if len(ours) == 0 {
			if verb == "stop" {
				// Nothing registered is nothing to stop (a project that is
				// down, a service whose profile is off): only one running
				// anyway, someone else's, fails it.
				return pr.stillRunning(verb, args, others)
			}
			return fmt.Errorf("%s: none of these units is registered by this project", verb)
		}
		if err := pr.act(verb, ours, nil); err != nil {
			return err
		}
		if verb == "stop" {
			return pr.stillRunning(verb, args, others)
		}
		if err := heldElsewhere(verb, args, others); err != nil {
			return err
		}
		return pr.namedSkipped(verb, args, ours)
	case "kill":
		flags, names, killSignal, err := killArgs(args)
		if err != nil {
			return err
		}
		var units []string
		for _, a := range names {
			s, err := pr.p.Lookup(a)
			if err != nil {
				return err
			}
			units = append(units, pr.p.ServiceUnit(s))
		}
		if len(units) == 0 {
			return fmt.Errorf("kill needs a service (services: %s); stop takes the whole project down", strings.Join(pr.p.ServiceNames(), ", "))
		}
		ours, others, err := pr.ownedUnits(units)
		if err != nil {
			return err
		}
		pr.scopeLine()
		printOthers(units, others)
		if len(ours) == 0 {
			return pr.stillRunning("kill", names, others) // nothing of ours to signal
		}
		pr.signal = killSignal
		if err := pr.act("kill", ours, flags); err != nil {
			return err
		}
		return pr.stillRunning("kill", names, others)
	case "build":
		return pr.build(args)
	case "run", "exec":
		return pr.runOneOff(verb, args)
	case "config":
		return pr.config()
	case "top":
		// compose's top SERVICE is that service's processes; any other word
		// is refused here, as cgtop would only say "Too many arguments"
		path := p.SliceName()
		var flags []string
		for i := 0; i < len(args); i++ {
			a := args[i]
			if strings.HasPrefix(a, "-") {
				flags = append(flags, a)
				if (a == "-n" || a == "-d" || a == "-M" || a == "--iterations" || a == "--delay" || a == "--depth" || a == "--machine") && i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
				continue
			}
			s, err := p.Lookup(a)
			if err != nil {
				return err
			}
			path = p.SliceName() + "/" + p.ServiceUnit(s)
		}
		// cgtop on a slice that does not run prints nothing at all
		if st, err := pr.m.States([]string{p.SliceName()}); err == nil && !st[p.SliceName()].Active() {
			fmt.Printf("nothing of project %s runs (its slice is not active; up starts it)\n", p.Name)
			return nil
		}
		return systemd.Exec("systemd-cgtop", append(append([]string{"--depth=2"}, flags...), pr.m.CGroupPath(path))...)
	case "list-timers":
		if slices.ContainsFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") }) {
			// a service's name maps to its timer below; a word that is
			// neither a service, a unit nor a pattern is a typo, which
			// systemctl would answer with 0 timers and exit 0
			for _, a := range args {
				if !strings.HasPrefix(a, "-") && !strings.ContainsAny(a, ".*?[") {
					if _, err := p.Lookup(a); err != nil {
						return err
					}
				}
			}
			break // systemctl's patterns, as given
		}
		var timers []string
		for _, s := range p.Services {
			if s.Schedule != nil {
				timers = append(timers, p.TimerUnit(s))
			}
		}
		if len(timers) == 0 {
			fmt.Printf("project %s declares no schedule: (systemctl --user list-timers shows every timer)\n", p.Name)
			return nil
		}
		return pr.m.Exec("systemctl", append(append([]string{"list-timers", "--all"}, args...), timers...)...)
	}
	switch verb {
	case "cancel":
		// with no job named, every pending job of the instance goes
		if !slices.ContainsFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") }) && !slices.Contains(args, "--help") && !slices.Contains(args, "-h") {
			return fmt.Errorf("cancel with no job named cancels every pending job of the user instance, not this project's; systemctl --user list-jobs shows them, and cancel JOB cancels one")
		}
	case "preset-all":
		// it takes no unit: every unit file of the instance, never one project's
		return fmt.Errorf("preset-all resets the enablement of every unit file of the user instance, not this project; systemctl --user preset-all for the instance")
	case "reset-failed", "status", "show", "cat", "list-dependencies", "clean", "list-units", "list-sockets", "list-paths", "list-automounts", "list-unit-files", "is-active", "is-failed", "is-enabled", "freeze", "thaw":
		// with no unit named, systemctl takes the whole user instance: a
		// reset-failed would clear other units' failures, and say nothing
		if !namesAUnit(args) && !slices.Contains(args, "--help") && !slices.Contains(args, "-h") { // status --help is systemctl's help
			return fmt.Errorf("%s with no service named acts on the whole user instance, not this project: name a service (%s), or use systemctl --user %s for the instance", verb, strings.Join(p.ServiceNames(), ", "), verb)
		}
	}
	// A service name maps to its unit only when that unit is ours; the
	// name of a unit somebody else registered is never handed to a verb.
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	mapped := make([]string, 0, len(args))
	changes := slices.Contains(changesUnits, verb)
	dd := false // after a --, systemctl takes every word for a unit
	// what may pass a registration that is not ours: the mask that
	// unmask removes (revert leaves a mask in place)
	mayPass := func(r registration) bool {
		return r.kind == "ours" || r.kind == "none" || r.masked && verb == "unmask"
	}
	for _, a := range args {
		if changes {
			switch {
			case a == "--" && !dd:
				dd = true
				mapped = append(mapped, a)
				continue
			case !dd && slices.Contains(changesValueFlags, a):
				// its value as a word of its own would be taken for a
				// unit, and the unit for its value: reset-failed
				// --job-mode web would reset the whole instance
				return fmt.Errorf("%s %s: write it as %s=VALUE, one word, inside a project", verb, a, a)
			case !dd && (strings.HasPrefix(a, "-") || strings.Contains(a, "=")):
				mapped = append(mapped, a) // a flag, or set-property's KEY=VALUE
				continue
			}
		}
		s := pr.p.Service(a)
		// the project's own: a service, or a unit it renders, registered or not
		own := s != nil || pr.serviceByUnit(a) != nil || a == p.TargetName() || a == p.SliceName()
		if changes && !own {
			// a glob or another project's unit: reset-failed "*" would
			// clear every project's failures, freeze would freeze
			// another's service
			return fmt.Errorf("%q is not of project %s (services: %s); systemctl --user %s %s acts outside this project", a, p.Name, strings.Join(p.ServiceNames(), ", "), verb, a)
		}
		if changes && slices.Contains(Registers, verb) {
			// up and down own the registration: disable would take the
			// link away from a running service, mask would make the next
			// up refuse
			hint := "stop " + a + " keeps it stopped"
			if verb == "enable" || verb == "reenable" || verb == "preset" {
				hint = "up enables the project's target, which starts its services at boot"
			}
			return fmt.Errorf("%s %s: up registers the project's units and down unregisters them; %s of one leaves the project half-registered (%s; systemctl --user %s for a unit outside the project)", verb, a, verb, hint, verb)
		}
		if s == nil {
			// a unit name of ours that a twin of this name, or a hand-
			// written file, holds is not ours to change
			if r := pr.registrationOf(unitDir, a); changes && own && !mayPass(r) {
				return fmt.Errorf("%s: %s is %s; this project does not own it", verb, a, r.owner)
			}
			mapped = append(mapped, a)
			continue
		}
		// A scheduled job is its timer, but for the verbs about its
		// runs: those that list units take its run's unit too, and
		// is-failed asks about the last run alone.
		units := []string{pr.p.UnitOf(s)}
		if s.Schedule != nil {
			switch verb {
			case "status", "cat", "reset-failed", "list-dependencies", "clean":
				units = []string{pr.p.ServiceUnit(s), pr.p.TimerUnit(s)}
			case "is-failed":
				units = []string{pr.p.ServiceUnit(s)}
			}
		}
		for _, u := range units {
			if r := pr.registrationOf(unitDir, u); !mayPass(r) {
				return fmt.Errorf("%s: %s is %s; this project does not own it", verb, u, r.owner)
			}
		}
		mapped = append(mapped, units...)
	}
	return pr.m.Exec("systemctl", append([]string{verb}, mapped...)...)
}

// runsSomething are the verbs that register, start, stop or run what is
// the project's; the others only read.
var runsSomething = strings.Fields("up down start stop restart kill build run exec")

// stillRunning fails a stop or kill given names when a unit it skipped is
// running all the same: one someone else registers (a hand-written unit, a
// twin checkout's), or one loaded from another unit directory, which
// registrationOf does not see. What is skipped and not running is
// stopped, as asked.
func (pr *project) stillRunning(verb string, named []string, others map[string]string) error {
	if len(named) == 0 || len(others) == 0 {
		return nil
	}
	var skipped []string
	for u := range others {
		skipped = append(skipped, u)
	}
	slices.Sort(skipped)
	st, err := pr.m.States(skipped)
	if err != nil {
		return err
	}
	var running []string
	for _, u := range skipped {
		if st[u].Running() {
			running = append(running, u)
		}
	}
	if len(running) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s running, but not registered by this project (above), so left as it is", verb, strings.Join(running, ", "))
}

// needsBus is a verb that talks to the manager. config needs no manager,
// and logs reads the journal, which needs none either: logs works from
// cron or su, with no user bus.
func needsBus(verb string) bool { return verb != "config" && verb != "logs" }

// printOthers says, in the order given, why each unit a verb leaves alone
// is left (ownedUnits' others).
func printOthers(units []string, others map[string]string) {
	for _, n := range units {
		if why, skip := others[n]; skip {
			fmt.Printf("  %-32s %s\n", n, why)
		}
	}
}

// heldElsewhere fails a verb given names when one of them is registered by
// someone else (NOT OURS above). Otherwise stop a b, with b a hand-written
// unit or a twin checkout's, would stop a, leave b running, and exit 0.
func heldElsewhere(verb string, named []string, others map[string]string) error {
	if len(named) == 0 {
		return nil
	}
	var held []string
	for u, why := range others {
		if strings.HasPrefix(why, notOurs) {
			held = append(held, u)
		}
	}
	if len(held) == 0 {
		return nil
	}
	slices.Sort(held)
	return fmt.Errorf("%s: %s registered by someone else (above), so left as it is", verb, strings.Join(held, ", "))
}

// namedSkipped fails a start or restart given service names when one of
// them had nothing registered to act on (the lines above say why): start
// web dbg with dbg's profile off would otherwise exit 0, as if both had
// started. A service with any unit acted on counts as done (a socket added
// since the last up is not registered yet), and a bare verb takes what is
// registered.
func (pr *project) namedSkipped(verb string, named, ours []string) error {
	var skipped []string
	for _, a := range named {
		svc := pr.p.Service(a)
		if svc == nil {
			continue
		}
		if !slices.ContainsFunc(pr.p.UnitsOf(svc), func(u string) bool { return slices.Contains(ours, u) }) {
			skipped = append(skipped, a)
		}
	}
	if len(skipped) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s not registered by this project, so not %sed (above)", verb, strings.Join(skipped, ", "), verb)
}

// checkEnvFiles refuses, for a one-off or a build step, a required env_file
// systemd-run would fail on with exit 1 and not a word (missing, a
// directory, unreadable), which would read as the command's own failure.
// Only a regular file is opened: a FIFO's open would wait for a writer for
// ever. An optional one is left to systemd, as up renders it.
func checkEnvFiles(files []config.EnvFile) error {
	if err := config.CheckEnvFiles(files); err != nil {
		return err
	}
	for _, ef := range files {
		if !ef.Required {
			continue
		}
		f, err := os.Open(ef.Path)
		if err != nil {
			return fmt.Errorf("env_file %s: %v", ef.Path, err)
		}
		f.Close()
	}
	return nil
}

// otherOwner names the owner of a project's yaml when root runs a verb in
// a user's project: root's user instance (XDG_RUNTIME_DIR=/run/user/0 in
// any root login) would start a second copy, from that user's files, of
// what runs under the user. "" for everyone else, and for root's own.
func otherOwner(cfg string, uid int) string {
	st, err := os.Stat(cfg)
	if uid != 0 || err != nil {
		return ""
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid == 0 {
		return ""
	}
	if u, err := user.LookupId(strconv.Itoa(int(sys.Uid))); err == nil {
		return u.Username
	}
	return strconv.Itoa(int(sys.Uid))
}

// logsCmd is the command that shows a unit's logs: its service's, or, for
// a unit of no service, its own.
func (pr *project) logsCmd(unit string) string {
	name := unit
	if s := pr.serviceByUnit(unit); s != nil {
		name = s.Name
	}
	return pr.cmd() + " logs " + name
}

func (pr *project) scopeLine() {
	if pr.registered != nil {
		return // asRegistered's note says what the project is
	}
	profiles := ""
	if len(pr.p.Profiles) > 0 {
		profiles = fmt.Sprintf(", profiles %s from %s", strings.Join(pr.p.Profiles, ","), pr.p.ProfilesFrom)
	}
	units := fmt.Sprintf("%d units", len(pr.p.UnitNames()))
	if n := len(pr.p.DeclaredNames()); n != len(pr.p.UnitNames()) {
		units = fmt.Sprintf("%d of %d units", len(pr.p.UnitNames()), n)
	}
	fmt.Printf("project %s (%s, name from %s%s): %s, from %s\n", pr.p.Name, pr.m.ScopeName(), pr.p.NameFrom, profiles, units, pr.p.ConfigPath)
}

// declared is every unit of the project: the yaml's, of every profile; or,
// while the yaml does not load, the units registered from it.
func (pr *project) declared() []string {
	if pr.registered != nil {
		return pr.registered.all
	}
	return pr.p.DeclaredNames()
}

// unitsFor maps service names to the units start, stop or restart acts on.
// A bare stop takes every service, whatever its profile, so nothing of the
// project keeps running; a named service is acted on whatever its profile,
// as compose does. restart takes the one unit that stands for a service, so
// a listening service keeps its socket open across it.
func (pr *project) unitsFor(verb string, args []string) ([]string, error) {
	var services []*config.Service
	if len(args) == 0 {
		services = pr.p.EnabledServices()
		if verb == "stop" {
			services = pr.p.Services
		}
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
		switch {
		case verb == "restart":
			units = append(units, pr.p.UnitOf(s))
		case verb == "stop" && s.Schedule != nil:
			// the timer, and a run of the job in progress
			units = append(units, pr.p.TimerUnit(s), pr.p.ServiceUnit(s))
		default:
			units = append(units, pr.p.UnitsOf(s)...)
		}
	}
	return units, nil
}

func (pr *project) render() error {
	if pr.rendered != nil {
		return nil
	}
	opt, err := render.DefaultRenderOptions()
	if err != nil {
		return err
	}
	pr.opt = opt
	pr.rendered, err = render.Render(pr.p, opt)
	return err
}

func (pr *project) config() error {
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	for _, n := range pr.p.Notes {
		fmt.Println("note: " + n)
	}
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

func (pr *project) serviceByUnit(unit string) *config.Service {
	for _, s := range pr.p.Services {
		if pr.p.ServiceUnit(s) == unit || pr.p.TimerUnit(s) == unit || pr.p.SocketUnit(s) == unit {
			return s
		}
	}
	return nil
}
