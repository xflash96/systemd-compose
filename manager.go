// manager.go — the seam to systemd. Everything here shells out to
// systemctl, journalctl, systemd-analyze and loginctl: systemd stays the one
// owner of the truth, and this file only asks and tells.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Manager struct {
	User bool
}

// exitError carries a child's exit status to main so it becomes ours.
type exitError struct{ code int }

func (e exitError) Error() string { return "exit " + strconv.Itoa(e.code) }

func (m *Manager) scope() string {
	if m.User {
		return "--user"
	}
	return "--system"
}

func (m *Manager) ScopeName() string {
	if m.User {
		return "user instance"
	}
	return "system instance"
}

func (m *Manager) cmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, append([]string{m.scope()}, args...)...)
}

// run streams a systemctl call's output through and returns its failure as
// an error carrying the exit code. Nothing is swallowed.
func (m *Manager) run(args ...string) error {
	c := m.cmd("systemctl", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return asExit(c.Run())
}

func asExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitError{ee.ExitCode()}
	}
	return err
}

// Exec replaces this process with the command: pass-through verbs keep
// their pager, their tty and their exit code.
func Exec(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{name}, args...), os.Environ())
}

func (m *Manager) ExecSystemctl(args ...string) error {
	return Exec("systemctl", append([]string{m.scope()}, args...)...)
}

func (m *Manager) ExecJournalctl(args ...string) error {
	return Exec("journalctl", append([]string{m.scope()}, args...)...)
}

// ---- state ----------------------------------------------------------------

type UnitState struct {
	Id, LoadState, ActiveState, SubState, UnitFileState string

	Started time.Time // InactiveExitTimestamp: when the current run began; zero if never
}

func (s UnitState) Known() bool { return s.LoadState != "not-found" }
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
// as older than the file (whole seconds would call it applied).
const showTime = "Mon 2006-01-02 15:04:05.000000 MST"

// States asks systemctl about every unit at once.
func (m *Manager) States(units []string) (map[string]UnitState, error) {
	args := append([]string{"show", "--timestamp=us+utc", "-p", "Id,LoadState,ActiveState,SubState,UnitFileState,InactiveExitTimestamp"}, units...)
	out, err := m.cmd("systemctl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %v", asExit(err))
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
			case "InactiveExitTimestamp":
				if v == "" {
					continue
				}
				t, err := time.Parse(showTime, v)
				if err != nil {
					return nil, fmt.Errorf("systemctl show: %s=%s: %v", k, v, err)
				}
				s.Started = t
			}
		}
		if s.Id != "" {
			states[s.Id] = s
		}
	}
	return states, nil
}

// ProbeExits reads each unit's last ExecStartPost= exit status, which for a
// service with a healthcheck is the probe's verdict at start: 0 passed, more
// failed, -1 when none ran. systemd keeps it after the unit fails.
func (m *Manager) ProbeExits(units []string) (map[string]int, error) {
	out, err := m.cmd("systemctl", append([]string{"show", "-p", "Id,ExecStartPost"}, units...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %v", asExit(err))
	}
	return parseProbeExits(string(out)), nil
}

func parseProbeExits(out string) map[string]int {
	exits := map[string]int{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		id, exit := "", -1
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "Id":
				id = v
			case "ExecStartPost":
				// "{ path=… ; … ; code=exited ; status=1 }"; a killed probe
				// reads code=killed, one that never ran code=(null).
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

// UnitDir is where this instance's persistent unit links live.
func (m *Manager) UnitDir() (string, error) {
	if !m.User {
		return "/etc/systemd/system", nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// ---- verbs on units --------------------------------------------------------

func (m *Manager) Link(paths []string) error {
	return m.run(append([]string{"link", "--no-reload"}, paths...)...)
}
func (m *Manager) EnableNow(unit string) error { return m.run("enable", "--now", unit) }
func (m *Manager) Start(units []string) error  { return m.run(append([]string{"start"}, units...)...) }
func (m *Manager) TryRestart(units []string) error {
	return m.run(append([]string{"try-restart"}, units...)...)
}

// ResetFailed clears failed state on exactly these units; a name that is
// not loaded is not an error here, because "nothing to reset" is fine.
func (m *Manager) ResetFailed(units []string) error {
	if len(units) == 0 {
		return nil
	}
	c := m.cmd("systemctl", append([]string{"reset-failed"}, units...)...)
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
	c := m.cmd("systemd-analyze", append([]string{"verify"}, paths...)...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return strings.TrimSpace(out.String()), asExit(err)
}

// Linger reports whether the user manager outlives the login session.
func Linger() (bool, error) {
	out, err := exec.Command("loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger", "--value").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// CGroupPath is the cgroup path systemd-cgtop wants for a slice under this
// instance.
func (m *Manager) CGroupPath(slice string) string {
	if m.User {
		uid := strconv.Itoa(os.Getuid())
		return fmt.Sprintf("user.slice/user-%s.slice/user@%s.service/%s", uid, uid, slice)
	}
	return slice
}

// ---- files -----------------------------------------------------------------

// writeUnit is compare-then-swap, and an unchanged render is never
// rewritten. That is a contract, not a nicety: up's plan reads the file's
// mtime as the birth of the definition ("changed (not applied)"), so a
// rewrite of identical text would restart every running service on every
// up. TestWriteUnitKeepsMtime holds it.
func writeUnit(path, text string) error {
	old, err := os.ReadFile(path)
	switch {
	case err == nil && string(old) == text:
		return nil
	case err != nil && !os.IsNotExist(err):
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sweepEmptyWants removes .wants/.requires directories the disables left
// empty, so the unit directory does not accumulate husks.
func sweepEmptyWants(unitDir string) {
	entries, err := os.ReadDir(unitDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !(strings.HasSuffix(e.Name(), ".wants") || strings.HasSuffix(e.Name(), ".requires")) {
			continue
		}
		p := filepath.Join(unitDir, e.Name())
		if inner, err := os.ReadDir(p); err == nil && len(inner) == 0 {
			os.Remove(p)
		}
	}
}

// splitWords splits a command string into argv under simple quoting. The
// refusal list has already removed $, % and ;, so this is total.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	in := byte(0)
	has := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in != 0:
			if c == in {
				in = 0
			} else if c == '\\' && in == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			in = c
			has = true
		case c == ' ' || c == '\t':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
