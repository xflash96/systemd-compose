package config

import (
	"os/exec"
	"strings"
	"testing"
)

// A calendar expression this host's systemd does not read is named at its
// line (where systemd-analyze is there to ask).
func TestParseSchedule_NamesABadCalendarAtItsLine(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	if _, err := loadYAML(t, "services: {a: {command: x, schedule: \"*-*-* 25:00:00\"}}"); err == nil || !strings.Contains(err.Error(), "line 1: service a: schedule: \"*-*-* 25:00:00\" is not a calendar expression systemd reads") {
		t.Errorf("bad calendar: %v", err)
	}
	if _, err := loadYAML(t, "services: {a: {command: x, schedule: \"Mon *-*-* 03:00\"}}"); err != nil {
		t.Errorf("good calendar: %v", err)
	}
}

// A calendar with no next elapse is noted: its timer would never fire.
func TestParseSchedule_NotesACalendarThatNeverElapses(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	p, err := loadYAML(t, `services: {a: {command: x, schedule: "2020-01-01 00:00:00"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "has no next run") {
		t.Errorf("notes: %q", p.Notes)
	}
}
