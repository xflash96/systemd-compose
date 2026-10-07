package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The start time is what tells a running unit from the file it was started
// from; States runs systemctl with TZ=UTC so it parses on any systemd.
func TestParseStates(t *testing.T) {
	out := `Id=a.service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=linked
InactiveExitTimestamp=Fri 2026-09-04 05:26:51 UTC

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
	if !a.Active() || a.UnitFileState != "linked" || a.Started.Unix() != 1788499611 {
		t.Errorf("a = %+v", a)
	}
	if b.Known() || !b.Started.IsZero() {
		t.Errorf("b = %+v", b)
	}
	if !a.StartedBefore(a.Started.Add(time.Second)) {
		t.Error("a file written a second after the start must read as newer")
	}
	if a.StartedBefore(a.Started) || a.StartedBefore(a.Started.Add(900*time.Millisecond)) {
		t.Error("a tie within the second must read as applied")
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
