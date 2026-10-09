//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestDocs_TryItAndTheReference runs the documents' projects as written:
// the README's Try it, and docs/config.example.yaml through up's verify
// gate, its programs stood in, and its cpus: line only where the user
// manager has the cpu controller.
func TestDocs_TryItAndTheReference(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	p := newProject(t, "hello", "hello", "")
	p.file("systemd-compose.yaml", tryItYAML(string(readme)))
	p.file(".env", "SYSTEMD_COMPOSE_PROJECT_NAME="+p.name+"\n")
	check(t, "README's Try it: up", p.sc("up").ok())
	check(t, "  logs has greeter's line", p.sc("logs", "--no-color").says("hello, world"))
	check(t, "  run greeter printenv NAME says world", p.sc("run", "-T", "greeter", "printenv", "NAME").says("^world"))
	check(t, "  down", p.sc("down").ok())

	ex := filepath.Join(t.TempDir(), "ex")
	bin := filepath.Join(ex, "bin")
	for _, d := range []string{filepath.Join(ex, "api"), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	example, err := os.ReadFile(filepath.Join(repo, "docs", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cpuDelegated() {
		example = regexp.MustCompile(`(?m)^ *cpus:.*\n`).ReplaceAll(example, nil)
	}
	files := map[string]string{"systemd-compose.yaml": string(example), ".env": ""}
	for _, prog := range []string{"postgres", "bookshelf-api", "bookshelf-web", "node", "curl", "pgweb"} {
		files[filepath.Join("bin", prog)] = "#!/bin/sh\n"
	}
	for f, text := range files {
		if err := os.WriteFile(filepath.Join(ex, f), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "docs/config.example.yaml passes up --dry-run, every profile",
		run(ex, []string{"PATH=" + bin + ":" + os.Getenv("PATH")}, sc, "-p", prefix+"_ex", "--profile", "*", "up", "--dry-run").ok())
}

// tryItYAML is the yaml block of the README's Try it section.
func tryItYAML(readme string) string {
	_, s, _ := strings.Cut(readme, "\n## Try it")
	_, s, _ = strings.Cut(s, "```yaml\n")
	s, _, _ = strings.Cut(s, "\n```")
	return s + "\n"
}

// TestRun_GetsTheLimits checks that a one-off gets its service's limits,
// and before the first up, when there is no project slice yet, the
// project's too.
func TestRun_GetsTheLimits(t *testing.T) {
	p := newProject(t, "lim", "lim", "name: NAME\nresources: {pids: 50}\nservices:\n  a: {command: [sleep, infinity], resources: {memory: 32M}}\n")
	capOf := func() result {
		return p.sc("run", "-T", "a", "sh", "-c", `d=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup); cat "$d/memory.max" "$d/pids.max"`)
	}
	before := capOf()
	check(t, "run before up has its service's memory cap", before.shows(`^33554432$`))
	check(t, "  and the project's pids cap", before.shows(`^50$`))
	check(t, "up of that project", p.sc("up").ok())
	check(t, "  run after up still has the service's memory cap", capOf().shows(`^33554432$`))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestLogs_ShowsAChildsLines checks that logs shows a line from a child
// that exits at once, which reaches the journal with no unit name, and
// that it reads the journal with no user bus.
func TestLogs_ShowsAChildsLines(t *testing.T) {
	p := newProject(t, "lg", "lg", "name: NAME\nservices:\n  s: {command: [sh, -c, \"while sleep 1; do /bin/echo child-line; done\"]}\n  t: {command: [sleep, infinity]}\n")
	check(t, "up of a project whose service prints through a child", p.sc("up").ok())
	time.Sleep(2 * time.Second)
	logs := p.sc("logs", "--no-color", "s")
	check(t, "  logs s shows the child's lines", logs.shows("child-line"))
	// systemd 255 says "Started UNIT - DESCRIPTION.", 249 "Started DESCRIPTION."
	check(t, "  and systemd's own line about s", logs.shows("Started "+regexp.QuoteMeta(p.name)+"(-s|: s)"))
	check(t, "  logs t shows none of them", p.sc("logs", "--no-color", "t").lacks("child-line"))
	check(t, "  logs with no user bus (cron, su) still reads the journal",
		run(p.dir, nil, "env", "-u", "XDG_RUNTIME_DIR", "-u", "DBUS_SESSION_BUS_ADDRESS", sc, "logs", "--no-color", "s").shows("child-line"))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestProbe_RunsFromACopy checks that the healthcheck probe runs from a
// copy of the binary named by its content: the binary that ran up can
// move, and an up from another copy of the same build changes nothing.
func TestProbe_RunsFromACopy(t *testing.T) {
	p := newProject(t, "pc", "pc", "name: NAME\nservices:\n  s: {command: [sleep, infinity], healthcheck: {test: [/bin/true]}}\n")
	data, err := os.ReadFile(sc)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "sc-copy")
	if err := os.WriteFile(cp, data, 0o755); err != nil {
		t.Fatal(err)
	}
	check(t, "up from a copy of the binary", run(p.dir, nil, cp, "up").ok())
	if err := os.Rename(cp, cp+"-moved"); err != nil {
		t.Fatal(err)
	}
	check(t, "  with that copy moved away, restart passes the healthcheck", p.sc("restart", "s").ok())
	check(t, "  and up from this binary finds nothing changed", noChange(p.sc("up", "--dry-run"), p.name))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestRun_AtATerminal checks one-offs at a terminal: ^C reaches the
// command through its pty, and the client says so and exits 130; a stop
// from elsewhere exits 255; a terminal whose input has ended does not
// stall the run; a command killed by a signal exits 128+N, named.
func TestRun_AtATerminal(t *testing.T) {
	p := newProject(t, "cc", "cc", "name: NAME\nservices:\n  a: {command: [sleep, infinity]}\n")
	sleeper := fmt.Sprintf("'%s' run a sleep 30; echo client-exit $?", sc)
	out := atTerminal(p.dir, sleeper, input{2 * time.Second, "\x03"}, input{4 * time.Second, ""})
	check(t, "^C of run at a terminal exits 130", that(strings.Contains(out, "client-exit 130"), "the transcript:\n%s", out))
	check(t, "  and says it was interrupted", that(strings.Contains(out, "interrupted"), "the transcript:\n%s", out))
	stopped := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Second)
		systemctl("stop", "run-"+p.name+"-*")
		close(stopped)
	}()
	out = atTerminal(p.dir, sleeper, input{6 * time.Second, ""})
	<-stopped
	check(t, "  a stop from elsewhere still exits 255", that(strings.Contains(out, "client-exit 255"), "the transcript:\n%s", out))
	// with the terminal's input already ended (ssh -t host CMD </dev/null)
	noStall := func() error {
		for i := 0; i < 3; i++ {
			r := run(p.dir, nil, "timeout", "-s", "KILL", "15", "script", "-qec", fmt.Sprintf("'%s' run a /bin/echo hi", sc), "/dev/null")
			if r.code == 137 {
				return r.fail("stalled on try %d", i+1)
			}
		}
		return nil
	}
	check(t, "  run at a terminal whose input has ended does not stall (3 tries)", noStall())
	segv := p.sc("run", "-T", "a", "sh", "-c", "kill -SEGV $$")
	check(t, "  run of a command killed by SIGSEGV exits 139", segv.exits(139))
	check(t, "  and names the signal", segv.says("killed by SIGSEGV"))
}

// TestUp_RetiresADroppedJob checks that a scheduled job dropped from the
// yaml is retired by a plain up: its armed timer runs nothing.
func TestUp_RetiresADroppedJob(t *testing.T) {
	p := newProject(t, "dj", "dj", "name: NAME\nservices:\n  s: {command: [sleep, infinity]}\n  j: {command: /bin/true, schedule: hourly}\n")
	check(t, "up of a project with a scheduled job", p.sc("up").ok())
	p.write("name: NAME\nservices:\n  s: {command: [sleep, infinity]}\n")
	check(t, "  with the job dropped, a plain up exits 0", p.sc("up").ok())
	timer := systemctl("list-units", "--all", "--plain", "--no-legend", p.unit("j", ".timer"))
	var left []string
	for _, l := range strings.Split(strings.TrimSpace(timer.out), "\n") {
		if l != "" && !strings.Contains(l, "not-found") {
			left = append(left, l)
		}
	}
	check(t, "  and its timer is retired", that(len(left) == 0, "still loaded: %q", left))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestUp_EnvFileReadAlready checks that an env_file edit the service has
// read already, by a restart by hand after it, is no change: up restarts
// neither it nor its dependents.
func TestUp_EnvFileReadAlready(t *testing.T) {
	p := newProject(t, "ev", "ev", "name: NAME\nservices:\n  s: {command: [sleep, infinity], env_file: e.env}\n  d: {command: [sleep, infinity], depends_on: {s: {restart: true}}}\n")
	p.file("e.env", "K=1\n")
	check(t, "up of a project with an env_file", p.sc("up").ok())
	time.Sleep(time.Second)
	p.file("e.env", "K=2\n")
	check(t, "  edited, then s restarted by hand", p.sc("restart", "s").ok())
	sd, dd := mainPID(p.unit("s", ".service")), mainPID(p.unit("d", ".service"))
	check(t, "  up --dry-run reads no change", noChange(p.sc("up", "--dry-run"), p.name))
	check(t, "  up exits 0", p.sc("up").ok())
	check(t, "  and restarted neither s nor its dependent", equal("the pids of s and d", mainPID(p.unit("s", ".service"))+" "+mainPID(p.unit("d", ".service")), sd+" "+dd))
	check(t, "  ps notes nothing pending", p.sc("ps").lacks("not applied"))
	check(t, "  a second up reads no change either", noChange(p.sc("up", "--dry-run"), p.name))
	time.Sleep(time.Second)
	p.file("e.env", "K=3\n")
	check(t, "  an edit not yet read is a change", p.sc("up", "--dry-run").shows(`changed \(env_file\)`))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestUp_RenamedSocketService checks that a socket-activated service
// renamed with its address comes up with up --force: the old socket and
// service are retired before the new socket binds the address.
func TestUp_RenamedSocketService(t *testing.T) {
	yaml, port := "name: NAME\nservices:\n  %s: {command: [sleep, infinity], listen: [\"127.0.0.1:%d\"]}\n", freePort(t)
	p := newProject(t, "rnm", "rnm", fmt.Sprintf(yaml, "web", port))
	check(t, "up of a socket-activated service", p.sc("up").ok())
	p.write(fmt.Sprintf(yaml, "web2", port))
	check(t, "  renamed, keeping its address: up --force exits 0", p.sc("up", "--force").ok())
	check(t, "  and the new socket listens", active(p.unit("web2", ".socket")))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestProfiles_RequiredByAnother checks a yaml whose service requires a
// profile's service: without the profile, ps and down work, and up says
// which profile it needs.
func TestProfiles_RequiredByAnother(t *testing.T) {
	p := newProject(t, "np", "np", "name: NAME\nservices:\n  web: {command: [sleep, infinity], depends_on: {dbg: {required: true}}}\n  dbg: {command: [sleep, infinity], profiles: [dbg]}\n")
	check(t, "--profile dbg up of a project that needs it", p.sc("--profile", "dbg", "up").ok())
	check(t, "  ps without the profile exits 0 and reads the yaml", p.sc("ps").lacks("does not load|has not applied"))
	check(t, "  up without it names the profile", p.sc("up").says("add --profile dbg"))
	check(t, "  down without it exits 0", p.sc("down").ok())
	check(t, "  and nothing of it stays registered", equal("entries", p.registered(), 0))
}

// TestUp_WaitsForAServiceStartingAlready checks that up says what it waits
// for when a healthchecked service was starting already.
func TestUp_WaitsForAServiceStartingAlready(t *testing.T) {
	p := newProject(t, "hs", "hs", "name: NAME\nservices:\n  s: {command: [sleep, infinity], healthcheck: {test: [test, -f, ready], start_period: 30s}}\n")
	p.file("ready", "")
	check(t, "up of a healthchecked service", p.sc("up").ok())
	os.Remove(p.path("ready"))
	systemctl("restart", "--no-block", p.unit("s", ".service"))
	time.Sleep(time.Second)
	ready := make(chan struct{})
	go func() {
		time.Sleep(4 * time.Second)
		os.WriteFile(p.path("ready"), nil, 0o644)
		close(ready)
	}()
	check(t, "  up while it is starting again says it waits for it", p.sc("up").says("starting already; waiting for its healthcheck"))
	<-ready
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestStart_NamesStoppedDependents checks that start of a dependency names
// every dependent still stopped, a restart: true one too.
func TestStart_NamesStoppedDependents(t *testing.T) {
	p := newProject(t, "df", "df", "name: NAME\nservices:\n  db: {command: [sleep, infinity]}\n  api: {command: [sleep, infinity], depends_on: {db: {required: true}}}\n  wk: {command: [sleep, infinity], depends_on: {db: {restart: true}}}\n")
	check(t, "up of a project with both kinds of dependents", p.sc("up").ok())
	check(t, "  stop db", p.sc("stop", "db").ok())
	check(t, "  start db names the restart: true dependent still stopped", p.sc("start", "db").shows(regexp.QuoteMeta(p.unit("wk", ".service"))+" .*still stopped"))
	check(t, "  down exits 0", p.sc("down").ok())
}

// TestYamlGone_PointsTheWayBack checks that a directory whose project's
// yaml and rendered files are gone points the way back, since the verbs
// there act on the user instance. Links, whose files are what is gone;
// TestRegistration_CopiesLoadWithoutTheProject has the copies.
func TestYamlGone_PointsTheWayBack(t *testing.T) {
	p := newProject(t, "yg", "yg", "name: NAME\nregistration: link\nservices:\n  a: {command: [sleep, infinity]}\n")
	away := filepath.Dir(p.dir)
	moves := [][2]string{
		{p.path("systemd-compose.yaml"), filepath.Join(away, "yg.yaml")},
		{p.path(".systemd-compose"), filepath.Join(away, "yg.render")},
	}
	putBack := func() {
		for _, m := range moves {
			if exists(m[1]) == nil {
				os.Rename(m[1], m[0])
			}
		}
	}
	t.Cleanup(putBack)
	check(t, "up of a project", p.sc("up").ok())
	for _, m := range moves {
		if err := os.Rename(m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "  with its yaml and files gone, a verb there says how to get back", p.sc("ps").says("registered from .*whose systemd-compose.yaml is gone"))
	check(t, "  and ls says to put the yaml back", run("", nil, sc, "ls").shows(regexp.QuoteMeta(p.name)+" .*yaml and rendered files gone"))
	putBack()
	check(t, "  put back, down exits 0", p.sc("down").ok())
}

// TestProjectName_FromAnywhere checks that -p NAME outside a project acts
// on the project registered under NAME, as compose's -p does, and that
// it names a project registered with links to files it cannot read.
func TestProjectName_FromAnywhere(t *testing.T) {
	p := newProject(t, "pa", "pa", "name: NAME\nregistration: link\nservices:\n  a: {command: [sleep, infinity]}\n")
	elsewhere := t.TempDir()
	check(t, "up of a project", p.sc("up").ok())
	check(t, "  -p NAME ps elsewhere shows its units", run(elsewhere, nil, sc, "-p", p.name, "ps").shows(regexp.QuoteMeta(p.unit("a", ".service"))+` +loaded +active`))
	moved := p.dir + ".moved"
	if err := os.Rename(p.dir, moved); err != nil {
		t.Fatal(err)
	}
	check(t, "  moved away, -p NAME says its files cannot be read", run(elsewhere, nil, sc, "-p", p.name, "ps").says("registered from .*whose files cannot be read"))
	if err := os.Rename(moved, p.dir); err != nil {
		t.Fatal(err)
	}
	check(t, "  put back, -p NAME down elsewhere exits 0", run(elsewhere, nil, sc, "-p", p.name, "down").ok())
	check(t, "  and unregisters it", equal("entries", p.registered(), 0))
	check(t, "-p with no such project is refused", run(elsewhere, nil, sc, "-p", p.name, "ps").says("no project "+p.name+" is registered"))
}

// TestStop_SendSIGKILLNo checks that a service SendSIGKILL=no leaves
// running past its stop timeout is not reported stopped.
func TestStop_SendSIGKILLNo(t *testing.T) {
	p := newProject(t, "sk2", "sk2", `name: NAME
services:
  s:
    command: [sh, -c, "trap \"\" TERM; while :; do sleep 1; done"]
    unit: {Service: {SendSIGKILL: "no", TimeoutStopSec: "2s"}}
`)
	kill := func() { systemctl("kill", "-s", "KILL", p.unit("s", ".service")) }
	t.Cleanup(kill)
	check(t, "up of a service that ignores SIGTERM, under SendSIGKILL=no", p.sc("up").ok())
	check(t, "  stop of it exits nonzero and says it is not stopped", p.sc("stop", "s").says("NOT stopped"))
	kill()
	time.Sleep(time.Second)
	check(t, "  once killed, down exits 0", p.sc("down").ok())
}

// TestUnit_AsSystemdReadsIt checks unit: and drop-ins as systemd reads
// them: a oneshot's several ExecStart= lines, set-property's drop-in, a
// drop-in with a deprecated setting, a project named like one of systemd's
// own units, and an address given twice.
func TestUnit_AsSystemdReadsIt(t *testing.T) {
	p := newProject(t, "ux", "ux", "name: NAME\nservices:\n  s: {command: [sleep, infinity]}\n  o:\n    oneshot: true\n    unit: {Service: {ExecStart: [/bin/touch DIR/one, /bin/touch DIR/two]}}\n")
	control := filepath.Join(unitDir+".control", p.unit("s", ".service.d"))
	dropIns := filepath.Join(unitDir, p.name+"-.service.d")
	t.Cleanup(func() {
		os.RemoveAll(control)
		os.RemoveAll(dropIns)
		systemctl("daemon-reload")
	})
	check(t, "up of a oneshot with two ExecStart= lines", p.sc("up").ok())
	check(t, "  and both ran", firstErr(exists(p.path("one")), exists(p.path("two"))))
	systemctl("set-property", p.unit("s", ".service"), "MemoryMax=64M")
	check(t, "  set-property's drop-in is named at up", p.sc("up").says("settings from systemctl set-property"))
	os.RemoveAll(control)
	systemctl("daemon-reload")
	if err := os.MkdirAll(dropIns, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropIns, "old.conf"), []byte("[Service]\nKillMode=none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(t, "  a drop-in with a deprecated setting is a note, not a refusal", p.sc("up").says("deprecates, but still honours"))
	os.RemoveAll(dropIns)
	systemctl("daemon-reload")
	check(t, "  down exits 0", p.sc("down").ok())

	q := newProject(t, "ux2", "ux2", "")
	q.write(fmt.Sprintf("name: NAME\nservices:\n  a: {command: [sleep, infinity], listen: [\"127.0.0.1:%[1]d\"], unit: {Socket: {ListenStream: \"127.0.0.1:%[1]d\"}}}\n", freePort(t)))
	check(t, "an address in listen: and ListenStream= both is refused", q.sc("config").says("listed already"))
}

// TestImport_RunsTheUnitAsAProjectsService checks import's round trip: a
// hand-written unit, imported and retired by the commands import prints,
// runs the same command, in the same directory, with the same environment,
// as a project's service.
func TestImport_RunsTheUnitAsAProjectsService(t *testing.T) {
	unit := prefix + "_hand.service"
	out := filepath.Join(t.TempDir(), "out")
	file := writeUnit(t, unit, fmt.Sprintf(`[Unit]
Description=a hand-written unit
[Service]
Environment="A=one two" B=$HOME
Environment=C=3
ExecStart=/bin/sh -c 'echo "$A|$B|$C|$(pwd)" > %s; exec sleep infinity'
Nice=5
[Install]
WantedBy=default.target
`, out))
	t.Cleanup(func() {
		systemctl("disable", "--now", unit)
		os.Remove(file)
		systemctl("daemon-reload")
	})
	systemctl("daemon-reload")
	check(t, "the hand-written unit starts", systemctl("enable", "--now", unit).ok())
	before := waitFile(out)
	check(t, "  and writes its line", that(before != "", "no %s", out))
	os.Remove(out)

	p := newProject(t, "imp", "imp", "")
	imp := run(p.dir, nil, sc, "import", unit)
	check(t, "import exits 0", imp.ok())
	p.file("systemd-compose.yaml", "name: "+p.name+"\n"+imp.out)
	check(t, "  prints the steps that retire the unit", equal("steps", runRetireSteps(t, imp.out), 3))
	check(t, "  which leave no unit file", missing(file))
	check(t, "up of the import", p.sc("up").ok())
	check(t, "  runs the same command the same way", equal("its line", waitFile(out), before))
	check(t, "  with the unit's settings", equal("Nice", property(p.unit(strings.TrimSuffix(unit, ".service"), ".service"), "Nice"), "5"))
}

func TestImport_RetiresTheTimerThatStartsIt(t *testing.T) {
	unit := prefix + "_job"
	files := []string{writeUnit(t, unit+".service", "[Service]\nType=oneshot\nExecStart=/bin/true\n"),
		writeUnit(t, unit+".timer", "[Timer]\nOnCalendar=*-*-* 03:30:00\nPersistent=true\n[Install]\nWantedBy=timers.target\n")}
	t.Cleanup(func() {
		systemctl("disable", "--now", unit+".timer")
		for _, f := range files {
			os.Remove(f)
		}
		systemctl("daemon-reload")
	})
	systemctl("daemon-reload")
	check(t, "the hand-written timer is enabled", systemctl("enable", "--now", unit+".timer").ok())

	imp := run("", nil, sc, "import", unit+".service")
	check(t, "import shows the timer's own text", imp.shows(`^#   \[Timer\]\n#   OnCalendar=\*-\*-\* 03:30:00\n#   Persistent=true$`))
	check(t, "  prints the steps that retire the unit and its timer", equal("steps", runRetireSteps(t, imp.out), 4))
	check(t, "  which leave neither file", errors.Join(missing(files[0]), missing(files[1])))
	check(t, "  nor the timer's enablement", missing(filepath.Join(unitDir, "timers.target.wants", unit+".timer")))
	check(t, "  nor a timer to fail at the next boot", equal("LoadState", property(unit+".timer", "LoadState"), "not-found"))
}

// writeUnit writes a hand-written unit file into the unit directory, and
// says where.
func writeUnit(t *testing.T, name, text string) string {
	t.Helper()
	file := filepath.Join(unitDir, name)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// runRetireSteps runs the shell steps import prints after its yaml, and
// says how many there were.
func runRetireSteps(t *testing.T, out string) int {
	t.Helper()
	_, steps, _ := strings.Cut(out, "# To move it into a project")
	n := 0
	for _, l := range strings.Split(steps, "\n") {
		if cmd, ok := strings.CutPrefix(l, "#   "); ok && !strings.HasPrefix(cmd, "(") && !strings.HasPrefix(cmd, "systemd-compose ") {
			n++
			check(t, "  its step "+cmd, run("", nil, "sh", "-c", cmd).ok())
		}
	}
	return n
}

// waitFile is the file's text once it has a line, or "" after 10s.
func waitFile(path string) string {
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			return string(b)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// TestHealthcheck_ChecksWhileItRuns checks compose's healthcheck after the
// start: retries failed checks in a row make ps say unhealthy, with what
// the last one printed, and logs say so; one pass makes it healthy again.
// Nothing restarts it for that. The checks run in the service's cgroup and
// end with it, their state too.
func TestHealthcheck_ChecksWhileItRuns(t *testing.T) {
	p := newProject(t, "hw", "hw", "name: NAME\nservices:\n  s:\n    command: [sh, -c, 'trap \"\" HUP; exec sleep infinity']\n"+
		"    healthcheck: {test: [sh, -c, 'test -f ok || { echo no ok file; echo on two lines; exit 3; }'], interval: 1s, retries: 2, start_interval: 1s}\n")
	p.file("ok", "")
	unit := p.unit("s", ".service")
	row := regexp.QuoteMeta(unit) + ` +loaded +active +running +`
	psUntil := func(pattern string) error { // checks run each second
		var r result
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
			if r = p.sc("ps"); r.shows(pattern) == nil {
				return nil
			}
		}
		return r.shows(pattern)
	}
	health := filepath.Join(runtimeDir(), "systemd-compose", "health")
	check(t, "up", p.sc("up").ok())
	check(t, "  ps says healthy", p.sc("ps").shows(row+`healthy +copied$`))
	check(t, "  and the run's state is kept", exists(filepath.Join(health, property(unit, "InvocationID"))))
	cg := "/sys/fs/cgroup" + property(unit, "ControlGroup") + "/cgroup.procs"
	watches := func() (n int) {
		data, _ := os.ReadFile(cg)
		for _, pid := range strings.Fields(string(data)) {
			if cmd, _ := os.ReadFile("/proc/" + pid + "/cmdline"); strings.Contains(string(cmd), "probe\x00--watch\x00") {
				n++
			}
		}
		return n
	}
	check(t, "  the checks run in the service's cgroup", equal("watches", watches(), 1))
	pid := mainPID(unit)
	check(t, "  a HUP sent to the service (its whole cgroup)", p.sc("kill", "-s", "HUP", "s").says("sent SIGHUP"))
	time.Sleep(time.Second)
	check(t, "  leaves the checks running", firstErr(equal("watches", watches(), 1), p.sc("ps").shows(row+`healthy +copied$`)))

	os.Remove(p.path("ok"))
	check(t, "two failed checks in a row: ps says unhealthy, since when, and what the last printed", psUntil(row+`unhealthy +copied \(unhealthy since [0-9:]+: 2 checks failed in a row, the last: exit status 3; its output: no ok file on two lines; `))
	check(t, "  logs say so", p.sc("logs", "s").says(`probe: unhealthy: 2 checks failed in a row, the last: exit status 3; its output: no ok file on two lines`))
	check(t, "  and nothing restarted it", equal("MainPID", mainPID(unit), pid))
	p.file("ok", "")
	check(t, "one pass: healthy again", psUntil(row+`healthy +copied$`))
	check(t, "  logs say so", p.sc("logs", "s").says(`probe: healthy again, after [0-9]+ failed checks`))

	before := filepath.Join(health, property(unit, "InvocationID"))
	check(t, "restart", p.sc("restart", "s").ok())
	check(t, "  the new run is checked, by one watch", firstErr(p.sc("ps").shows(row+`healthy +copied$`), equal("watches", watches(), 1)))
	check(t, "  and the last run's state is gone", missing(before))
	now := filepath.Join(health, property(unit, "InvocationID"))
	check(t, "stop", p.sc("stop", "s").ok())
	check(t, "  ends the checks and takes their state along", missing(now))
}
