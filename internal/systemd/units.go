package systemd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Link links unit files where they are, without a reload: up reloads once
// after all its links.
func (m *Manager) Link(paths []string) error {
	return m.Run(append([]string{"link", "--no-reload"}, paths...)...)
}

// LinkLoaded links and reloads, for a unit needed before up's own reload:
// the manager does not see a file linked with --no-reload. Re-linking an
// already-linked path reloads too, so an active slice takes its new text;
// The e2e check "and the build saw the new cap" holds it.
func (m *Manager) LinkLoaded(path string) error { return m.Run("link", path) }

// Enable enables a unit: for a target, boot starts it.
func (m *Manager) Enable(unit string) error { return m.Run("enable", unit) }

// Start starts the units in one transaction. systemd's own words on a
// failure are returned, not printed: the caller explains the failure from
// the units' states and shows these only when it cannot.
//
// When the start takes a while, it says which of the watched units are
// still starting and for how long systemd waits: a oneshot: true whose
// program does not exit has no start timeout, and up would otherwise wait
// for it in silence.
func (m *Manager) Start(units, watch []string) (string, error) {
	var stderr bytes.Buffer
	err := m.run(&stderr, append([]string{"start"}, units...), watch, func(jobs map[string]string) {
		for _, u := range watch {
			switch job := jobs[u]; {
			case job == "start waiting":
				fmt.Printf("  %s is waiting for a unit it depends on to finish starting\n", u)
			case job == "start running":
				switch limit := m.property(u, "TimeoutStartUSec"); limit {
				case "":
				case "infinity":
					fmt.Printf("  %s is still starting, and nothing times it out (oneshot: true waits for its program to exit); ^C, then systemd-compose down, if it never will\n", u)
				default:
					fmt.Printf("  %s is still starting: systemd waits up to %s, its start timeout\n", u, limit)
				}
			}
		}
	})
	return strings.TrimSpace(stderr.String()), err
}

// property is one property of a unit as systemctl show prints it, or "".
func (m *Manager) property(unit, name string) string {
	out, err := m.Cmd("systemctl", "show", "-p", name, "--value", unit).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Jobs is the manager's queued and running jobs on these units, unit ->
// "TYPE STATE" (start running, start waiting, stop running...).
func (m *Manager) Jobs(units []string) (map[string]string, error) {
	jobs := map[string]string{}
	if len(units) == 0 {
		return jobs, nil
	}
	out, err := m.Cmd("systemctl", append([]string{"list-jobs", "--no-legend", "--plain"}, units...)...).Output()
	if err != nil {
		return nil, CmdErr("systemctl list-jobs", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(l); len(f) >= 4 {
			jobs[f[1]] = f[2] + " " + f[3]
		}
	}
	return jobs, nil
}

// SettleFor is how long Settle waits.
const SettleFor = 10 * time.Second

// Settle waits up to SettleFor for the jobs on these units to finish: a
// systemctl verb returns when the named unit's job is done, while the jobs
// it set off on the units around it (a dependent's restart) may still be
// queued. It returns the jobs still there at the end.
func (m *Manager) Settle(units []string) (map[string]string, error) {
	deadline := time.Now().Add(SettleFor)
	for {
		jobs, err := m.Jobs(units)
		if err != nil || len(jobs) == 0 || time.Now().After(deadline) {
			return jobs, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TryRestart restarts the units that run; systemd's words on a failure are
// returned, as Start's are.
func (m *Manager) TryRestart(units []string) (string, error) {
	return m.RunQuiet(append([]string{"try-restart"}, units...)...)
}

// Stop stops units, and says so when a stop takes a while: a program that
// ignores SIGTERM keeps a stop waiting for its stop timeout, and a terminal
// with nothing on it for a minute and a half reads as hung. A slice or
// target has no stop timeout of its own: it waits for its members, which
// say why.
func (m *Manager) Stop(units []string) error {
	return m.run(os.Stderr, append([]string{"stop"}, units...), units, func(jobs map[string]string) {
		for _, u := range units {
			if !strings.HasPrefix(jobs[u], "stop ") {
				continue
			}
			switch limit := m.property(u, "TimeoutStopUSec"); limit {
			case "":
			case "infinity":
				fmt.Printf("  %s is still stopping, and its stop timeout is infinity: systemd waits for it to exit and never kills it\n", u)
			default:
				then := "then kills it"
				if m.property(u, "SendSIGKILL") == "no" {
					then = "then gives up and leaves it running (SendSIGKILL=no)"
				}
				fmt.Printf("  %s is still stopping: systemd waits up to %s, its stop timeout, %s\n", u, limit, then)
			}
		}
	})
}

// ResetFailed clears failed state on exactly these units; a name that is
// not loaded is not an error here, because "nothing to reset" is fine.
func (m *Manager) ResetFailed(units []string) error {
	if len(units) == 0 {
		return nil
	}
	c := m.Cmd("systemctl", append([]string{"reset-failed"}, units...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil && !strings.Contains(stderr.String(), "not loaded") {
		return fmt.Errorf("reset-failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Verify runs systemd-analyze verify on the paths and returns everything it
// said. The caller refuses on ANY output: a typo in a directive is a warning
// with exit 0, and that is the silent failure this gate exists to catch.
func (m *Manager) Verify(paths []string) (string, error) {
	c := m.Cmd("systemd-analyze", append([]string{"verify"}, paths...)...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return strings.TrimSpace(out.String()), AsExit(err)
}

// ControlDropIns is, per unit, the drop-ins systemctl set-property wrote
// (under a .control directory): they win over the unit's own file, and
// outlast a disable.
func (m *Manager) ControlDropIns(units []string) (map[string][]string, error) {
	out, err := m.Cmd("systemctl", append([]string{"show", "-p", "Id,DropInPaths"}, units...)...).Output()
	if err != nil {
		return nil, CmdErr("systemctl show", err)
	}
	got := map[string][]string{}
	id := ""
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "Id="); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(l, "DropInPaths="); ok {
			for _, f := range strings.Fields(v) {
				if strings.Contains(f, ".control/") {
					got[id] = append(got[id], f)
				}
			}
		}
	}
	return got, nil
}

// Leftover counts the processes still in a unit's cgroup (path as
// CGroupPath gives it): after a stop that timed out under SendSIGKILL=no,
// systemd marks the unit failed and leaves them running.
func Leftover(cgroup string) int {
	b, err := os.ReadFile(filepath.Join(cgroupRoot, cgroup, "cgroup.procs"))
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(b)))
}
