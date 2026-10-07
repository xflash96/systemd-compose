package systemd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// UnitState is what systemctl show says of one unit.
type UnitState struct {
	Id, LoadState, ActiveState, SubState, UnitFileState string
	FragmentPath                                        string // the unit file systemd loaded it from; "" for one made on demand

	Started   time.Time // InactiveExitTimestamp: when the current run began; zero if never
	Ran       time.Time // ExecMainStartTimestamp: when the program last started (after any restart delay)
	NRestarts int       // automatic restarts since the last start by hand
	NextRun   string    // a timer's next elapse as systemd prints it (local time); "" for anything else
	ExitCode  int       // ExecMainCode: 1 exited, 2 killed, 3 dumped; 0 never ran
	Exit      int       // ExecMainStatus: the exit status, or the signal that killed it
	Result    string    // why it last stopped or failed: success, exit-code, signal, timeout...
}

// Finished is a oneshot that has run and stays active (RemainAfterExit):
// nothing of it runs.
func (s UnitState) Finished() bool { return s.ActiveState == "active" && s.SubState == "exited" }

// Running is an active unit that is not a finished oneshot.
func (s UnitState) Running() bool { return s.Active() && !s.Finished() }

// Known is a unit systemd has a file for.
func (s UnitState) Known() bool { return s.LoadState != "not-found" }

// Restarting is a unit waiting out an automatic restart: its program is
// not running. One that restarted and is starting again (its healthcheck
// probe running in start-post, a oneshot's run) is starting, not this.
func (s UnitState) Restarting() bool {
	return s.SubState == "auto-restart" || s.SubState == "auto-restart-queued"
}

// Active is a unit running, starting or reloading.
func (s UnitState) Active() bool {
	return s.ActiveState == "active" || s.ActiveState == "activating" || s.ActiveState == "reloading"
}

// StartedBefore reports whether the current run began before t.
func (s UnitState) StartedBefore(t time.Time) bool {
	return !s.Started.IsZero() && s.Started.Before(t)
}

// showTime is what --timestamp=us+utc prints (systemd 248 and later).
// Microseconds, not the default whole seconds: an up that writes a file in
// the same second an earlier up restarted the unit must still see the run
// as older than the file. In whole seconds the two compare equal, and the
// change reads as applied.
const showTime = "Mon 2006-01-02 15:04:05.000000 MST"

// States asks systemctl about every unit at once.
func (m *Manager) States(units []string) (map[string]UnitState, error) {
	args := append([]string{"show", "--timestamp=us+utc", "-p", "Id,LoadState,ActiveState,SubState,UnitFileState,FragmentPath,InactiveExitTimestamp,ExecMainStartTimestamp,NRestarts,NextElapseUSecRealtime,ExecMainCode,ExecMainStatus,Result"}, units...)
	out, err := m.Cmd("systemctl", args...).Output()
	if err != nil {
		return nil, CmdErr("systemctl show", err)
	}
	return parseStates(string(out))
}

func parseStates(out string) (map[string]UnitState, error) {
	states := map[string]UnitState{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var s UnitState
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "Id":
				s.Id = v
			case "LoadState":
				s.LoadState = v
			case "ActiveState":
				s.ActiveState = v
			case "SubState":
				s.SubState = v
			case "UnitFileState":
				s.UnitFileState = v
			case "FragmentPath":
				s.FragmentPath = v
			case "InactiveExitTimestamp", "ExecMainStartTimestamp":
				if v == "" || v == "n/a" { // never started: 255 prints nothing, 249 n/a
					continue
				}
				t, err := time.Parse(showTime, v)
				if err != nil {
					return nil, fmt.Errorf("systemctl show: %s=%s: %v", k, v, err)
				}
				if k == "InactiveExitTimestamp" {
					s.Started = t
				} else {
					s.Ran = t
				}
			case "NRestarts":
				s.NRestarts, _ = strconv.Atoi(v)
			case "ExecMainCode":
				s.ExitCode, _ = strconv.Atoi(v)
			case "ExecMainStatus":
				s.Exit, _ = strconv.Atoi(v)
			case "Result":
				s.Result = v
			case "NextElapseUSecRealtime":
				// Printed in local time without microseconds, whatever
				// --timestamp says; shown, never compared, so kept as text.
				if v != "n/a" {
					s.NextRun = v
				}
			}
		}
		if s.Id != "" {
			states[s.Id] = s
		}
	}
	return states, nil
}

// ProbeExits reads the last exit status of each unit's healthcheck probe,
// the ExecStartPost= isProbe recognises: 0 passed, more failed, -1 when
// none ran. systemd keeps it after the unit fails. Any other
// ExecStartPost= (the author's, through unit:) is not the probe's verdict
// and is skipped.
func (m *Manager) ProbeExits(units []string, isProbe func(execStartPost string) bool) (map[string]int, error) {
	out, err := m.Cmd("systemctl", append([]string{"show", "-p", "Id,ExecStartPost"}, units...)...).Output()
	if err != nil {
		return nil, CmdErr("systemctl show", err)
	}
	return parseProbeExits(string(out), isProbe), nil
}

func parseProbeExits(out string, isProbe func(execStartPost string) bool) map[string]int {
	exits := map[string]int{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		id, exit := "", -1
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "Id":
				id = v
			case "ExecStartPost":
				// "{ path=… ; argv[]=… ; … ; code=exited ; status=1 }"; a
				// killed probe reads code=killed, one that never ran code=(null).
				if !isProbe(v) {
					continue
				}
				code := fieldOf(v, "code=")
				status, _ := strconv.Atoi(strings.SplitN(fieldOf(v, "status="), "/", 2)[0])
				switch {
				case code == "exited" && status > exit:
					exit = status
				case code == "killed" || code == "dumped":
					exit = max(exit, 1)
				}
			}
		}
		if id != "" {
			exits[id] = exit
		}
	}
	return exits
}

// fieldOf returns the value after key in systemctl's "a=1 ; b=2" lists.
func fieldOf(s, key string) string {
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	v := s[i+len(key):]
	if j := strings.IndexAny(v, " ;}"); j >= 0 {
		v = v[:j]
	}
	return v
}
