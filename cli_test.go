package main

import (
	"strings"
	"testing"
)

// compose's logs flags, rewritten for journalctl.
func TestComposeLogFlags(t *testing.T) {
	cases := []struct {
		in      string
		project bool
		want    string
	}{
		{"--tail 50 web", true, "-n 50 web"},
		{"--tail=5", true, "-n 5"},
		{"--tail all -f", true, "-f"},
		{"-t web", true, "web"},
		{"-t sshd", false, "-t sshd"},
		{"--timestamps --no-log-prefix", true, "-o cat"},
		{"--since 42m --until=1h30m", true, "--since -42m --until -1h30m"},
		{"--since 2026-10-02T17:00:00Z", true, "--since 2026-10-02T17:00:00Z"},
		{"--since yesterday -n 3", false, "--since yesterday -n 3"},
		{"-g --tail 5", true, "-g --tail 5"}, // a flag's own value is not a flag
		{"--tail -f", true, "--tail -f"},     // no count: journalctl refuses it by name
		{"-u --no-log-prefix", false, "-u --no-log-prefix"},
	}
	for _, c := range cases {
		if got := strings.Join(composeLogFlags(strings.Fields(c.in), c.project), " "); got != c.want {
			t.Errorf("composeLogFlags(%q, %v) = %q, want %q", c.in, c.project, got, c.want)
		}
	}
	flags, units, err := journalArgs(composeLogFlags(strings.Fields("--tail 2 web"), true), func(s string) (string, error) { return s + ".service", nil })
	if err != nil || strings.Join(flags, " ") != "-o short-iso -n 2" || strings.Join(units, " ") != "web.service" {
		t.Errorf("journalArgs after rewrite: %v %v %v", flags, units, err)
	}
}
