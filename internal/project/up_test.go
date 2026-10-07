package project

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// A change only in the env files' hash is told apart from a change to what
// the yaml says.
func TestOnlyTriggers_TellsAnEnvFileChangeApart(t *testing.T) {
	a := "[Service]\nExecStart=/bin/x\n\n[X-SystemdCompose]\nRestartTriggers=aaa\n"
	if !onlyTriggers(a, strings.Replace(a, "aaa", "bbb", 1)) {
		t.Error("a new env file hash alone is an env_file change")
	}
	if onlyTriggers(a, strings.Replace(strings.Replace(a, "aaa", "bbb", 1), "/bin/x", "/bin/y", 1)) {
		t.Error("a changed ExecStart= is more than an env_file change")
	}
}

// A restart takes along what Requires= the restarted unit, transitively, and
// the plan says so; a restart that would take a running start-only service
// along is held back.
func TestAlongside_RestartsDependentsAndSparesStartOnly(t *testing.T) {
	p, err := loadYAML(t, `
name: p
services:
  db: {command: x}
  mid: {command: x, depends_on: {db: {condition: service_started, required: true}}}
  leaf: {command: x, depends_on: {mid: {condition: service_started, required: true}}}
  other: {command: x, depends_on: [db]}
  cache: {command: x}
  keep: {command: x, on_change: start-only, depends_on: {cache: {condition: service_started, required: true}}}
`)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := render.Render(p, render.RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "x")}})
	if err != nil {
		t.Fatal(err)
	}
	pr := &project{p: p, rendered: rendered}
	before := map[string]systemd.UnitState{}
	var rows []planRow
	for _, u := range rendered {
		before[u.Name] = systemd.UnitState{ActiveState: "active"}
		rows = append(rows, planRow{unit: u.Name, change: "unchanged"})
	}
	keep, bounced, held, warnings := pr.alongside(rows, []string{"p-db.service", "p-cache.service"}, before)
	if !slices.Equal(keep, []string{"p-db.service"}) || !slices.Equal(bounced, []string{"p-mid.service", "p-leaf.service"}) || !slices.Equal(held, []string{"p-cache.service"}) || len(warnings) != 1 {
		t.Fatalf("keep %v, bounced %v, held %v, warnings %q", keep, bounced, held, warnings)
	}
	for _, r := range rows {
		got := strings.Join(r.actions, ", ")
		switch r.unit {
		case "p-leaf.service":
			if got != "restarts along with p-mid.service, which it depends on" {
				t.Errorf("leaf: %q", got)
			}
		case "p-cache.service":
			if !strings.Contains(got, "it would take keep along") {
				t.Errorf("cache: %q", got)
			}
		case "p-other.service", "p-keep.service":
			if got != "" {
				t.Errorf("%s: %q (Wants= takes nothing along; start-only is spared)", r.unit, got)
			}
		}
	}
}

// Paths the tool finds are written as systemd reads them back: a % in the
// project directory would be taken for a specifier (pct%done would run in
// pct + the credentials directory + one), and a space in it would split a
// string command's resolved program in two. A program its own build: makes
// is no refusal before the build has run, or a fresh clone could never
// come up.
func TestUp_EscapesFoundPathsAndWaitsForBuilds(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := filepath.Join(t.TempDir(), "a b%d")
	for _, d := range []string{dir, filepath.Join(dir, "run")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "e.env"), []byte("A=1\n"), 0o644)
	yml := `name: fp
services:
  s: {command: "./tool x", env_file: e.env}
  l: {command: [./tool], listen: run/a.sock}
  b: {command: ./out/app, build: {run: [./tool], creates: out/app}}
`
	path := filepath.Join(dir, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(path, config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, err := render.Render(p, render.RenderOptions{Exe: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{}
	for _, u := range units {
		text[u.Name] = u.Text
	}
	esc := strings.ReplaceAll(dir, "%", "%%")
	for unit, want := range map[string][]string{
		"fp-s.service": {"WorkingDirectory=" + esc + "\n", `ExecStart="` + esc + `/tool" x` + "\n", "EnvironmentFile=" + esc + "/e.env\n"},
		"fp-l.socket":  {"ListenStream=" + esc + "/run/a.sock\n"},
		"fp-l.service": {`ExecStart="` + esc + `/tool"` + "\n"},
		"fp-b.service": {`ExecStart="` + esc + `/out/app"` + "\n"},
	} {
		for _, w := range want {
			if !strings.Contains(text[unit], w) {
				t.Errorf("%s: want %q in\n%s", unit, w, text[unit])
			}
		}
	}
	// up's verify gate: systemd's "not executable" about that program
	// alone is no refusal while its build is pending; any other line is
	pr := &project{p: p}
	prog := filepath.Join(dir, "out", "app")
	if out, err := pr.pendingPrograms("/tmp/v/fp-b.service: Command "+prog+" is not executable: No such file or directory", errors.New("exit 1")); out != "" || err != nil {
		t.Errorf("a pending program: %q %v", out, err)
	}
	other := "/tmp/v/fp-s.service: Command /nowhere/x is not executable: No such file or directory"
	if out, _ := pr.pendingPrograms(other, nil); out != other {
		t.Errorf("another missing program: %q", out)
	}
}

// A limit the user manager cannot enforce renders fine and does nothing,
// so it is refused: cpus: on systemd 249 (memory and pids delegated),
// AllowedCPUs= anywhere without cpuset.
func TestUndelegated_RefusesLimitsWithoutTheirController(t *testing.T) {
	p, err := loadYAML(t, "resources: {cpus: 0.5}\nservices: {a: {command: x}, b: {command: x, unit: {Service: {AllowedCPUs: 0}}}}")
	if err != nil {
		t.Fatal(err)
	}
	if err := undelegated(p, []string{"memory", "pids"}); err == nil || !strings.Contains(err.Error(), "resources: cpus: the cpu cgroup controller") {
		t.Errorf("cpus without cpu: %v", err)
	}
	if err := undelegated(p, []string{"cpu", "memory", "pids"}); err == nil || !strings.Contains(err.Error(), "AllowedCPUs: the cpuset cgroup controller") {
		t.Errorf("AllowedCPUs without cpuset: %v", err)
	}
	if err := undelegated(p, []string{"cpu", "cpuset", "memory", "pids"}); err != nil {
		t.Errorf("all delegated: %v", err)
	}
	if err := undelegated(p, nil); err != nil {
		t.Errorf("controllers unknown: %v", err)
	}
}

// verify's refusal says whose lines it quotes: the rendered units', or a
// host drop-in's, which has line numbers of its own.
func TestVerifyWhere_NamesADropInsLines(t *testing.T) {
	if got := verifyWhere("p-a.service:12: Unknown key"); !strings.Contains(got, "the rendered units'") || strings.Contains(got, "drop-in") {
		t.Errorf("ours: %q", got)
	}
	if got := verifyWhere("/home/u/.config/systemd/user/service.d/x.conf:2: KillMode=none is deprecated"); !strings.Contains(got, "drop-in") {
		t.Errorf("a drop-in's: %q", got)
	}
}

// The healthcheck probe runs from a copy of the binary named by its
// content: every probe survives a move of the binary that ran up, and an up
// from another copy of the same build changes no healthchecked unit.
func TestEnsureProbe_CopiesByContent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	for f, body := range map[string]string{a: "build one", b: "build one", c: "build two"} {
		if err := os.WriteFile(f, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pa, _ := render.ProbePath(a)
	pb, _ := render.ProbePath(b)
	pc, _ := render.ProbePath(c)
	if pa != pb || pa == pc {
		t.Errorf("probePath: same build %q and %q, other build %q", pa, pb, pc)
	}
	opt := render.RenderOptions{Exe: pa, Self: a}
	if err := ensureProbe(opt); err != nil {
		t.Fatal(err)
	}
	os.Remove(a) // the binary that ran up, moved away
	got, err := os.ReadFile(pa)
	if err != nil || string(got) != "build one" {
		t.Errorf("the copy: %q, %v", got, err)
	}
	if st, _ := os.Stat(pa); st.Mode().Perm() != 0o700 {
		t.Errorf("the copy's mode: %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(pa)); st.Mode().Perm() != 0o700 {
		t.Errorf("the copy's directory's mode: %v", st.Mode().Perm())
	}
	if err := ensureProbe(opt); err != nil { // there already: nothing to do
		t.Errorf("second ensureProbe: %v", err)
	}
}

// A drop-in of the user's that uses a setting systemd deprecates but honours
// is a note, not a refusal of every up; a typo there, or anything about the
// rendered units, still refuses.
func TestOthersDeprecations_NotesWhatSystemdHonours(t *testing.T) {
	out := "/home/u/.config/systemd/user/service.d/x.conf:2: Unit uses KillMode=none. This is unsafe... Support for KillMode=none is deprecated and will eventually be removed.\n" +
		"/home/u/.config/systemd/user/service.d/y.conf:3: Standard output type syslog is obsolete, automatically updating to journal."
	rest, notes, err := othersDeprecations(out, errors.New("exit 1"), "/tmp/stage")
	if rest != "" || err != nil || len(notes) != 2 {
		t.Errorf("deprecations alone: %q, %v, %d notes", rest, err, len(notes))
	}
	typo := "/home/u/.config/systemd/user/service.d/z.conf:2: Unknown key name 'Nicee' in section 'Service', ignoring."
	if rest, _, _ := othersDeprecations(typo, nil, "/tmp/stage"); rest != typo {
		t.Errorf("a typo in a drop-in passed: %q", rest)
	}
	ours := "/tmp/stage/p-a.service:5: Standard output type syslog is obsolete, automatically updating to journal."
	if rest, _, _ := othersDeprecations(ours, nil, "/tmp/stage"); rest != ours {
		t.Errorf("a line about the render passed: %q", rest)
	}
}
