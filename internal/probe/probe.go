// Package probe is the healthcheck: the hidden `probe` verb a rendered
// unit runs as ExecStartPost=. It runs the healthcheck's test until it
// passes or start_period is over, so the service counts as started only
// once it is ready. Then it leaves a watch behind, in the service's
// cgroup, that runs the test every interval for as long as the service
// runs, as compose's healthcheck does, and keeps what it found for ps.
package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// The probe's exits that are its own rather than the test's: the test timed
// out (as timeout(1) says it), or the program it probes has exited, when
// waiting out start_period would only blame the healthcheck.
const (
	TimedOut = 124
	MainGone = 125
)

// Verb is the hidden verb that runs Run.
const Verb = "probe"

// Args is the probe's command line after this program's path, as render
// writes it into ExecStartPost=: the verb, the healthcheck's settings, then
// the test.
func Args(h *config.Healthcheck, test []string) []string {
	return slices.Concat([]string{Verb, "--interval", h.Interval, "--timeout", h.Timeout, "--retries", strconv.Itoa(h.Retries),
		"--start-period", h.StartPeriod, "--start-interval", h.StartInterval, "--"}, test)
}

// IsProbe reports whether an ExecStartPost=, as systemctl show prints it,
// is a command line Args wrote: its argv[] has Args' first two words after
// the program's path. The author's own, through unit:, is not.
func IsProbe(execStartPost string) bool {
	return strings.Contains(execStartPost, " "+strings.Join(Args(&config.Healthcheck{}, nil)[:2], " ")+" ")
}

// Run is the probe verb: probe [--watch] [--interval D] [--timeout D]
// [--retries N] [--start-period D] [--start-interval D] -- TEST...
func Run(args []string) error {
	interval, timeout, startPeriod, startInterval := config.Default("healthcheck.interval"), config.Default("healthcheck.timeout"), config.Default("healthcheck.start_period"), config.Default("healthcheck.start_interval")
	retries := config.Default("healthcheck.retries")
	flags := map[string]*string{"--interval": &interval, "--timeout": &timeout, "--retries": &retries, "--start-period": &startPeriod, "--start-interval": &startInterval}
	watching := false
	var argv []string
	for i := 0; i < len(args); i++ {
		dst, isFlag := flags[args[i]]
		switch {
		case args[i] == "--watch":
			watching = true
		case isFlag && i+1 >= len(args):
			return fmt.Errorf("probe: %s needs a value", args[i])
		case isFlag:
			*dst = args[i+1]
			i++
		case args[i] == "--":
			argv = args[i+1:]
			i = len(args)
		default:
			return fmt.Errorf("probe: unexpected %q", args[i])
		}
	}
	if len(argv) == 0 {
		return fmt.Errorf("probe: no test command after --")
	}
	var secs [4]int
	for i, v := range []string{interval, timeout, startPeriod, startInterval} {
		n, err := config.Seconds(v)
		if err != nil {
			return err
		}
		secs[i] = n
	}
	iv, to, sp, si := secs[0], secs[1], secs[2], secs[3]
	n, err := strconv.Atoi(retries)
	if err != nil || n < 1 {
		return fmt.Errorf("probe: --retries %q: want a positive integer", retries)
	}
	// systemd gives an ExecStartPost= the main process's pid: once that is
	// gone nothing can become healthy, and start_period need not run out.
	// It is watched during a test too, not only between tests: a test with
	// a long timeout: would otherwise keep a dead program's up waiting.
	mainPID, _ := strconv.Atoi(os.Getenv("MAINPID"))
	gone := func() bool { return mainPID > 0 && syscall.Kill(mainPID, 0) == syscall.ESRCH }
	t := test{argv: argv, timeout: time.Duration(to) * time.Second, gone: gone}
	if watching {
		return t.watch(time.Duration(iv)*time.Second, n, statePath(os.Getenv("INVOCATION_ID")))
	}

	deadline := time.Now().Add(time.Duration(sp) * time.Second)
	mainGone := func() error {
		fmt.Fprintf(os.Stderr, "probe: the program (pid %d) exited before it became healthy\n", mainPID)
		return systemd.ExitError{Code: MainGone}
	}
	attempt := 0
	for {
		if gone() {
			return mainGone()
		}
		attempt++
		r := t.run()
		switch {
		case r.mainGone:
			return mainGone()
		case r.notRun: // no program to run: no attempt will be different
			return fmt.Errorf("probe: %v", r.err)
		}
		if r.err == nil {
			fmt.Printf("probe: healthy after %d attempt(s)\n", attempt)
			watchAfter(args, statePath(os.Getenv("INVOCATION_ID")))
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "probe: not healthy within %s (%d attempts, last: %s)\n", startPeriod, attempt, r)
			// The test's own last exit becomes the probe's, which systemd
			// keeps (ProbeExits): a curl 7 stays a 7. A test's own 124 or
			// 125 (a timeout(1) wrapper) reads as 1, and the line above
			// keeps the real one.
			var ee *exec.ExitError
			switch {
			case r.timedOut:
				return systemd.ExitError{Code: TimedOut}
			case errors.As(r.err, &ee) && ee.ExitCode() > 0 && ee.ExitCode() != TimedOut && ee.ExitCode() != MainGone:
				return systemd.ExitError{Code: ee.ExitCode()}
			}
			return systemd.ExitError{Code: 1}
		}
		time.Sleep(time.Duration(si) * time.Second)
	}
}

// watchAfter starts the watch once the start's test has passed: this
// program again, with --watch, in a session of its own, writing to the
// journal as this one does. It stays in the service's cgroup, so systemd
// ends it with the service: on a stop, a restart, or the program's exit.
// A watch that cannot start leaves the service started; ps says it is
// unchecked.
func watchAfter(args []string, state string) {
	fail := func(err error) { fmt.Fprintf(os.Stderr, "probe: the checks after the start did not start: %v\n", err) }
	exe, err := os.Executable()
	if err != nil {
		fail(err)
		return
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		fail(err)
		return
	}
	defer null.Close()
	p, err := os.StartProcess(exe, slices.Concat([]string{exe, Verb, "--watch"}, args), &os.ProcAttr{
		Files: []*os.File{null, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		fail(err)
		return
	}
	if state != "" {
		// written here, not by the watch: ps right after the start finds it
		prune(filepath.Dir(state))
		if err := writeState(state, State{PID: p.Pid, Start: procStart(p.Pid), Healthy: true, Since: time.Now()}); err != nil {
			fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		}
	}
	p.Release()
}

// watch runs the test every interval for as long as the service runs:
// retries failed in a row make it unhealthy, and one pass healthy again,
// each said once in the journal. Nothing else acts on it.
func (t test) watch(interval time.Duration, retries int, state string) error {
	st := State{PID: os.Getpid(), Start: procStart(os.Getpid()), Healthy: true, Since: time.Now()}
	// Every signal is the watch's to take: one sent to the service reaches
	// its whole cgroup (systemd-compose kill -s HUP), and must not end the
	// checks, or dump a Go trace into the service's log (QUIT), while the
	// program runs on. TERM and INT, a stop's, end the watch once the
	// program has gone.
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs)
	defer signal.Stop(sigs)
	// ended takes s and the signals waiting behind it, and says whether the
	// watch ends: the program gone after a TERM or INT. Any other signal is
	// ignored; one that ended a test makes that check a failed one, which
	// retries absorbs.
	ended := func(s os.Signal) bool {
		term := false
		for s != nil {
			term = term || s == syscall.SIGTERM || s == syscall.SIGINT
			select {
			case s = <-sigs:
			default:
				s = nil
			}
		}
		for until := time.Now().Add(stopGrace); term && time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
			if t.gone() {
				return true
			}
		}
		return false
	}
	defer func() {
		if state != "" {
			os.Remove(state) // the state is the run's, and goes with it
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		// the program's exit ends the watch within a second, even where
		// systemd leaves the watch running (KillMode=process)
		for next := time.After(interval); next != nil; {
			select {
			case s := <-sigs:
				if ended(s) {
					return nil
				}
			case <-tick.C:
				if t.gone() {
					return nil
				}
			case <-next:
				next = nil
			}
		}
		r := t.run()
		select {
		case s := <-sigs: // a stop's TERM reaches the test too: no verdict then
			if ended(s) {
				return nil
			}
		default:
		}
		if r.mainGone {
			return nil
		}
		now := time.Now()
		switch {
		case r.err == nil:
			if !st.Healthy {
				fmt.Printf("probe: healthy again, after %d failed checks\n", st.Failed)
				st.Healthy, st.Since = true, now
			}
			st.Failed, st.Last = 0, ""
		default:
			st.Failed++
			st.Last = r.String()
			if st.Healthy && st.Failed >= retries {
				fmt.Fprintf(os.Stderr, "probe: unhealthy: %d checks failed in a row, the last: %s\n", st.Failed, st.Last)
				st.Healthy, st.Since = false, now
			}
		}
		if state != "" {
			if err := writeState(state, st); err != nil {
				fmt.Fprintf(os.Stderr, "probe: %v\n", err)
			}
		}
	}
}

// stopGrace is how long the watch waits, after a TERM or INT, for the
// program to end: a stop's ends it, and one sent by hand to a program that
// takes it may not.
const stopGrace = 5 * time.Second

// test is the healthcheck's test, as one check runs it.
type test struct {
	argv    []string
	timeout time.Duration
	gone    func() bool // the service's program has exited
}

// result is how one check went.
type result struct {
	err      error // nil: it passed
	timedOut bool  // the kill at the timeout ended it
	mainGone bool  // the service's program exited while it ran
	notRun   bool  // its program could not be started
	out      string
}

// String is the check's failure and the tail of its output, on one line.
func (r result) String() string {
	s := fmt.Sprint(r.err)
	if out := strings.Join(strings.Fields(r.out), " "); out != "" {
		s += "; its output: " + out
	}
	return s
}

// run runs the test once. Its output is kept only to say why it failed,
// and only its tail. The test runs in a process group of its own, and a
// kill takes the group: a shell test's children would otherwise hold the
// output pipe, and the wait for the test, open past its timeout.
// WaitDelay bounds that wait all the same.
func (t test) run() result {
	out := &tail{max: 400}
	c := exec.Command(t.argv[0], t.argv[1:]...)
	c.Stdout, c.Stderr = out, out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.WaitDelay = time.Second
	if err := c.Start(); err != nil {
		return result{err: err, notRun: true}
	}
	kill := func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	var r result
	limit := time.After(t.timeout)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case r.err = <-done:
			break wait
		case <-limit:
			kill()
			<-done // reap it; a killed test must not linger as a zombie
			r.err = fmt.Errorf("timed out after %s", t.timeout)
			r.timedOut = true
			break wait
		case <-tick.C:
			if t.gone() {
				kill()
				<-done
				return result{mainGone: true}
			}
		}
	}
	// A test that ended on its own is judged by its exit, though a child it
	// left held the output open past it (Wait then reports its WaitDelay),
	// even past the limit: only a test the kill ended timed out.
	if ps := c.ProcessState; ps != nil && ps.Exited() {
		r.err, r.timedOut = nil, false
		if !ps.Success() {
			r.err = &exec.ExitError{ProcessState: ps}
		}
	}
	r.out = out.String()
	return r
}

// State is what the checks after the start last found, for ps.
type State struct {
	PID     int       `json:"pid"`   // the watch's
	Start   string    `json:"start"` // and when it started (procStart): a pid alone may be reused
	Healthy bool      `json:"healthy"`
	Failed  int       `json:"failed"` // checks failed in a row
	Last    string    `json:"last"`   // the last failed check: its error and output
	Since   time.Time `json:"since"`  // when Healthy last changed
}

// statePath is where the watch of the service run invocation keeps its
// State: the user's runtime directory, which a reboot empties. "" when
// the invocation is unknown.
func statePath(invocation string) string {
	if invocation == "" || strings.ContainsRune(invocation, '/') {
		return ""
	}
	return filepath.Join(systemd.RuntimeDir(), "health", invocation)
}

// ReadState reads the State of the service run invocation, if its watch
// is still there to keep it.
func ReadState(invocation string) (State, bool) {
	var st State
	path := statePath(invocation)
	if path == "" {
		return st, false
	}
	return live(path)
}

// live reads the State at path, and whether its watch still runs.
func live(path string) (State, bool) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, st.PID > 0 && st.Start != "" && procStart(st.PID) == st.Start
}

// prune removes the states in dir whose watch has gone without taking its
// state along: one a SIGKILL ended.
func prune(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if _, ok := live(path); !ok && !strings.HasPrefix(e.Name(), ".") {
			os.Remove(path)
		}
	}
}

// procStart is when process pid started, in clock ticks after boot, as
// /proc/PID/stat says; "" for no such process. With the pid, it names one
// process for good.
func procStart(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(data)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:]) // the name may hold anything
	if len(f) < 20 {
		return ""
	}
	return f[19] // field 22; f[0] is field 3
}

// writeState replaces the file at path with st, whole.
func writeState(path string, st State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return systemd.ReplaceFile(path, data)
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	buf []byte
}

// Write keeps p, dropping what is older than the last max bytes.
func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte("..."), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

// String is what was kept, with "..." in front when some was dropped.
func (t *tail) String() string { return string(t.buf) }
