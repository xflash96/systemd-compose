package project

import (
	"fmt"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/probe"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// healthchecked is the service whose own unit is u and has a healthcheck;
// nil for any other unit.
func (pr *project) healthchecked(u string) *config.Service {
	if svc := pr.serviceByUnit(u); svc != nil && svc.Healthcheck != nil && u == pr.p.ServiceUnit(svc) {
		return svc
	}
	return nil
}

// probeExits is the healthcheck verdict of each failed unit that has one.
// Only a failed unit's verdict is ever read.
func (pr *project) probeExits(states map[string]systemd.UnitState) (map[string]int, error) {
	var probed []string
	for n, st := range states {
		if pr.healthchecked(n) != nil && st.ActiveState == "failed" {
			probed = append(probed, n)
		}
	}
	if len(probed) == 0 {
		return map[string]int{}, nil
	}
	return pr.m.ProbeExits(probed, probe.IsProbe)
}

// whyNot says why a service's unit did not start when systemd's job
// failed around it: its socket, or a service it requires.
func (pr *project) whyNot(unit string, svc *config.Service, states map[string]systemd.UnitState) string {
	if svc == nil || unit != pr.p.ServiceUnit(svc) {
		return ""
	}
	if len(svc.Listen) > 0 && states[pr.p.SocketUnit(svc)].ActiveState == "failed" {
		return ": its socket failed"
	}
	if dep := pr.downDependency(svc, states); dep != "" {
		return needsNote(dep)
	}
	return ""
}

// verdictOf is verdict for one of this project's units: a socket that
// failed says the addresses it could not listen on.
func (pr *project) verdictOf(unit string, st systemd.UnitState, probeExit int) string {
	svc := pr.serviceByUnit(unit)
	if svc != nil && len(svc.Listen) > 0 && unit == pr.p.SocketUnit(svc) && st.ActiveState == "failed" {
		shown := strings.ReplaceAll(strings.Join(svc.Listen, ", "), "%%", "%")
		// services of profiles that take turns on one address: the other
		// one, left as is when its profile went off, may still hold it
		for _, o := range pr.p.Services {
			if o != svc && !pr.p.Enabled(o) && slices.ContainsFunc(o.Listen, func(a string) bool { return slices.Contains(svc.Listen, a) }) {
				return fmt.Sprintf("failed to start: it could not listen on %s, which service %s (its profile not in this run) listens on too and may still hold: %s stop %s, then %s up", shown, o.Name, pr.cmd(), o.Name, pr.cmd())
			}
		}
		return "failed to start: it could not listen on " + shown + " (in use, or a directory that is not there)"
	}
	return verdict(st, svc, probeExit)
}

// lowPids notes a pids: cap low enough to stop the probe itself: its
// processes and threads count against the service's cap. Below about 8 the
// probe cannot run, and the failure would read as the test's.
func lowPids(svc *config.Service) string {
	if svc == nil || svc.Resources == nil || svc.Resources.PIDs == 0 || svc.Resources.PIDs >= 16 {
		return ""
	}
	return fmt.Sprintf("; its pids: %d counts the probe's own processes and threads too, and so low a cap can keep the probe from running", svc.Resources.PIDs)
}

// verdict words what is wrong with a unit that was just started, from its
// state and its healthcheck probe's exit (-1 or 0: none, or it passed);
// "" when it runs, or is still starting. systemd resets NRestarts at every
// start but its own, so any count means it has died since. It is read once
// watchRestarts is done, so a healthchecked unit still starting after a
// restart has had its healthcheck budget and did not become ready in it.
func verdict(st systemd.UnitState, svc *config.Service, probeExit int) string {
	healthchecked := svc != nil && svc.Healthcheck != nil
	failed := st.ActiveState == "failed"
	switch {
	case failed && st.Result == "timeout" && healthchecked:
		return "failed to start: its healthcheck did not pass within start_period + timeout (its start timeout), and systemd stopped it" + lowPids(svc)
	case failed && st.Result == "timeout":
		return "failed to start: it was not up within its start timeout (TimeoutStartSec), and systemd stopped it"
	case failed && healthchecked && st.Result == "signal" && st.ExitCode == 2 && st.Exit == 15 && probeExit != probe.MainGone:
		// a stop from elsewhere kills the probe and the program alike,
		// which is no failure of the healthcheck
		return "stopped from elsewhere (systemctl stop, a down) while its healthcheck was still waiting"
	case failed && probeExit == probe.MainGone:
		return "failed to start: the program exited before its healthcheck passed"
	case failed && probeExit == probe.TimedOut:
		return "failed to start: its healthcheck test ran past its timeout:"
	case failed && probeExit > 0:
		return fmt.Sprintf("failed to start: its healthcheck did not pass (last probe exit %d)%s", probeExit, lowPids(svc))
	case failed && st.Result == "oom-kill":
		return "failed: the kernel killed it for want of memory (resources: memory)"
	case failed && st.ExitCode == 1 && st.Exit > 0:
		return fmt.Sprintf("failed to start (it exited with status %d)", st.Exit)
	case failed && st.ExitCode > 1:
		return fmt.Sprintf("failed to start (killed by signal %d)", st.Exit)
	case failed:
		return "failed to start"
	case st.Restarting() || st.ActiveState == "active" && st.NRestarts > 0 && !steady(st):
		// what ended the last run, as systemd recorded it
		how := ""
		switch {
		case healthchecked && st.ExitCode > 1 && st.Result == "exit-code":
			how = ": its healthcheck did not pass, and systemd stopped the program to retry"
		case st.ExitCode == 1:
			how = fmt.Sprintf(": the program exits (status %d)", st.Exit)
		case st.ExitCode > 1:
			how = fmt.Sprintf(": the program is killed (signal %d)", st.Exit)
		}
		if st.NRestarts == 0 {
			return "failed to start, and its restart: policy is trying again" + how
		}
		return fmt.Sprintf("keeps restarting (restart #%d)%s; it did not stay up %s while watched. If it waits for a slow dependency, a healthcheck on that one and condition: service_healthy make it wait instead, or up --wait-timeout watches longer", st.NRestarts, how, steadyAfter)
	case healthchecked && st.ActiveState == "activating" && st.NRestarts > 0:
		return fmt.Sprintf("keeps restarting (restart #%d): its healthcheck has not passed while watched, and it is starting again; up --wait-timeout watches longer", st.NRestarts)
	}
	return ""
}

// healthOf words a healthchecked service's state: starting while it
// activates (the probe runs as ExecStartPost=), ready once it passed,
// probe failed when the probe is why it failed, unhealthy while systemd
// keeps restarting it.
func healthOf(st systemd.UnitState, probeExit int) string {
	switch {
	case st.Restarting() && st.NRestarts > 0:
		return fmt.Sprintf("restarting (#%d)", st.NRestarts)
	case st.Restarting():
		return "restarting"
	case st.ActiveState == "activating":
		return "starting"
	case st.ActiveState == "active" || st.ActiveState == "reloading":
		return "ready"
	case st.ActiveState == "failed" && probeExit == probe.MainGone:
		return "program exited"
	case st.ActiveState == "failed" && probeExit > 0:
		return "probe failed"
	case st.ActiveState == "failed":
		return "failed" // a reload forgets the probe's exit; "-" would read as no news
	}
	return "-"
}

// healthBudget is how long a start waits for a service's healthcheck: the
// TimeoutStartSec= the renderer writes, once per attempt.
func healthBudget(s *config.Service) string {
	b := fmt.Sprintf("%ds", s.Healthcheck.StartTimeout())
	if s.Restart != nil && s.Restart.Policy != "no" {
		b += " per attempt (restart: retries)"
	}
	if d := config.Default("healthcheck.start_period"); s.Healthcheck.StartPeriod == d {
		b += "; start_period is " + d + " unless set, and a shorter one fails sooner"
	}
	return b
}
