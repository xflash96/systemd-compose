package project

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// A one-off whose systemd-run exits 0 is no success when the user manager
// went away mid-command (restarted, exited, killed): systemd-run exits 0
// then, on either path.
func TestOneOffOutcome_ManagerGoneIsNoSuccess(t *testing.T) {
	up := func() error { return nil }
	gone := func() error { return errors.New("no manager") }
	if err := oneOffOutcome(nil, up); err != nil {
		t.Errorf("a run that ended, manager there: %v", err)
	}
	if err := oneOffOutcome(nil, gone); err == nil || !strings.Contains(err.Error(), "went away") {
		t.Errorf("a run that ended with the manager gone: %v", err)
	}
	if err := oneOffOutcome(systemd.ExitError{Code: 7}, gone); err != (systemd.ExitError{Code: 7}) {
		t.Errorf("the command's own exit, passed on: %v", err)
	}
}

// envFileQuote: systemd reads a backslash before \ " ` $ in a double-quoted
// env-file value as keeping that character.
func TestEnvFileQuote_EscapesAsSystemdReads(t *testing.T) {
	if got := envFileQuote(`a"b\c$d` + "`e"); got != `a\"b\\c\$d\`+"`e" {
		t.Errorf("envFileQuote = %q", got)
	}
}

// A systemd-run that a terminal's ^C killed before this client's handler
// saw the signal is an interruption, so the build step it ran is stopped.
// A child that exits, even with a failure, is not.
func TestKilledBySignal_TellsASignalFromAnExit(t *testing.T) {
	c := exec.Command("sleep", "10")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	c.Process.Signal(syscall.SIGINT)
	if err := c.Wait(); !killedBySignal(err) {
		t.Errorf("sleep killed by SIGINT: killedBySignal(%v) = false", err)
	}
	if err := exec.Command("sh", "-c", "exit 1").Run(); killedBySignal(err) {
		t.Errorf("exit 1: killedBySignal(%v) = true", err)
	}
	if killedBySignal(nil) || killedBySignal(errors.New("no")) {
		t.Error("a nil or plain error counts as a signal")
	}
}

// A one-off gets the service's own limits, and outside the project's slice
// the project's too, the tighter of each.
func TestLimitProps_TakesTheTighterCap(t *testing.T) {
	svc := &config.Resources{Memory: "32M", PIDs: 64}
	proj := &config.Resources{Memory: "2G", CPUs: 0.5, PIDs: 16}
	for _, c := range []struct {
		svc, proj *config.Resources
		inSlice   bool
		want      string
	}{
		{nil, nil, true, ""},
		{svc, proj, true, "-p MemoryMax=32M -p TasksMax=64"},
		{svc, proj, false, "-p MemoryMax=32M -p CPUQuota=50% -p TasksMax=16"},
		{nil, proj, false, "-p MemoryMax=2G -p CPUQuota=50% -p TasksMax=16"},
		{nil, proj, true, ""},
		{&config.Resources{Memory: "1G"}, &config.Resources{Memory: "512M"}, false, "-p MemoryMax=512M"},
	} {
		if got := strings.Join(limitProps(c.svc, c.proj, c.inSlice), " "); got != c.want {
			t.Errorf("limitProps(%+v, %+v, %v) = %q, want %q", c.svc, c.proj, c.inSlice, got, c.want)
		}
	}
}

// A memory value's suffixes are systemd's powers of 1024.
func TestMemBytes_PowersOf1024(t *testing.T) {
	for s, want := range map[string]uint64{"512": 512, "1K": 1024, "32M": 32 << 20, "2G": 2 << 30, "1T": 1 << 40} {
		if got := memBytes(s); got != want {
			t.Errorf("memBytes(%q) = %d, want %d", s, got, want)
		}
	}
}

// How a one-off ended, as its unit records it on systemd 249 and 255:
// systemd-run exits 255 for a ^C as for a stop, and 1 for an out-of-memory
// kill or a SEGV, as if the command had exited 1.
func TestReadEnd_NamesHowTheCommandEnded(t *testing.T) {
	dir := t.TempDir()
	for record, want := range map[string]oneOffEnd{
		"exit-code exited 7":    {recorded: true},
		"signal killed TERM":    {recorded: true, signal: "TERM"},
		"signal killed INT":     {recorded: true, signal: "INT"},
		"oom-kill killed KILL":  {recorded: true, oom: true, signal: "KILL"},
		"core-dump dumped SEGV": {recorded: true, signal: "SEGV", dumped: true},
		"":                      {},
	} {
		p := filepath.Join(dir, "end")
		os.WriteFile(p, []byte(record+"\n"), 0o600)
		if got := readEnd(p); got != want {
			t.Errorf("readEnd(%q) = %+v, want %+v", record, got, want)
		}
	}
	if e := (oneOffEnd{signal: "SEGV", dumped: true}); e.number() != 11 || e.how() != "killed by SIGSEGV (core dumped)" {
		t.Errorf("SEGV: %d, %q", e.number(), e.how())
	}
	if readEnd(filepath.Join(dir, "missing")) != (oneOffEnd{}) {
		t.Error("no record reads as an ending")
	}
}

// A run's files in the private directory are named by its pid, and those
// of a pid that is gone are swept: a killed up leaves its verify copies,
// and a killed client its env file.
func TestScratchDir_SweepsWhatDeadRunsLeft(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	c := exec.Command("true")
	c.Run()
	dead, live := c.Process.Pid, os.Getpid()
	dir := filepath.Join(rt, "systemd-compose")
	os.MkdirAll(dir, 0o700)
	files := map[string]bool{ // name -> swept
		fmt.Sprintf("env-%d-abc", dead): true,
		fmt.Sprintf("end-%d-1", dead):   true,
		fmt.Sprintf("env-%d-abc", live): false,
		fmt.Sprintf("end-%d-0", live):   false,
		"demo.lock":                     false,
	}
	for f := range files {
		os.WriteFile(filepath.Join(dir, f), nil, 0o600)
	}
	os.Mkdir(filepath.Join(dir, fmt.Sprintf("verify-%d-xyz", dead)), 0o700)
	files[fmt.Sprintf("verify-%d-xyz", dead)] = true
	if _, err := scratchDir(); err != nil {
		t.Fatal(err)
	}
	for f, swept := range files {
		if exists(filepath.Join(dir, f)) == swept {
			t.Errorf("%s: swept %v, want %v", f, !exists(filepath.Join(dir, f)), swept)
		}
	}
}
