package systemd

import (
	"strings"
	"testing"
)

// compose's logs flags, rewritten for journalctl.
func TestComposeLogFlags_RewritesForJournalctl(t *testing.T) {
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
		{"-u --no-log-prefix", false, "-u --no-log-prefix"},
		// refused in the tool's words: journalctl would read "abc" as a
		// match, and a bare --tail or --since as its own unknown flag or a
		// time
		{"--since -1h web", true, "--since -1h web"}, // journalctl's own relative time
		{"--until -5min", false, "--until -5min"},
		{"--tail -f", true, "error"},
		{"--tail abc", true, "error"},
		{"--tail=", true, "error"},
		{"--tail -5", true, "error"},
		{"--since", true, "error"},
		{"--until= web", true, "error"},
	}
	for _, c := range cases {
		out, err := ComposeLogFlags(strings.Fields(c.in), c.project)
		got := strings.Join(out, " ")
		if err != nil {
			got = "error"
		}
		if got != c.want {
			t.Errorf("ComposeLogFlags(%q, %v) = %q (%v), want %q", c.in, c.project, got, err, c.want)
		}
	}
}

// JournalArgs takes ComposeLogFlags' output: the rewritten flags, the
// default output format, and the words mapped to units.
func TestJournalArgs_TakesRewrittenFlags(t *testing.T) {
	lf, _ := ComposeLogFlags(strings.Fields("--tail 2 web"), true)
	flags, units, err := JournalArgs(lf, func(s string) ([]string, error) { return []string{s + ".service"}, nil })
	if err != nil || strings.Join(flags, " ") != "-o short-iso -n 2" || strings.Join(units, " ") != "web.service" {
		t.Errorf("JournalArgs after rewrite: %v %v %v", flags, units, err)
	}
}
