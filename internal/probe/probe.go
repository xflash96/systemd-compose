// Package probe is the healthcheck: the hidden `probe` verb a rendered
// unit runs as ExecStartPost=. It runs the healthcheck's test until it
// passes or start_period is over, so the service counts as started only
// once it is ready.
package probe

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
// writes it into ExecStartPost=: the verb, the healthcheck's spans, then
// the test.
func Args(h *config.Healthcheck, test []string) []string {
	return slices.Concat([]string{Verb, "--interval", h.Interval, "--timeout", h.Timeout, "--start-period", h.StartPeriod, "--"}, test)
}

// IsProbe reports whether an ExecStartPost=, as systemctl show prints it,
// is a command line Args wrote: its argv[] has Args' first two words after
// the program's path. The author's own, through unit:, is not.
func IsProbe(execStartPost string) bool {
	return strings.Contains(execStartPost, " "+strings.Join(Args(&config.Healthcheck{}, nil)[:2], " ")+" ")
}

// Run is the probe verb: probe [--interval D] [--timeout D] [--start-period D] -- TEST...
func Run(args []string) error {
	interval, timeout, startPeriod := config.Default("healthcheck.interval"), config.Default("healthcheck.timeout"), config.Default("healthcheck.start_period")
	flags := map[string]*string{"--interval": &interval, "--timeout": &timeout, "--start-period": &startPeriod}
	var argv []string
	for i := 0; i < len(args); i++ {
		dst, isFlag := flags[args[i]]
		switch {
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
	iv, err := config.Seconds(interval)
	if err != nil {
		return err
	}
	to, err := config.Seconds(timeout)
	if err != nil {
		return err
	}
	sp, err := config.Seconds(startPeriod)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(sp) * time.Second)
	// systemd gives an ExecStartPost= the main process's pid: once that is
	// gone nothing can become healthy, and start_period need not run out.
	// It is watched during a test too, not only between tests: a test with
	// a long timeout: would otherwise keep a dead program's up waiting.
	mainPID, _ := strconv.Atoi(os.Getenv("MAINPID"))
	gone := func() bool { return mainPID > 0 && syscall.Kill(mainPID, 0) == syscall.ESRCH }
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
		// The test's output goes to the journal only for the attempt that
		// ends the wait: every attempt's would bury it. Only its tail is
		// kept. The test runs in a process group of its own, and a kill
		// takes the group: a shell test's children would otherwise hold
		// the output pipe, and the wait for the test, open past its
		// timeout. WaitDelay bounds that wait all the same.
		out := &tail{max: 400}
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdout, c.Stderr = out, out
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.WaitDelay = time.Second
		kill := func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
		done := make(chan error, 1)
		if err := c.Start(); err != nil {
			return fmt.Errorf("probe: %v", err)
		}
		go func() { done <- c.Wait() }()
		var perr error
		timedOut := false
		limit := time.After(time.Duration(to) * time.Second)
		tick := time.NewTicker(250 * time.Millisecond)
	wait:
		for {
			select {
			case perr = <-done:
				break wait
			case <-limit:
				kill()
				<-done // reap it; a killed probe must not linger as a zombie
				perr = fmt.Errorf("timed out after %s", timeout)
				timedOut = true
				break wait
			case <-tick.C:
				if gone() {
					kill()
					<-done
					tick.Stop()
					return mainGone()
				}
			}
		}
		tick.Stop()
		// A test that ended on its own is judged by its exit, though a
		// child it left held the output open past it (Wait then reports its
		// WaitDelay), even past the limit: only a test the kill ended timed
		// out.
		if ps := c.ProcessState; ps != nil && ps.Exited() {
			perr, timedOut = nil, false
			if !ps.Success() {
				perr = &exec.ExitError{ProcessState: ps}
			}
		}
		if perr == nil {
			fmt.Printf("probe: healthy after %d attempt(s)\n", attempt)
			return nil
		}
		if time.Now().After(deadline) {
			last := strings.TrimSpace(out.String())
			if last != "" {
				last = "; its output: " + last
			}
			fmt.Fprintf(os.Stderr, "probe: not healthy within %s (%d attempts, last: %v%s)\n", startPeriod, attempt, perr, last)
			// The test's own last exit becomes the probe's, which systemd
			// keeps (ProbeExits): a curl 7 stays a 7. A test's own 124 or
			// 125 (a timeout(1) wrapper) reads as 1, and the line above
			// keeps the real one.
			var ee *exec.ExitError
			switch {
			case timedOut:
				return systemd.ExitError{Code: TimedOut}
			case errors.As(perr, &ee) && ee.ExitCode() > 0 && ee.ExitCode() != TimedOut && ee.ExitCode() != MainGone:
				return systemd.ExitError{Code: ee.ExitCode()}
			}
			return systemd.ExitError{Code: 1}
		}
		time.Sleep(time.Duration(iv) * time.Second)
	}
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
