package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/probe"
	"github.com/xflash96/systemd-compose/internal/systemd"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// A unit just started is in trouble when it failed or has restarted since;
// the verdict says how, from what systemd recorded.
func TestVerdict_NamesWhatWentWrong(t *testing.T) {
	hc := &config.Service{Name: "a", Healthcheck: &config.Healthcheck{}}
	for _, c := range []struct {
		st    systemd.UnitState
		svc   *config.Service
		probe int
		want  string
	}{
		{systemd.UnitState{ActiveState: "active", SubState: "running"}, nil, -1, ""},
		{systemd.UnitState{ActiveState: "inactive", SubState: "dead", ExitCode: 1}, nil, -1, ""},
		{systemd.UnitState{ActiveState: "failed"}, nil, -1, "failed to start"},
		{systemd.UnitState{ActiveState: "failed", ExitCode: 1, Exit: 3}, nil, -1, "failed to start (it exited with status 3)"},
		{systemd.UnitState{ActiveState: "failed", ExitCode: 2, Exit: 15}, hc, 1, "failed to start: its healthcheck did not pass (last probe exit 1)"},
		{systemd.UnitState{ActiveState: "failed", ExitCode: 1, Exit: 1}, hc, probe.MainGone, "failed to start: the program exited before its healthcheck passed"},
		{systemd.UnitState{ActiveState: "active", SubState: "running", NRestarts: 2}, nil, -1, "keeps restarting (restart #2); it did not stay up 3s while watched"},
		// up 3s since the program last started: a retry that worked
		{systemd.UnitState{ActiveState: "active", SubState: "running", NRestarts: 2, Started: time.Now(), Ran: time.Now().Add(-4 * time.Second)}, nil, -1, ""},
		// the program started a moment ago, though the restart began 4s ago
		{systemd.UnitState{ActiveState: "active", SubState: "running", NRestarts: 2, Started: time.Now().Add(-4 * time.Second), Ran: time.Now()}, nil, -1, "keeps restarting"},
		{systemd.UnitState{ActiveState: "inactive", NRestarts: 1, Result: "success", ExitCode: 1}, nil, -1, ""},
		{systemd.UnitState{ActiveState: "failed", Result: "timeout", ExitCode: 2, Exit: 15}, nil, -1, "failed to start: it was not up within its start timeout"},
		{systemd.UnitState{ActiveState: "activating", SubState: "auto-restart"}, hc, -1, "failed to start, and its restart: policy is trying again"},
		// the probe failed, and systemd stopped the program to retry
		{systemd.UnitState{ActiveState: "activating", SubState: "auto-restart", NRestarts: 2, ExitCode: 2, Exit: 15, Result: "exit-code"}, hc, -1, "keeps restarting (restart #2): its healthcheck did not pass"},
		// the probe passed, and the program exits after
		{systemd.UnitState{ActiveState: "activating", SubState: "auto-restart", NRestarts: 2, ExitCode: 1, Exit: 1, Result: "exit-code"}, hc, -1, "keeps restarting (restart #2): the program exits (status 1)"},
		// started again, its probe in flight once the watch gave it its
		// healthcheck budget: it never became ready
		{systemd.UnitState{ActiveState: "activating", SubState: "start-post", NRestarts: 1}, hc, -1, "keeps restarting (restart #1): its healthcheck has not passed while watched"},
		// a oneshot's run after a retry is no healthcheck's to judge
		{systemd.UnitState{ActiveState: "activating", SubState: "start", NRestarts: 1}, nil, -1, ""},
	} {
		if got := verdict(c.st, c.svc, c.probe); got != c.want && (c.want == "" || !strings.HasPrefix(got, c.want)) {
			t.Errorf("verdict(%+v, %d) = %q, want %q", c.st, c.probe, got, c.want)
		}
	}
}

// Each state has its HEALTH word, and docs/troubleshooting.md names every
// word ps can show.
func TestHealthOf_NamesEachState(t *testing.T) {
	words := map[string]bool{}
	healthy, unhealthy := probe.State{Healthy: true}, probe.State{Failed: 3}
	cases := []struct {
		active  string
		exit    int
		check   probe.State
		checked bool
		want    string
	}{
		{"activating", -1, healthy, true, "starting"},
		{"active", 0, healthy, true, "healthy"},
		{"active", 0, unhealthy, true, "unhealthy"},
		{"active", 0, healthy, false, "unchecked"}, // no watch keeps a state for this run
		{"failed", 1, healthy, false, "probe failed"},
		{"failed", probe.MainGone, healthy, false, "program exited"},
		{"failed", -1, healthy, false, "failed"}, // a failed unit whose probe exit a reload forgot is "failed", not "-"
		{"inactive", 0, healthy, false, "-"},
	}
	for _, c := range cases {
		h := healthOf(systemd.UnitState{ActiveState: c.active}, c.exit, c.check, c.checked)
		if h != c.want {
			t.Errorf("healthOf(%s, %d, %+v, %v) = %q, want %q", c.active, c.exit, c.check, c.checked, h, c.want)
		}
		words[h] = true
	}
	// A service waiting out an automatic restart is restarting; one whose
	// program is up again while its probe runs is starting.
	for st, want := range map[systemd.UnitState]string{
		{ActiveState: "activating", SubState: "auto-restart", NRestarts: 3}: "restarting (#3)",
		{ActiveState: "activating", SubState: "auto-restart"}:               "restarting",
		{ActiveState: "activating", SubState: "start-post", NRestarts: 3}:   "starting",
	} {
		h := healthOf(st, -1, probe.State{}, false)
		if h != want {
			t.Errorf("healthOf(%+v) = %q, want %q", st, h, want)
		}
		words[strings.TrimSuffix(h, " (#3)")] = true
	}
	doc, err := os.ReadFile("../../docs/troubleshooting.md")
	if err != nil {
		t.Fatal(err)
	}
	_, health, _ := strings.Cut(string(doc), "\n- HEALTH ")
	health, _, _ = strings.Cut(health, "\n- ")
	for w := range words {
		if w != "-" && !strings.Contains(strings.Join(strings.Fields(health), " "), "`"+w+"`") {
			t.Errorf("docs/troubleshooting.md's HEALTH line does not name %s", w)
		}
	}
}

// Two services of profiles that take turns on one address: when the new
// one's socket fails, the one left running from the other profile is named.
func TestVerdictOf_NamesTheOtherProfilesListener(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	path := filepath.Join(dir, config.ConfigFileName)
	os.WriteFile(path, []byte("name: tt\nservices:\n  p1: {command: x, listen: 18200, profiles: [one]}\n  p2: {command: x, listen: 18200, profiles: [two]}\n"), 0o644)
	p, err := config.Load(path, config.Options{Profiles: []string{"two"}})
	if err != nil {
		t.Fatal(err)
	}
	pr := &project{p: p, self: "systemd-compose --profile two"}
	if got := pr.verdictOf("tt-p2.socket", systemd.UnitState{ActiveState: "failed"}, -1); !strings.Contains(got, "service p1 (its profile not in this run)") || !strings.Contains(got, "then systemd-compose --profile two up") {
		t.Errorf("verdict: %q", got) // a bare "then up" would turn the profile off again
	}
}

// A healthcheck failure under a low pids: cap says the cap counts the
// probe's own threads, so the test is not blamed for it.
func TestVerdict_NotesALowPidsCap(t *testing.T) {
	svc := &config.Service{Healthcheck: &config.Healthcheck{}, Resources: &config.Resources{PIDs: 4}}
	if v := verdict(systemd.UnitState{ActiveState: "failed"}, svc, 2); !strings.Contains(v, "pids: 4 counts the probe's own") {
		t.Errorf("verdict = %q", v)
	}
	svc.Resources.PIDs = 64
	if v := verdict(systemd.UnitState{ActiveState: "failed"}, svc, 2); strings.Contains(v, "pids:") {
		t.Errorf("a cap of 64 noted: %q", v)
	}
}

// A healthchecked service stopped from elsewhere while up waits is said so,
// not blamed on the healthcheck: the stop leaves Result=signal and a main
// process killed by SIGTERM, a failed probe Result=exit-code.
func TestVerdict_StopFromElsewhereIsNoHealthcheckFailure(t *testing.T) {
	svc := &config.Service{Healthcheck: &config.Healthcheck{}}
	stopped := systemd.UnitState{ActiveState: "failed", Result: "signal", ExitCode: 2, Exit: 15}
	if v := verdict(stopped, svc, 1); !strings.Contains(v, "stopped from elsewhere") {
		t.Errorf("a stop: %q", v)
	}
	failed := systemd.UnitState{ActiveState: "failed", Result: "exit-code", ExitCode: 2, Exit: 15}
	if v := verdict(failed, svc, 1); !strings.Contains(v, "healthcheck did not pass") {
		t.Errorf("a failed probe: %q", v)
	}
	crashed := systemd.UnitState{ActiveState: "failed", Result: "signal", ExitCode: 2, Exit: 11}
	if v := verdict(crashed, svc, probe.MainGone); strings.Contains(v, "stopped from elsewhere") {
		t.Errorf("a crash: %q", v)
	}
}

// up's wait line says, in the yaml's words, how long the healthcheck may
// take: its start_period, the default named as one, and what a restart:
// policy does when it fails.
func TestHealthWait_SaysItInTheYamlsWords(t *testing.T) {
	for _, c := range []struct {
		startPeriod string
		restart     *config.Restart
		want        string
	}{
		{config.Default("healthcheck.start_period"), nil, "until it passes or its start_period, 60s by default, runs out"},
		{"30s", &config.Restart{Policy: "always", Written: "unless-stopped"}, "until it passes or its start_period, 30s, runs out; if it fails, restart: unless-stopped starts it again"},
		{"1min", &config.Restart{Policy: "no", Written: "no"}, "until it passes or its start_period, 60s, runs out"},
	} {
		svc := &config.Service{Restart: c.restart, Healthcheck: &config.Healthcheck{StartPeriod: c.startPeriod, Timeout: "5s"}}
		if got := healthWait(svc); got != c.want {
			t.Errorf("start_period %s: %q, want %q", c.startPeriod, got, c.want)
		}
	}
}
