package systemd

import (
	"strings"
	"testing"
	"time"
)

// The start time is what tells a running unit from the file it was started
// from, to the microsecond (--timestamp=us+utc).
func TestParseStates_ReadsStartTimesToTheMicrosecond(t *testing.T) {
	out := `Id=a.service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=linked
InactiveExitTimestamp=Fri 2026-09-04 05:26:51.250000 UTC

Id=b.service
LoadState=not-found
ActiveState=inactive
SubState=dead
UnitFileState=
InactiveExitTimestamp=
`
	states, err := parseStates(out)
	if err != nil {
		t.Fatal(err)
	}
	a, b := states["a.service"], states["b.service"]
	if !a.Active() || a.UnitFileState != "linked" || a.Started.UnixMicro() != 1788499611250000 {
		t.Errorf("a = %+v", a)
	}
	if b.Known() || !b.Started.IsZero() {
		t.Errorf("b = %+v", b)
	}
	if !a.StartedBefore(a.Started.Add(500 * time.Millisecond)) {
		t.Error("a file written later in the same second must read as newer")
	}
	if a.StartedBefore(a.Started) || a.StartedBefore(a.Started.Add(-time.Millisecond)) {
		t.Error("a file written before or at the start must read as applied")
	}
	if b.StartedBefore(time.Now()) {
		t.Error("a unit that never started has no start to be before anything")
	}
	if s, err := parseStates("Id=c.service\nInactiveExitTimestamp=n/a\n"); err != nil || !s["c.service"].Started.IsZero() {
		t.Errorf("systemd 249 spells a never-started unit n/a: %v %+v", err, s)
	}
	if _, err := parseStates("Id=d.service\nInactiveExitTimestamp=yesterday\n"); err == nil {
		t.Error("an unparseable timestamp must be an error, not a silent zero")
	}
}

// The probe's verdict survives in ExecStartPost= after the unit fails, and
// only the entry the recogniser takes speaks for it (d: the author's own
// /bin/false). probe.IsProbe is the real recogniser, tested beside Args.
func TestParseProbeExits_ReadsOnlyTheProbesEntry(t *testing.T) {
	out := `Id=a.service
ExecStartPost={ path=/x ; argv[]=/x probe --interval 2s -- curl ; ignore_errors=no ; start_time=[Fri] ; stop_time=[Fri] ; pid=9 ; code=exited ; status=1 }

Id=b.service
ExecStartPost={ path=/x ; argv[]=/x probe --interval 2s -- curl ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }

Id=c.service
ExecStartPost={ path=/x ; argv[]=/x probe --interval 1s -- t ; ignore_errors=no ; start_time=[Fri] ; stop_time=[Fri] ; pid=9 ; code=killed ; status=9/KILL }

Id=d.service
ExecStartPost={ path=/x ; argv[]=/x probe --interval 1s -- t ; ignore_errors=no ; start_time=[Fri] ; stop_time=[Fri] ; pid=9 ; code=exited ; status=0 }
ExecStartPost={ path=/bin/false ; argv[]=/bin/false ; ignore_errors=no ; start_time=[Fri] ; stop_time=[Fri] ; pid=10 ; code=exited ; status=1 }
`
	got := parseProbeExits(out, func(v string) bool { return strings.Contains(v, " probe ") })
	if got["a.service"] != 1 || got["b.service"] != -1 || got["c.service"] != 1 || got["d.service"] != 0 {
		t.Errorf("parseProbeExits = %v", got)
	}
}

// NRestarts and a timer's next run come with the states; a value systemd
// leaves out reads as zero.
func TestParseStates_ReadsRestartsAndTimers(t *testing.T) {
	states, err := parseStates(`Id=a.service
ActiveState=activating
SubState=start-post
NRestarts=4

Id=b.timer
ActiveState=active
NextElapseUSecRealtime=Mon 2026-10-05 01:25:00 PDT

Id=c.service
NRestarts=0
NextElapseUSecRealtime=
`)
	if err != nil {
		t.Fatal(err)
	}
	if a := states["a.service"]; a.NRestarts != 4 || a.Restarting() {
		t.Errorf("a = %+v", a)
	}
	if b := states["b.timer"]; b.NextRun != "Mon 2026-10-05 01:25:00 PDT" {
		t.Errorf("b = %+v", b)
	}
	if c := states["c.service"]; c.NRestarts != 0 || c.NextRun != "" || c.Restarting() {
		t.Errorf("c = %+v", c)
	}
}
