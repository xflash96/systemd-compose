package probe

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// What Args writes, Run reads after the verb, and IsProbe knows as
// systemctl show prints it; the author's own ExecStartPost= it does not.
func TestArgs_RunAndIsProbeReadWhatItWrites(t *testing.T) {
	h := &config.Healthcheck{Interval: "2s", Timeout: "5s", Retries: 3, StartPeriod: "60s", StartInterval: "2s"}
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

// The watch after the start: retries failed checks in a row make the
// service unhealthy, one pass healthy again, and the program's exit ends
// the watch and its state.
func TestWatch_UnhealthyAfterRetriesThenHealthyAgain(t *testing.T) {
	dir := t.TempDir()
	ok, state := filepath.Join(dir, "ok"), filepath.Join(dir, "state")
	if err := os.WriteFile(ok, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var exited atomic.Bool
	w := test{argv: []string{"test", "-e", ok}, timeout: time.Second, gone: exited.Load}
	done := make(chan error, 1)
	go func() { done <- w.watch(time.Second, 2, state) }()
	await := func(what string, want func(State) bool) {
		t.Helper()
		var st State
		for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if data, err := os.ReadFile(state); err == nil && json.Unmarshal(data, &st) == nil && want(st) {
				return
			}
		}
		t.Fatalf("%s: never; the state last read %+v", what, st)
	}
	await("healthy after a passed check", func(s State) bool { return s.Healthy && s.Failed == 0 && s.PID == os.Getpid() })
	os.Remove(ok)
	await("one failed check is not unhealthy yet", func(s State) bool { return s.Healthy && s.Failed == 1 })
	healthyAt2 := false // with retries 2, never: the second failure makes it unhealthy
	await("two in a row are", func(s State) bool {
		healthyAt2 = healthyAt2 || s.Healthy && s.Failed >= 2
		return !s.Healthy && strings.Contains(s.Last, "exit status 1")
	})
	if healthyAt2 {
		t.Error("still healthy after 2 failed checks in a row, with retries 2")
	}
	os.WriteFile(ok, nil, 0o644)
	await("one pass is healthy again", func(s State) bool { return s.Healthy && s.Failed == 0 && s.Last == "" })
	exited.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watch outlived the program by 3s")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("the state outlived the watch: %v", err)
	}
}

// A passed start leaves the watch running (this test binary, as TestMain
// makes it), with its state written for ps. A signal sent to the service
// (HUP reaches its whole cgroup) leaves the checks running; the program's
// end, with the stop's TERM, ends them and takes the state along.
func TestRun_LeavesTheWatchAfterAPassedStart(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("INVOCATION_ID", id)
	program := exec.Command("sleep", "60")
	if err := program.Start(); err != nil {
		t.Fatal(err)
	}
	defer program.Process.Kill()
	t.Setenv("MAINPID", strconv.Itoa(program.Process.Pid))
	if err := Run([]string{"--interval", "30s", "--timeout", "1s", "--start-period", "1s", "--", "true"}); err != nil {
		t.Fatal(err)
	}
	st, ok := ReadState(id)
	if !ok || !st.Healthy || st.PID == os.Getpid() {
		t.Fatalf("ReadState = %+v, %v; want a healthy state kept by another process", st, ok)
	}
	time.Sleep(time.Second) // a signal before the watch takes them would end it as a kill does
	syscall.Kill(st.PID, syscall.SIGHUP)
	time.Sleep(500 * time.Millisecond)
	if _, ok := ReadState(id); !ok {
		t.Fatal("a SIGHUP sent to the service ended its checks")
	}
	program.Process.Kill()
	program.Wait() // no zombie: the program is gone
	syscall.Kill(st.PID, syscall.SIGTERM)
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(StatePath(id)); os.IsNotExist(err) {
			return
		}
	}
	t.Errorf("%s outlived the program and the watch's SIGTERM", StatePath(id))
}

// A state is a live watch's only: not one whose watch is gone, nor one
// whose pid another process has now. A start prunes the others.
func TestReadState_TakesOnlyALiveWatch(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := filepath.Dir(StatePath("own"))
	for id, st := range map[string]State{
		"own":    {PID: os.Getpid(), Start: procStart(os.Getpid())},
		"reused": {PID: os.Getpid(), Start: "1"}, // this pid, an earlier process's start
		"gone":   {PID: 1<<22 + 1, Start: "1"},   // past pid_max
	} {
		if err := writeState(filepath.Join(dir, id), st); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]bool{"own": true, "reused": false, "gone": false} {
		if _, ok := ReadState(id); ok != want {
			t.Errorf("ReadState(%s) = %v, want %v", id, ok, want)
		}
	}
	prune(dir)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != "own" {
		t.Errorf("after prune: %v, want own alone", entries)
	}
}

// A check's output goes on one line: ps puts it in a table cell.
func TestResult_StringIsOneLine(t *testing.T) {
	r := result{err: errors.New("exit status 3"), out: "line one\nline two\ttabbed\n"}
	if got, want := r.String(), "exit status 3; its output: line one line two tabbed"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
