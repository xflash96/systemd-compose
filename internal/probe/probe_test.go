package probe

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// What Args writes, Run reads after the verb, and IsProbe knows as
// systemctl show prints it; the author's own ExecStartPost= it does not.
func TestArgs_RunAndIsProbeReadWhatItWrites(t *testing.T) {
	h := &config.Healthcheck{Interval: "2s", Timeout: "5s", StartPeriod: "60s"}
	if args := Args(h, []string{"true"}); args[0] != Verb || Run(args[1:]) != nil {
		t.Errorf("Run(%v) did not pass", args)
	}
	ours := "{ path=/x ; argv[]=/x " + strings.Join(Args(h, []string{"curl", "-f", "localhost"}), " ") + " ; ignore_errors=no ; pid=9 ; code=exited ; status=1 }"
	theirs := "{ path=/bin/false ; argv[]=/bin/false ; ignore_errors=no ; pid=10 ; code=exited ; status=1 }"
	if !IsProbe(ours) || IsProbe(theirs) {
		t.Errorf("IsProbe(ours) = %v, IsProbe(theirs) = %v", IsProbe(ours), IsProbe(theirs))
	}
}

// The probe's exit for each way a test ends: a test whose child holds its
// output times out at timeout:; a test's own status is passed on, but its
// own 124 or 125 reads as 1, since those are the probe's; the program gone
// ends the wait at once.
func TestRun_ExitFollowsHowTheTestEnded(t *testing.T) {
	run := func(args ...string) (time.Duration, error) {
		t.Helper()
		begun := time.Now()
		err := Run(args)
		return time.Since(begun), err
	}
	code := func(err error) int {
		var ee systemd.ExitError
		if errors.As(err, &ee) {
			return ee.Code
		}
		return -1
	}
	if took, err := run("--interval", "1s", "--timeout", "1s", "--start-period", "1s", "--", "sh", "-c", "sleep 30 & wait"); code(err) != TimedOut || took > 6*time.Second {
		t.Errorf("a test whose child holds its output: %v after %v, want exit %d within its timeout", err, took, TimedOut)
	}
	if _, err := run("--interval", "1s", "--timeout", "5s", "--start-period", "1s", "--", "sh", "-c", "exit 124"); code(err) != 1 {
		t.Errorf("a test's own 124: %v, want exit 1", err)
	}
	if _, err := run("--interval", "1s", "--timeout", "5s", "--start-period", "1s", "--", "sh", "-c", "exit 7"); code(err) != 7 {
		t.Errorf("a test's own 7: %v, want exit 7", err)
	}
	// A test that exits but leaves a child holding its output: judged by
	// its own exit, not by the wait on the output, which would read as a
	// failure, or as a timeout when the child outlives timeout:.
	if _, err := run("--interval", "1s", "--timeout", "20s", "--start-period", "2s", "--", "sh", "-c", "sleep 3 & exit 0"); err != nil {
		t.Errorf("a test that passed and left a child: %v, want healthy", err)
	}
	if _, err := run("--interval", "1s", "--timeout", "1s", "--start-period", "2s", "--", "sh", "-c", "sleep 3 & exit 0"); err != nil {
		t.Errorf("a test that passed and left a child past timeout:: %v, want healthy", err)
	}
	if _, err := run("--interval", "1s", "--timeout", "20s", "--start-period", "1s", "--", "sh", "-c", "sleep 3 & exit 7"); code(err) != 7 {
		t.Errorf("a test that failed and left a child: %v, want exit 7", err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAINPID", strconv.Itoa(gone.Process.Pid))
	if took, err := run("--interval", "1s", "--timeout", "30s", "--start-period", "60s", "--", "sleep", "20"); code(err) != MainGone || took > 3*time.Second {
		t.Errorf("the program gone: %v after %v, want exit %d at once", err, took, MainGone)
	}
}

// A test that writes without pause, in a shell that keeps it as a child
// (dash does): its timeout must end the whole test, writer included, and
// the probe keeps only the output's tail. Otherwise the probe never
// returns and grows by about a gigabyte a second; TestMain's watchdog then
// stops this test at 512 MB within a fraction of a second.
func TestRun_EndsAChattyTestAtItsTimeout(t *testing.T) {
	begun := time.Now()
	err := Run([]string{"--interval", "1s", "--timeout", "1s", "--start-period", "1s", "--", "sh", "-c", "yes"})
	var ee systemd.ExitError
	if !errors.As(err, &ee) || ee.Code != TimedOut || time.Since(begun) > 5*time.Second {
		t.Errorf("a test that never stops writing: %v after %v, want exit %d within its timeout", err, time.Since(begun), TimedOut)
	}
}

// The probe keeps only the tail of a test's output.
func TestTail_KeepsOnlyTheLastBytes(t *testing.T) {
	w := &tail{max: 10}
	for range 1000 {
		w.Write([]byte("0123456789abcdef"))
	}
	if got := w.String(); len(got) > 13 || !strings.HasSuffix(got, "abcdef") {
		t.Errorf("tail = %q", got)
	}
}

func TestRun_RefusesBadFlags(t *testing.T) {
	for _, args := range [][]string{{"--interval"}, {"--timeout", "5s", "--start-period"}, {"--bogus"}, {"--"}} {
		if err := Run(args); err == nil {
			t.Errorf("probe(%v) should refuse", args)
		}
	}
}
