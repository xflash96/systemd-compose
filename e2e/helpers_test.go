//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// check runs one check as a subtest named by its label, which says what
// must hold; err says what was seen instead, nil when it holds. A label
// that starts with spaces continues the check before it.
func check(t *testing.T, label string, err error) {
	t.Helper()
	t.Run(strings.TrimLeft(label, " "), func(t *testing.T) {
		t.Helper()
		if err != nil {
			t.Error(err)
		}
	})
}

// result is what a command did.
type result struct {
	cmd      string
	out, all string // stdout; stdout and stderr as written
	code     int
}

func (r result) fail(format string, args ...any) error {
	return fmt.Errorf("%s: %s (exit %d)\n%s", r.cmd, fmt.Sprintf(format, args...), r.code, r.all)
}

// ok holds when the command exited 0.
func (r result) ok() error {
	if r.code != 0 {
		return r.fail("want exit 0")
	}
	return nil
}

// fails holds when the command exited nonzero.
func (r result) fails() error {
	if r.code == 0 {
		return r.fail("want a nonzero exit")
	}
	return nil
}

// exits holds when the command exited with code.
func (r result) exits(code int) error {
	if r.code != code {
		return r.fail("want exit %d", code)
	}
	return nil
}

// shows holds when the command exited 0 and its stdout matches pattern, a
// regexp whose ^ and $ match at each line.
func (r result) shows(pattern string) error {
	if r.code != 0 {
		return r.fail("want exit 0")
	}
	if !match(pattern, r.out) {
		return r.fail("stdout does not match %q", pattern)
	}
	return nil
}

// says holds when stdout or stderr matches pattern, whatever the exit.
func (r result) says(pattern string) error {
	if !match(pattern, r.all) {
		return r.fail("output does not match %q", pattern)
	}
	return nil
}

// lacks holds when neither stdout nor stderr matches pattern.
func (r result) lacks(pattern string) error {
	if match(pattern, r.all) {
		return r.fail("output matches %q", pattern)
	}
	return nil
}

func match(pattern, s string) bool {
	return regexp.MustCompile("(?m)" + pattern).MatchString(s)
}

// run runs name with args in dir, with env added to this process's.
func run(dir string, env []string, name string, args ...string) result {
	c := exec.Command(name, args...)
	c.Dir, c.Env = dir, append(os.Environ(), env...)
	var out bytes.Buffer
	var all lockedBuffer
	c.Stdout, c.Stderr = &mux{&out, &all}, &all
	err := c.Run()
	r := result{cmd: strings.Join(append([]string{filepath.Base(name)}, args...), " "), out: out.String(), all: all.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		r.code, r.all = -1, err.Error()
	}
	return r
}

// mux writes stdout to its own buffer and to the one stderr shares.
type mux struct {
	out *bytes.Buffer
	all *lockedBuffer
}

func (m *mux) Write(p []byte) (int, error) {
	m.out.Write(p)
	return m.all.Write(p)
}

// lockedBuffer is a buffer two goroutines write: exec copies stdout and
// stderr each in its own.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// systemctl runs systemctl --user.
func systemctl(args ...string) result {
	return run("", nil, "systemctl", append([]string{"--user"}, args...)...)
}

// project is a throwaway project: a directory with a yaml, and a name.
type project struct {
	t    *testing.T
	dir  string
	name string
}

// newProject makes a project in a new directory named dir, under the
// test's temporary directory, and takes it down when the test ends. Its
// name is the tests' prefix, then "_" and suffix when there is one; in
// yaml, NAME and DIR stand for the name and the directory.
func newProject(t *testing.T, dir, suffix, yaml string) *project {
	t.Helper()
	p := &project{t: t, dir: filepath.Join(t.TempDir(), dir), name: prefix}
	if suffix != "" {
		p.name += "_" + suffix
	}
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if yaml != "" {
		p.write(yaml)
	}
	t.Cleanup(func() { p.sc("down") })
	return p
}

// write replaces the project's yaml; NAME and DIR stand for its name and
// its directory.
func (p *project) write(yaml string) {
	p.t.Helper()
	p.file("systemd-compose.yaml", strings.NewReplacer("NAME", p.name, "DIR", p.dir).Replace(yaml))
}

// file writes a file in the project's directory.
func (p *project) file(name, text string) {
	p.t.Helper()
	if err := os.WriteFile(p.path(name), []byte(text), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// path is a path in the project's directory.
func (p *project) path(name string) string { return filepath.Join(p.dir, name) }

// sc runs the binary under test in the project's directory.
func (p *project) sc(args ...string) result { return run(p.dir, nil, sc, args...) }

// escaped is the project's name as its units spell it: each - as \x2d.
func (p *project) escaped() string { return strings.ReplaceAll(p.name, "-", `\x2d`) }

// unit is the project's unit for a service and a suffix: unit("web",
// ".service") is NAME-web.service.
func (p *project) unit(service, suffix string) string { return p.escaped() + "-" + service + suffix }

// noChange holds when up (or up --dry-run) exited 0 and every row of its
// plan for the project named name reads unchanged (row grammar: printPlan
// in internal/project/up.go).
func noChange(r result, name string) error {
	if err := r.ok(); err != nil {
		return err
	}
	rows := 0
	for _, row := range strings.Split(r.out, "\n") {
		if match(`^  `+name+`[-.]`, row) {
			rows++
			if !match(`^  `+name+`[-.]\S* +unchanged `, row) {
				return r.fail("a row is not unchanged: %q", row)
			}
		}
	}
	if rows == 0 {
		return r.fail("no plan rows")
	}
	return nil
}

// registered counts the project's entries in the user manager's unit
// directory: copies, or links under registration: link.
func (p *project) registered() int {
	entries, _ := os.ReadDir(unitDir)
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), p.escaped()+"-") || strings.HasPrefix(e.Name(), p.escaped()+".") {
			n++
		}
	}
	return n
}

// active holds when the unit is active.
func active(unit string) error {
	if r := systemctl("is-active", unit); strings.TrimSpace(r.out) != "active" {
		return fmt.Errorf("%s is %s, want active", unit, strings.TrimSpace(r.out))
	}
	return nil
}

// inactive holds when the unit is not active.
func inactive(unit string) error {
	if r := systemctl("is-active", unit); strings.TrimSpace(r.out) == "active" {
		return fmt.Errorf("%s is active", unit)
	}
	return nil
}

// property is one property of a unit, as systemctl show prints it.
func property(unit, name string) string {
	return strings.TrimSpace(systemctl("show", "-p", name, "--value", unit).out)
}

// mainPID is the unit's main process.
func mainPID(unit string) string { return property(unit, "MainPID") }

// equal holds when got is want.
func equal[T comparable](what string, got, want T) error {
	if got != want {
		return fmt.Errorf("%s is %v, want %v", what, got, want)
	}
	return nil
}

// that holds when cond does; what says what was wanted.
func that(cond bool, format string, args ...any) error {
	if !cond {
		return fmt.Errorf(format, args...)
	}
	return nil
}

// exists holds when path exists.
func exists(path string) error {
	_, err := os.Lstat(path)
	return err
}

// missing holds when nothing is at path.
func missing(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s exists", path)
	}
	return nil
}

// fileMatches holds when the file at path matches pattern.
func fileMatches(path, pattern string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !match(pattern, string(data)) {
		return fmt.Errorf("%s does not match %q:\n%s", path, pattern, data)
	}
	return nil
}

// line is line n (from 1) of the file at path, "" when there is none.
func line(path string, n int) string {
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	if n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// cpuDelegated reports whether the user manager has the cpu controller.
func cpuDelegated() bool {
	data, err := os.ReadFile("/sys/fs/cgroup" + strings.TrimSpace(systemctl("show", "-p", "ControlGroup", "--value").out) + "/cgroup.controllers")
	return err == nil && strings.Contains(" "+strings.TrimSpace(string(data))+" ", " cpu ")
}

// runtimeDir is the user manager's runtime directory, which %t names.
func runtimeDir() string {
	for _, l := range strings.Split(systemctl("show-environment").out, "\n") {
		if v, ok := strings.CutPrefix(l, "XDG_RUNTIME_DIR="); ok {
			return v
		}
	}
	return "/run/user/" + strconv.Itoa(os.Getuid())
}

// background starts a command. wait waits for it; sofar is its output so
// far, stdout and stderr as written, while it still runs.
func background(dir string, name string, args ...string) (wait func() result, sofar func() string) {
	c := exec.Command(name, args...)
	c.Dir = dir
	var out bytes.Buffer
	var all lockedBuffer
	c.Stdout, c.Stderr = &mux{&out, &all}, &all
	if err := c.Start(); err != nil {
		return func() result { return result{cmd: name, all: err.Error(), code: -1} }, func() string { return "" }
	}
	return func() result {
		err := c.Wait()
		r := result{cmd: strings.Join(append([]string{filepath.Base(name)}, args...), " "), out: out.String(), all: all.String()}
		if ee, ok := err.(*exec.ExitError); ok {
			r.code = ee.ExitCode()
		}
		return r
	}, all.String
}

// freePort is a TCP port nothing listens on now.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// atTerminal runs a shell command line under script(1), so it has a
// terminal, and feeds the terminal input: each step waits, then writes.
// It returns the transcript.
func atTerminal(dir, cmdline string, steps ...input) string {
	c := exec.Command("script", "-qec", cmdline, "/dev/null")
	c.Dir = dir
	var all bytes.Buffer
	c.Stdout, c.Stderr = &all, &all
	in, err := c.StdinPipe()
	if err != nil {
		return err.Error()
	}
	if err := c.Start(); err != nil {
		return err.Error()
	}
	for _, s := range steps {
		time.Sleep(s.after)
		in.Write([]byte(s.text))
	}
	in.Close()
	c.Wait()
	return all.String()
}

// input is what to type at a terminal, and when.
type input struct {
	after time.Duration
	text  string
}
