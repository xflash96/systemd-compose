// Package systemd is the seam to systemd. It runs systemctl, journalctl,
// systemd-analyze and loginctl, and reads what they say: systemd stays the
// one owner of the truth about units.
package systemd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Manager is one systemd instance: the user's (User) or the system's.
type Manager struct {
	User bool
	Dir  string // UnitDir's answer, once asked
}

// ExitError carries a child's exit status to main so it becomes ours.
type ExitError struct{ Code int }

// Error is the code; a step that wraps an ExitError prints it.
func (e ExitError) Error() string { return "exit " + strconv.Itoa(e.Code) }

// scopeFlag is the systemctl flag that picks the instance.
func (m *Manager) scopeFlag() string {
	if m.User {
		return "--user"
	}
	return "--system"
}

// ScopeName is the instance in words, for messages.
func (m *Manager) ScopeName() string {
	if m.User {
		return "user instance"
	}
	return "system instance"
}

// Cmd is a command of systemd's (systemctl, journalctl, systemd-run...) on
// this instance.
func (m *Manager) Cmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, append([]string{m.scopeFlag()}, args...)...)
}

// Run streams a systemctl call's output through and returns its failure as
// an error carrying the exit code. Nothing is swallowed but systemd's
// advice to daemon-reload a unit whose file changed on disk: this program
// reloads when it writes, and when a file is gone (a git clean, a moved
// directory) it says so itself, while a reload cannot bring a file back.
func (m *Manager) Run(args ...string) error { return m.run(os.Stderr, args, nil, nil) }

// run runs systemctl on this instance, its stderr going to w but for
// systemd's changed-on-disk warning. It has the terminal's input, so
// systemctl can ask for a password on the system instance. A call still
// running after 2s calls note with the jobs then queued on watch (none
// with no watch), so a long wait says what it waits for.
func (m *Manager) run(w io.Writer, args, watch []string, note func(jobs map[string]string)) error {
	c := m.Cmd("systemctl", args...)
	filter := &staleFilter{w: w}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, filter
	err := c.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if jobs, jerr := m.Jobs(watch); jerr == nil && len(jobs) > 0 {
				note(jobs)
			}
			err = <-done
		}
	}
	filter.Flush()
	return AsExit(err)
}

var reStaleWarning = regexp.MustCompile(`^Warning: The unit file, source configuration file or drop-ins of \S+ changed on disk\. Run 'systemctl.* daemon-reload' to reload units\.$`)

// staleFilter passes lines through, but systemd's changed-on-disk warning.
type staleFilter struct {
	w   io.Writer
	buf []byte
}

func (f *staleFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := f.buf[:i+1]
		if !reStaleWarning.Match(bytes.TrimRight(line, "\n")) {
			if _, err := f.w.Write(line); err != nil {
				return len(p), err
			}
		}
		f.buf = f.buf[i+1:]
	}
}

// Flush writes what is left without a newline.
func (f *staleFilter) Flush() {
	if len(f.buf) > 0 && !reStaleWarning.Match(f.buf) {
		f.w.Write(f.buf)
	}
	f.buf = nil
}

// AsExit turns a command's nonzero exit into an ExitError with its code.
func AsExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ExitError{ee.ExitCode()}
	}
	return err
}

// CmdErr words the failure of a command whose output was captured: its own
// first line of stderr says why, which a bare exit code does not.
func CmdErr(name string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n"); msg != "" {
			return fmt.Errorf("%s: %s", name, msg)
		}
		return fmt.Errorf("%s: exit %d", name, ee.ExitCode())
	}
	return fmt.Errorf("%s: %v", name, err)
}

// Reachable checks that there is a manager to talk to. Without a login
// session or lingering, a user has no user manager, and every other call
// would fail with a message about something else.
func (m *Manager) Reachable() error {
	if _, err := m.Cmd("systemctl", "show", "-p", "Version").Output(); err != nil {
		if !m.User {
			return CmdErr("systemctl", err)
		}
		u := strconv.Itoa(os.Getuid())
		return fmt.Errorf("no systemd user manager to talk to (%v). It runs while this user is logged in, or always once lingering is on: loginctl enable-linger; outside a login session also set XDG_RUNTIME_DIR=/run/user/%s", CmdErr("systemctl --user", err), u)
	}
	return nil
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

// cgroupRoot is where the cgroup v2 hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// Controllers is the cgroup controllers the manager may hand its units,
// read from its own cgroup; nil when they cannot be read.
func (m *Manager) Controllers() []string {
	out, err := m.Cmd("systemctl", "show", "-p", "ControlGroup", "--value").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(cgroupRoot, strings.TrimSpace(string(out)), "cgroup.controllers"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}

// Exec replaces this process with a command of systemd's on this
// instance, as Cmd spells it.
func (m *Manager) Exec(name string, args ...string) error {
	return Exec(name, m.Cmd(name, args...).Args[1:]...)
}

// busProperty reads one property of a unit over D-Bus as JSON: the value
// systemd holds, specifiers expanded and nothing quoted, which no
// systemctl show line gives back exactly.
func (m *Manager) busProperty(unit, iface, prop string, into any) error {
	path := "/org/freedesktop/systemd1/unit/" + busEscape(unit)
	out, err := m.Cmd("busctl", "--json=short", "get-property", "org.freedesktop.systemd1", path, iface, prop).Output()
	if err != nil {
		return fmt.Errorf("busctl get-property %s %s: %v", unit, prop, AsExit(err))
	}
	var v struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("busctl %s %s: %v", unit, prop, err)
	}
	return json.Unmarshal(v.Data, into)
}

// busEscape spells a unit name as a D-Bus object path label: every byte
// but a letter, or a digit after the first, becomes _xx.
func busEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}

// ServiceRun is what systemd runs a loaded service with: its environment
// and its first ExecStart= argv, both as systemd expanded them.
func (m *Manager) ServiceRun(unit string) (env, argv []string, err error) {
	if err := m.busProperty(unit, "org.freedesktop.systemd1.Service", "Environment", &env); err != nil {
		return nil, nil, err
	}
	var execs [][]json.RawMessage // (path, argv, ignore, times...)
	if err := m.busProperty(unit, "org.freedesktop.systemd1.Service", "ExecStart", &execs); err != nil {
		return nil, nil, err
	}
	// A unit systemd cannot load answers every property with its empty
	// value, so an empty ExecStart= is the one signal that the environment
	// just read is nobody's rather than the service's.
	if len(execs) == 0 || len(execs[0]) < 2 {
		return nil, nil, fmt.Errorf("%s is registered but systemd has no ExecStart= for it; is its rendered file gone? (up writes it again)", unit)
	}
	if err := json.Unmarshal(execs[0][1], &argv); err != nil {
		return nil, nil, err
	}
	return env, argv, nil
}

// UnitDir is where this instance's persistent unit links live.
// The e2e tests spell the same rule for their link checks.
func (m *Manager) UnitDir() (string, error) {
	if !m.User {
		return "/etc/systemd/system", nil
	}
	if m.Dir != "" {
		return m.Dir, nil
	}
	// The manager's, not this process's: systemctl --user link writes where
	// the manager looks, which a shell's own XDG_CONFIG_HOME, or a HOME from
	// su -m or sudo without -i, need not be. Read from this process, every
	// unit would read as unregistered, and down would do nothing. Asked of
	// the manager when it answers; this process's environment otherwise.
	if out, err := m.Cmd("systemctl", "show", "-p", "UnitPath", "--value").Output(); err == nil {
		if d := configDir(string(out)); d != "" {
			m.Dir = d
			return d, nil
		}
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

// configDir is the user manager's persistent unit directory, from its
// UnitPath: the first entry is its control directory, <dir>.control, on
// 249 and 255 alike (the runtime one under /run follows).
func configDir(unitPath string) string {
	for _, d := range strings.Fields(unitPath) {
		if strings.HasSuffix(d, "/systemd/user.control") && !strings.HasPrefix(d, "/run/") {
			return strings.TrimSuffix(d, ".control")
		}
	}
	return ""
}

// RunQuiet is Run with systemctl's stderr kept for the caller: the verbs
// that report every unit's outcome themselves print it only when their own
// report says nothing about a unit (systemd's "Job for X failed ... See
// systemctl status X" names a unit and two commands the report already
// translates).
func (m *Manager) RunQuiet(args ...string) (string, error) {
	var stderr bytes.Buffer
	err := m.run(&stderr, args, nil, nil)
	return strings.TrimSpace(stderr.String()), err
}

// Version is systemd's version line: the first of systemctl --version.
func Version() (string, error) {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return first, nil
}

// Linger reports whether the user manager outlives the login session.
func Linger() (bool, error) {
	out, err := exec.Command("loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger", "--value").Output()
	if err != nil {
		// No logind user object (no session yet): logind keeps lingering
		// as a file, so the file answers.
		if u, uerr := user.Current(); uerr == nil && exists(filepath.Join("/var/lib/systemd/linger", u.Username)) {
			return true, nil
		}
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

// SplitWords splits a command string into argv under simple quoting. The
// refusal list has already removed % and ;, so this is total; a $ left by
// interpolation is an ordinary character here and runBuild writes it $$.
func SplitWords(s string) []string {
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
