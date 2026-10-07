package project

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// What a signal did, from the states before and after.
func TestKilled_WordsWhatTheSignalDid(t *testing.T) {
	t0, t1 := time.Unix(1, 0), time.Unix(2, 0)
	up := systemd.UnitState{ActiveState: "active", Started: t0}
	svc := &config.Service{Name: "api"}
	for _, c := range []struct {
		after systemd.UnitState
		want  string
	}{
		{up, "still running (the signal did not stop it; systemd-compose kill -s KILL api does)"},
		{systemd.UnitState{ActiveState: "active", Started: t0, NRestarts: 1}, "its restart: policy brings it back (stop api keeps it down)"},
		{systemd.UnitState{ActiveState: "activating", SubState: "auto-restart", Started: t0}, "its restart: policy brings it back"},
		{systemd.UnitState{ActiveState: "active", Started: t1}, "it stopped and came back"},
		{systemd.UnitState{ActiveState: "failed", Started: t0}, "now failed"},
	} {
		if got := killed(up, c.after, svc, "systemd-compose"); !strings.Contains(got, c.want) {
			t.Errorf("killed(%+v) = %q, want %q", c.after, got, c.want)
		}
	}
}

// What a verb can move: a start pulls in what the named unit needs, a stop
// takes along what needs it, a restart both; a unit with no path to the
// named one is never reported as moved by it.
func TestReach_StartPullsInStopTakesAlong(t *testing.T) {
	p, err := loadYAML(t, `
name: p
services:
  db: {command: x}
  api: {command: x, depends_on: {db: {condition: service_started, required: true}}}
  web: {command: x, depends_on: [api]}
  tick: {command: x, schedule: hourly}
`)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := render.Render(p, render.RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "x")}})
	if err != nil {
		t.Fatal(err)
	}
	pr := &project{p: p, rendered: rendered}
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		verb, unit string
		want       []string
	}{
		{"start", "p-web.service", []string{"p-api.service", "p-db.service"}},
		{"stop", "p-db.service", []string{"p-api.service"}}, // web only Wants= api
		{"restart", "p-api.service", []string{"p-db.service"}},
		{"stop", "p-tick.timer", nil},
		{"kill", "p-db.service", []string{"p-api.service"}}, // what a stop of db takes along
	} {
		if got := keys(pr.reach(c.verb, []string{c.unit})); !slices.Equal(got, c.want) {
			t.Errorf("reach(%s %s) = %v, want %v", c.verb, c.unit, got, c.want)
		}
	}
}

func TestSignalName_TakesNamesAndNumbers(t *testing.T) {
	for _, ok := range []string{"TERM", "sigkill", "SIGHUP", "9", "RTMIN+3", "usr1"} {
		if !signalName(ok) {
			t.Errorf("%q is a signal", ok)
		}
	}
	for _, bad := range []string{"BOGUS", "0", "99", "SIG", "RTMIN+99", ""} {
		if signalName(bad) {
			t.Errorf("%q is no signal", bad)
		}
	}
}

// kill hands systemctl the signal in capitals, since systemctl refuses
// "sigkill", and a number is reported by its signal's name.
func TestKillArgs_SpellsTheSignalForSystemctl(t *testing.T) {
	for _, c := range []struct {
		args   []string
		flags  []string
		names  []string
		signal string
		label  string
	}{
		{[]string{"web"}, nil, []string{"web"}, "TERM", "SIGTERM"},
		{[]string{"-s", "sigkill", "web"}, []string{"--signal=KILL"}, []string{"web"}, "KILL", "SIGKILL"},
		{[]string{"-shup", "web", "db"}, []string{"--signal=HUP"}, []string{"web", "db"}, "HUP", "SIGHUP"},
		{[]string{"--signal=9", "web"}, []string{"--signal=9"}, []string{"web"}, "9", "SIGKILL"},
		{[]string{"--signal", "40", "web"}, []string{"--signal=40"}, []string{"web"}, "40", "signal 40"},
	} {
		flags, names, sig, err := killArgs(c.args)
		if err != nil || !slices.Equal(flags, c.flags) || !slices.Equal(names, c.names) || sig != c.signal || signalLabel(sig) != c.label {
			t.Errorf("killArgs(%q) = %q %q %q %v (label %q)", c.args, flags, names, sig, err, signalLabel(sig))
		}
	}
}

func TestKillArgs_RefusesOtherFlags(t *testing.T) {
	for _, bad := range [][]string{{"-t", "5", "web"}, {"-s", "BOGUS", "web"}, {"-s"}} {
		if _, _, _, err := killArgs(bad); err == nil {
			t.Errorf("killArgs(%q) accepted", bad)
		}
	}
}
