package project

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// act runs start, stop, restart or kill on units of this project and says
// what happened to each unit it can reach: the named ones, and those their
// dependencies move (reach). A change is read from the states before and
// after, once the jobs the command set off have run; a unit that changed
// for its own reasons (a timer firing) gets no line. A start or restart
// that leaves a unit failed, in a restart loop or not started exits 1, as
// up does. systemctl's own words are printed only when this report says
// nothing about a unit it named.
func (pr *project) act(verb string, units, flags []string) error {
	names := pr.declared()
	before, err := pr.m.States(names)
	if err != nil {
		return err
	}
	if verb == "kill" {
		// A unit that is not running has nothing to signal: systemctl
		// would signal nothing, or fail with "No main process to kill".
		var live []string
		for _, u := range units {
			if before[u].Running() {
				live = append(live, u)
			} else {
				state := before[u].ActiveState
				if before[u].Finished() {
					state = "a oneshot that has finished" // rather than "not running (active)"
				}
				fmt.Printf("  %-32s nothing to signal: it is not running (%s)\n", u, state)
			}
		}
		if len(live) == 0 {
			return nil
		}
		units = live
	}
	named := map[string]bool{}
	for _, u := range units {
		named[u] = true
	}
	reach := pr.reach(verb, units)
	watched := slices.Clone(units)
	for u := range reach {
		watched = append(watched, u)
	}
	if verb == "start" || verb == "restart" {
		// A start asked for by name clears a failed state first, as up's
		// does: systemd refuses a unit that hit its start limit
		// ("Start request repeated too quickly") until it is reset.
		var failedBefore []string
		for _, u := range units {
			if before[u].ActiveState == "failed" {
				failedBefore = append(failedBefore, u)
			}
		}
		if err := pr.m.ResetFailed(failedBefore); err != nil {
			return err
		}
	}
	var said string
	var runErr error
	if verb == "stop" {
		runErr = pr.m.Stop(units) // says when a stop waits on a stop timeout
	} else {
		said, runErr = pr.m.RunQuiet(append(append([]string{verb}, flags...), units...)...)
	}
	var pending map[string]string
	if verb == "kill" {
		time.Sleep(500 * time.Millisecond) // a restart policy acts within moments
	} else if pending, err = pr.m.Settle(watched); err != nil {
		return err
	}
	after, err := pr.m.States(names)
	if err != nil {
		return err
	}
	if verb == "start" || verb == "restart" {
		for _, n := range watched {
			if !after[n].Started.Equal(before[n].Started) {
				time.Sleep(showFailure)
				if after, err = pr.m.States(names); err != nil {
					return err
				}
				break
			}
		}
		if err := pr.watchRestarts(watched, after); err != nil {
			return err
		}
	}
	exits, err := pr.probeExits(after)
	if err != nil {
		return err
	}
	var failed, reset []string
	alongside := false
	printed := map[string]bool{}
	for _, n := range names {
		if n == pr.p.SliceName() || n == pr.p.TargetName() || !named[n] && !reach[n] {
			continue // the project's own follow its services; the rest moved, if at all, on their own
		}
		b, a := before[n], after[n]
		svc := pr.serviceByUnit(n)
		job := svc != nil && svc.Schedule != nil && n == pr.p.ServiceUnit(svc)
		started := !a.Started.Equal(b.Started)
		logs := "; see " + pr.logsCmd(n)
		v := pr.verdictOf(n, a, exits[n])
		what, along := "", ""
		switch {
		case verb == "kill" && named[n]:
			if runErr == nil {
				what = "sent " + signalLabel(pr.signal) + "; " + killed(b, a, svc, pr.cmd())
			}
		case pending[n] != "":
			job, _, _ := strings.Cut(pending[n], " ")
			what = "still " + map[string]string{"start": "starting", "stop": "stopping", "restart": "restarting"}[job] + " (its " + job + " job had not finished after " + systemd.SettleFor.String() + ")"
		case job && b.Active() && !a.Active():
			what = "stopped a run in progress"
			reset = append(reset, n) // a run stopped on purpose is no failed run
		case verb == "stop" && b.Active() && a.ActiveState == "failed":
			// systemd records a stop that cut a start short, or a program
			// that exits non-zero on SIGTERM, as a failure; the stop caused it
			switch {
			case a.Result == "timeout" && pr.leftRunning(n):
				// SendSIGKILL=no: systemd gives up and calls the unit stopped
				what, along = "NOT stopped: it outlived its stop timeout, and SendSIGKILL=no left it running", "; systemctl --user kill -s KILL "+n+" ends it"
				failed = append(failed, n)
			case a.Result == "timeout":
				what = "stopped, but only after systemd killed it at its stop timeout (it ignored SIGTERM)"
			case b.ActiveState == "activating":
				what = "stopped while it was still starting"
			default:
				what = "stopped"
			}
			reset = append(reset, n)
		case (started || named[n] && runErr != nil) && verb != "stop" && v != "":
			what, along = v, logs
			failed = append(failed, n)
		case started && verb != "stop" && !a.Active() && b.ActiveState == "activating" && a.ExitCode > 1:
			what = "stopped before it finished starting" // by a stop from elsewhere
		case started && verb != "stop" && !a.Active() && a.ExitCode > 1:
			what = fmt.Sprintf("was killed by signal %d", a.Exit)
		case started && verb != "stop" && !a.Active():
			what = fmt.Sprintf("ran and exited (status %d)", a.Exit)
		case b.Active() && !a.Active():
			what = "stopped"
		case !b.Active() && a.Active():
			what = "started"
		case b.Active() && a.Active() && started && a.Finished():
			what = "ran again"
		case b.Active() && a.Active() && started:
			what = "restarted"
		case runErr != nil && named[n] && verb != "stop" && !a.Active():
			what, along = didNotStart+pr.whyNot(n, svc, after), logs
			failed = append(failed, n)
		case runErr != nil || !named[n]:
		case job && !a.Active():
			// stop takes a job's own unit with its timer, for a run in
			// progress; between runs there is nothing to say
		case a.Finished():
			what = "already ran (a oneshot; restart runs it again)"
		case a.Active():
			what = "already running"
		default:
			what = "already " + a.ActiveState
		}
		if what == "" {
			continue
		}
		if !named[n] {
			what += " too, through a dependency"
			alongside = alongside || verb == "stop"
		}
		if svc != nil && svc.Schedule != nil && n == pr.p.TimerUnit(svc) && verb != "stop" {
			what += " (" + svc.Name + " itself runs when the timer fires; " + pr.cmd() + " run " + svc.Name + " runs it now)"
		}
		fmt.Printf("  %-32s %s%s\n", n, what, along)
		printed[n] = true
	}
	if err := pr.m.ResetFailed(reset); err != nil {
		return err
	}
	switch {
	case alongside && pr.registered != nil:
		fmt.Println("  (once the yaml loads, start brings the project back)")
	case alongside:
		fmt.Printf("  (%s start brings the project back; %s start SERVICE starts it and what it depends on)\n", pr.cmd(), pr.cmd())
	}
	// A dependent stopped along with a service does not come back with it.
	if verb == "start" || verb == "restart" {
		for _, svc := range pr.p.EnabledServices() {
			u := pr.p.ServiceUnit(svc)
			if named[u] || printed[u] || svc.Schedule != nil || after[u].Active() {
				continue
			}
			for _, d := range svc.DependsOn {
				dep := pr.p.Service(d.Service)
				// restart: true (PartOf=) stops it with the dependency too,
				// and the rows above do not name it
				if !d.Required && !d.Restart || dep == nil || !named[pr.p.UnitOf(dep)] {
					continue
				}
				why := "it requires " + d.Service
				if !d.Required {
					why = "it stops with " + d.Service + " (restart: true)"
				}
				if after[pr.p.UnitOf(dep)].Active() {
					fmt.Printf("  %-32s still stopped: %s; %s start %s, or start the whole project\n", u, why, pr.cmd(), svc.Name)
				} else {
					fmt.Printf("  %-32s still stopped: %s, and %s is not up\n", u, why, d.Service)
				}
				break
			}
		}
	}
	// systemd's words, when the report above has not said why the verb
	// fails (a row like "restarted" says nothing of a job that failed).
	if runErr != nil && said != "" && len(failed) == 0 {
		fmt.Fprintln(os.Stderr, said)
	}
	switch {
	case len(failed) > 0:
		return systemd.ExitError{Code: 1}
	case runErr != nil:
		return runErr
	}
	return nil
}

// reach is what a verb on these units can move through the rendered
// dependencies, transitively: a start pulls in what they Require=, Want= or
// BindTo=; a stop takes along what Requires=, BindsTo= or is PartOf= them;
// a restart does both; a kill takes along what a stop would, as the unit
// it ends (and its restart:) moves them. When the yaml
// does not render, the files the units were registered from say the same.
func (pr *project) reach(verb string, units []string) map[string]bool {
	var rendered []render.Rendered
	if pr.render() == nil {
		rendered = pr.rendered
	} else {
		rendered = pr.ownRendered()
	}
	out := map[string]bool{}
	pulls := map[string][]string{} // unit -> what starting it starts
	for _, u := range rendered {
		pulls[u.Name] = unitDeps(u.Text, "Requires", "Wants", "BindsTo")
	}
	walk := func(edges map[string][]string) {
		order, _ := closure(edges, units...)
		for _, d := range order {
			out[d] = true
		}
	}
	if verb != "stop" && verb != "kill" {
		walk(pulls)
	}
	if verb != "start" {
		walk(dependentsOf(rendered)) // a kill that stops a unit restarts what is part of it
	}
	return out
}

// closure is what edges reach from roots, transitively, in the order a
// breadth-first walk finds it, each with the unit it was reached from.
// The roots are not in it.
func closure(edges map[string][]string, roots ...string) (order []string, via map[string]string) {
	via = map[string]string{}
	for _, r := range roots {
		via[r] = ""
	}
	for queue := slices.Clone(roots); len(queue) > 0; queue = queue[1:] {
		for _, d := range edges[queue[0]] {
			if _, seen := via[d]; !seen {
				via[d] = queue[0]
				order = append(order, d)
				queue = append(queue, d)
			}
		}
	}
	return order, via
}

// dependentsOf maps each unit to the units systemd stops or restarts along
// with it, those that Require=, BindsTo= or are PartOf= it, in the order of
// rendered.
func dependentsOf(rendered []render.Rendered) map[string][]string {
	out := map[string][]string{}
	for _, u := range rendered {
		for _, d := range unitDeps(u.Text, "Requires", "BindsTo", "PartOf") {
			out[d] = append(out[d], u.Name)
		}
	}
	return out
}

// steadyAfter is how long a unit that has restarted must stay up to count
// as a retry that worked (a dependency not ready the first time) rather
// than a crash loop caught between two crashes.
const steadyAfter = 3 * time.Second

// showFailure is how long start, restart and up wait after a start before
// they read the states: a plain service counts as started once its process
// forks, and one that exits at once needs a moment to show it.
const showFailure = 2 * time.Second

// defaultWait is how long a restarted unit is watched without up
// --wait-timeout.
const defaultWait = 15 * time.Second

// steady is a unit whose program has run steadyAfter, or whose run ended in
// success. The run is the program's (ExecMainStartTimestamp).
// InactiveExitTimestamp moves when an automatic restart begins, a restart
// delay before the program runs, so a loop with a 3s delay would read as
// steady from it.
func steady(st systemd.UnitState) bool {
	if st.ActiveState == "inactive" && st.Result == "success" {
		return true
	}
	since := st.Ran
	if since.IsZero() {
		since = st.Started
	}
	return st.Active() && !st.Restarting() && !since.IsZero() && time.Since(since) >= steadyAfter
}

// watchRestarts gives the units just started time to settle, re-reading
// their states until each program has run steadyAfter: one restart on the
// way up is a retry, a loop is not, and a single sample cannot tell the
// two (nor a program that dies a moment after it was sampled alive).
// It watches pr.waitFor (defaultWait unless up --wait-timeout says so), and
// a healthchecked unit the budget up announced for it. A unit whose
// program runs while its start goes on (a probe in start-post, a oneshot's
// run) is starting, not looping: the probe's own wait decides it, and a
// oneshot's run is no loop at all.
func (pr *project) watchRestarts(units []string, states map[string]systemd.UnitState) error {
	begun := time.Now()
	wait := pr.waitFor
	if wait == 0 {
		wait = defaultWait
	}
	limit := func(u string) time.Duration {
		if svc := pr.healthchecked(u); svc != nil {
			if b := time.Duration(svc.Healthcheck.StartTimeout()) * time.Second; b > wait {
				return b
			}
		}
		return wait
	}
	unsettled := func(u string, st systemd.UnitState) bool {
		switch {
		case st.ActiveState == "failed" || st.ActiveState == "inactive":
			return false
		case st.Restarting():
			return true
		case st.ActiveState == "activating":
			svc := pr.serviceByUnit(u)
			return svc != nil && svc.Healthcheck != nil
		}
		return !steady(st)
	}
	said := false
	for {
		var watching []string
		for _, u := range units {
			if unsettled(u, states[u]) && time.Since(begun) < limit(u) {
				watching = append(watching, u)
			}
		}
		if len(watching) == 0 {
			return nil
		}
		var restarted []string
		for _, u := range watching {
			if states[u].NRestarts > 0 || states[u].Restarting() {
				restarted = append(restarted, u)
			}
		}
		if !said && len(restarted) > 0 {
			fmt.Printf("  %s restarted since it was started; watching whether it stays up\n", strings.Join(restarted, ", "))
			said = true
		}
		time.Sleep(500 * time.Millisecond)
		fresh, err := pr.m.States(watching)
		if err != nil {
			return err
		}
		for u, st := range fresh {
			states[u] = st
		}
	}
}

// killArgs splits kill's arguments into the flags handed to systemctl,
// the service names and the signal (TERM unless -s says otherwise). Only
// -s and systemctl kill's own --kill-whom (--kill-who) and --kill-value
// pass, each with its value: any other flag would reach systemctl as one
// of its own, or turn its value into a service name. The signal is handed
// on in capitals, which systemctl's parser needs (sigkill is refused
// there).
func killArgs(args []string) (flags, names []string, signal string, err error) {
	signal = "TERM" // systemctl kill's default (compose's kill sends KILL)
	setSignal := func(v string) error {
		if !signalName(v) {
			return fmt.Errorf("kill: %q is no signal: give a name (TERM, SIGKILL, HUP, USR1...) or a number (9)", v)
		}
		signal = strings.TrimPrefix(strings.ToUpper(v), "SIG")
		flags = append(flags, "--signal="+signal)
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			names = append(names, a)
			continue
		}
		switch name := flagName(a); {
		case name == "-s" || name == "--signal":
			v, ok := strings.CutPrefix(a, name+"=")
			if !ok {
				if i+1 >= len(args) {
					return nil, nil, "", fmt.Errorf("kill: %s needs a signal (TERM, KILL, HUP, 9...)", a)
				}
				i++
				v = args[i]
			}
			if err := setSignal(v); err != nil {
				return nil, nil, "", err
			}
		case name == "--kill-whom" || name == "--kill-who" || name == "--kill-value":
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case strings.HasPrefix(a, "-s") && !strings.HasPrefix(a, "--"): // -sHUP
			if err := setSignal(a[2:]); err != nil {
				return nil, nil, "", err
			}
		default:
			return nil, nil, "", fmt.Errorf("kill: unknown flag %q (kill takes -s SIGNAL)", a)
		}
	}
	return flags, names, signal, nil
}

// signalNames are the signals by number, for a report of kill -s 9.
var signalNames = strings.Fields("HUP INT QUIT ILL TRAP ABRT BUS FPE KILL USR1 SEGV USR2 PIPE ALRM TERM STKFLT CHLD CONT STOP TSTP TTIN TTOU URG XCPU XFSZ VTALRM PROF WINCH IO PWR SYS")

// signalLabel names a signal as kill was given it: SIGKILL for KILL or 9,
// "signal 40" for a number with no name.
func signalLabel(sig string) string {
	if n, err := strconv.Atoi(sig); err == nil {
		if n >= 1 && n <= len(signalNames) {
			return "SIG" + signalNames[n-1]
		}
		return "signal " + sig
	}
	return "SIG" + sig
}

// signalName reports a signal kill can send: a name, with or without SIG,
// any case, RTMIN+n and RTMAX-n, or a number.
func signalName(s string) bool {
	if n, err := strconv.Atoi(s); err == nil {
		return n > 0 && n < 65
	}
	s = strings.TrimPrefix(strings.ToUpper(s), "SIG")
	for _, rt := range []string{"RTMIN+", "RTMAX-"} {
		if v, ok := strings.CutPrefix(s, rt); ok {
			n, err := strconv.Atoi(v)
			return err == nil && n >= 0 && n <= 30
		}
	}
	return slices.Contains(signalNames, s) || slices.Contains(strings.Fields("IOT POLL RTMIN RTMAX"), s) // IOT is ABRT, POLL is IO
}

// killed words what a signal did to a unit, from its states before and
// after: a restart: policy brought it back (a restart counted, or one
// pending), something else did (a new start time), the signal did not stop
// it (HUP, USR1: most programs reload or reopen their logs), or it is down.
func killed(b, a systemd.UnitState, svc *config.Service, cmd string) string {
	keep := ""
	if svc != nil {
		keep = " (stop " + svc.Name + " keeps it down)"
	}
	switch {
	case a.NRestarts > b.NRestarts || a.SubState == "auto-restart":
		return "it stopped, and its restart: policy brings it back" + keep
	case a.Active() && !a.Started.Equal(b.Started):
		return "it stopped and came back" + keep
	case a.Active() && svc != nil:
		return "still running (the signal did not stop it; " + cmd + " kill -s KILL " + svc.Name + " does)"
	case a.Active():
		return "still running (the signal did not stop it)"
	}
	return "now " + a.ActiveState
}
