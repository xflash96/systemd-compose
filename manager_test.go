package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The start time is what tells a running unit from the file it was started
// from, to the microsecond (--timestamp=us+utc).
func TestParseStates(t *testing.T) {
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
		t.Error("a file written later in the same second must read as newer (whole seconds would call it applied)")
	}
	if a.StartedBefore(a.Started) || a.StartedBefore(a.Started.Add(-time.Millisecond)) {
		t.Error("a file written before or at the start must read as applied")
	}
	if b.StartedBefore(time.Now()) {
		t.Error("a unit that never started has no start to be before anything")
	}
	if _, err := parseStates("Id=c.service\nInactiveExitTimestamp=n/a\n"); err == nil {
		t.Error("an unparseable timestamp must be an error, not a silent zero")
	}
}

// writeUnit's contract with up's plan: identical text is never rewritten, so
// the mtime stays the birth of the definition; changed text is.
func TestWriteUnitKeepsMtime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.service")
	if err := writeUnit(p, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	born := time.Unix(1700000000, 0)
	if err := os.Chtimes(p, born, born); err != nil {
		t.Fatal(err)
	}
	if err := writeUnit(p, "[Service]\n"); err != nil || !mtime(p).Equal(born) {
		t.Errorf("same text rewrote the file: mtime %v, err %v", mtime(p), err)
	}
	if err := writeUnit(p, "[Service]\nNice=5\n"); err != nil || !mtime(p).After(born) {
		t.Errorf("changed text not written: mtime %v, err %v", mtime(p), err)
	}
}

// The probe's verdict survives in ExecStartPost= after the unit fails, and
// only the probe's entry speaks for it (d: the author's own /bin/false).
func TestProbeExits(t *testing.T) {
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
	got := parseProbeExits(out)
	if got["a.service"] != 1 || got["b.service"] != -1 || got["c.service"] != 1 || got["d.service"] != 0 {
		t.Errorf("parseProbeExits = %v", got)
	}
	cases := []struct {
		active string
		exit   int
		want   string
	}{{"activating", -1, "starting"}, {"active", 0, "ready"}, {"failed", 1, "probe failed"}, {"failed", -1, "-"}, {"inactive", 0, "-"}}
	for _, c := range cases {
		if h := healthOf(UnitState{ActiveState: c.active}, c.exit); h != c.want {
			t.Errorf("healthOf(%s, %d) = %q, want %q", c.active, c.exit, h, c.want)
		}
	}
}
