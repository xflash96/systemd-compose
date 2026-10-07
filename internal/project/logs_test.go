package project

import (
	"strings"
	"testing"
)

// logs matches each unit as journalctl -u does, or'ed with each service's
// journal tag, which is all a line from an already-exited child carries.
func TestJournalMatches_OrsUnitsWithTags(t *testing.T) {
	got := strings.Join(journalMatches([]string{"p.slice", "p-s.service"}, []string{"p-s"}), " ")
	want := "_SYSTEMD_USER_SLICE=p.slice + USER_UNIT=p.slice + OBJECT_SYSTEMD_USER_UNIT=p.slice + " +
		"_SYSTEMD_USER_UNIT=p-s.service + COREDUMP_USER_UNIT=p-s.service + USER_UNIT=p-s.service + OBJECT_SYSTEMD_USER_UNIT=p-s.service + " +
		"SYSLOG_IDENTIFIER=p-s"
	if got != want {
		t.Errorf("journalMatches = %q\nwant %q", got, want)
	}
}

// A service's journal tag is PROJECT-SERVICE, or the SyslogIdentifier= its
// unit: sets.
func TestLogIdentifier_TakesTheUnitsSyslogIdentifier(t *testing.T) {
	p, err := loadYAML(t, "name: p\nservices:\n  a: {command: /bin/true}\n  b: {command: /bin/true, unit: {Service: {SyslogIdentifier: mine}}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := p.LogIdentifier(p.Service("a")), p.LogIdentifier(p.Service("b")); a != "p-a" || b != "mine" {
		t.Errorf("LogIdentifier: a %q, b %q; want p-a, mine", a, b)
	}
}
