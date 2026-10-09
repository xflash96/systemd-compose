//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A job and its runs: run while the timer's run goes on says it is a
// second copy. A job turned into a long-running service while a run goes
// on is restarted into it, since try-restart passes over a unit that is
// still starting, which would then run its old definition.
func TestJob_RunWhileItRunsAndTurnedIntoAService(t *testing.T) {
	p := newProject(t, "g", "g", `name: NAME
services:
  job:
    command: [sh, -c, "sleep 6"]
    schedule: "2099-01-01 00:00:00"
`)
	job := p.unit("job", ".service")
	check(t, "up of a project with a scheduled job", p.sc("up").ok())
	systemctl("start", "--no-block", job)
	time.Sleep(time.Second)
	check(t, "  run of the job while its run goes on says it is a second copy", p.sc("run", "job").says("running its job now"))
	systemctl("start", "--no-block", job)
	time.Sleep(time.Second)
	p.write(`name: NAME
services:
  job:
    command: [sh, -c, "exec sleep infinity"]
`)
	check(t, "  the job turned into a service while it runs: up exits 0", p.sc("up").ok())
	check(t, "  and the service runs its new definition", p.sc("ps").says("^"+regexp.QuoteMeta(job)+` .* active *running`))
	check(t, "  down of that project exits 0", p.sc("down").ok())
}

// A changed oneshot still running when up comes is restarted without
// blocking before the start, so the rest of the project starts while it
// runs, and up says what it waits for.
func TestUp_RestartsAChangedOneshotStillRunning(t *testing.T) {
	p := newProject(t, "g2", "g2", `name: NAME
services:
  job:
    oneshot: true
    command: [./run]
`)
	p.file("dur", "0\n")
	p.file("run", "#!/bin/sh\nexec sleep \"$(cat dur)\"\n")
	if err := os.Chmod(p.path("run"), 0o755); err != nil {
		t.Fatal(err)
	}
	check(t, "up of a project with a quick oneshot", p.sc("up").ok())
	p.file("dur", "300\n")
	systemctl("restart", "--no-block", p.unit("job", ".service"))
	time.Sleep(time.Second)
	p.write(`name: NAME
services:
  job:
    oneshot: true
    command: [./run, changed]
  w:
    command: [sleep, infinity]
`)
	wait, sofar := background("", "timeout", "-s", "INT", "25", sc, "-f", p.path("systemd-compose.yaml"), "up")
	time.Sleep(12 * time.Second)
	check(t, "  with the oneshot still running and changed, up starts the new service", active(p.unit("w", ".service")))
	check(t, "  and says what it waits for", result{cmd: "up", all: sofar()}.says("still starting"))
	wait()
	check(t, "  down of that project exits 0", p.sc("down").ok())
}

// A ^C at a terminal reaches a one-off's whole process group, so a build
// is stopped, not left to finish after it said it did not.
func TestBuild_InterruptedAtATerminal(t *testing.T) {
	p := newProject(t, "b", "b", `name: NAME
resources: {memory: 256M}
services:
  a:
    command: [sleep, infinity]
    build:
      run: ["sh -c \"sleep 8; mkdir -p dist\""]
      creates: dist
`)
	transcript := atTerminal(p.dir, "'"+sc+"' build", input{3 * time.Second, "\x03"}, input{time.Second, ""})
	time.Sleep(9 * time.Second)
	check(t, "a build ^C'd with its process group says so", that(match("interrupted", transcript), "the terminal showed:\n%s", transcript))
	check(t, "  and its step is stopped, not finished", missing(p.path("dist")))
	check(t, "  and no build unit is left", equal("build units", strings.TrimSpace(systemctl("list-units", "--all", "--plain", "--no-legend", "build-"+p.name+"-*").out), ""))
}

// A project unit still running that nothing registers (its link removed
// by hand) fails down, which names it, and says what it did.
func TestDown_FailsForAStrayUnit(t *testing.T) {
	p := newProject(t, "st", "st", `name: NAME
services:
  s: {command: [sleep, infinity]}
`)
	target, s := p.name+".target", p.unit("s", ".service")
	t.Cleanup(func() { systemctl("stop", s, target) })
	check(t, "up of a project", p.sc("up").ok())
	systemctl("disable", target)
	r := p.sc("down")
	check(t, "  down with its target unlinked by hand but active exits nonzero and names it", firstErr(r.fails(), r.says(regexp.QuoteMeta(target)+" .*still running, though nothing registers it")))
	systemctl("stop", target)
	check(t, "  and once it is stopped, down exits 0", p.sc("down").ok())
	check(t, "up of it again", p.sc("up").ok())
	systemctl("disable", s)
	r = p.sc("down")
	check(t, "  down with its service unlinked by hand but running exits nonzero, says so, and what it did", firstErr(r.fails(), r.says("still running, though nothing registers it"), r.says(`^down: .*unregistered`)))
	systemctl("stop", s)
	check(t, "  and once it is stopped, down exits 0", p.sc("down").ok())
}

// A project named run renders run-SERVICE.service, the shape of another
// project's one-offs: the other project's down leaves it running.
func TestNames_ProjectNamedRun(t *testing.T) {
	// the name run is not the tests' own: skipped where a project holds it
	if exists(filepath.Join(unitDir, "run.target")) == nil || exists(filepath.Join(unitDir, "run.slice")) == nil {
		t.Skip("a project named run is registered on this user manager")
	}
	rn := newProject(t, "rn", "", `name: run
services:
  NAME_o-x: {command: [sleep, infinity]}
`)
	o := newProject(t, "o", "o", `name: NAME
services:
  s: {command: [sleep, infinity]}
`)
	check(t, "up of a project named run", rn.sc("up").ok())
	check(t, "up of a project its service name begins with", o.sc("up").ok())
	check(t, "  whose down", o.sc("down").ok())
	check(t, "  leaves the other project's service running", active("run-"+o.name+"-x.service"))
	check(t, "  down of the project named run exits 0", rn.sc("down").ok())
}

// A dashed project name is escaped in its units, as systemd-escape writes
// it. Unescaped, project NAME-kit's web would be project NAME's kit-web,
// and its slice would sit inside NAME.slice, so NAME's down would stop it.
func TestNames_DashedProject(t *testing.T) {
	plain := newProject(t, "plain", "", `name: NAME
services:
  kit-web: {command: [sleep, infinity]}
`)
	dashed := newProject(t, "dashed", "", "")
	dashed.name = plain.name + "-kit"
	dashed.write("name: NAME\nservices:\n  web: {command: [sleep, infinity]}\n")
	esc := dashed.escaped()
	check(t, "up of a dashed project", dashed.sc("up").ok())
	check(t, "  runs NAME\\x2dkit-web.service", active(dashed.unit("web", ".service")))
	cg := property(esc+".slice", "ControlGroup")
	check(t, "  in a slice of its own, not inside NAME.slice", that(strings.HasSuffix(cg, "/"+esc+".slice") && !strings.Contains(cg, "/"+plain.name+".slice/"), "ControlGroup %s", cg))
	check(t, "up of project NAME, whose kit-web would share the unit name unescaped", plain.sc("up").ok())
	check(t, "  runs its own", active(plain.unit("kit-web", ".service")))
	check(t, "ls shows the dashed name as written", run("", nil, sc, "ls").shows(regexp.QuoteMeta(dashed.name)+` +running 1/1 `))
	check(t, "a one-off in the dashed project", dashed.sc("run", "-T", "web", "echo", "one-off ran").shows("one-off ran"))
	// one left running is found by its unit's name, the project spelt
	// escaped there too: ps lists it, NAME's down leaves it, its own stops it
	wait, _ := background(dashed.dir, "timeout", "-s", "KILL", "60", sc, "run", "-T", "web", "sleep", "30")
	running := func() string { return loaded("run-" + esc + "-") }
	for i := 0; i < 50 && running() == ""; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	check(t, "a one-off left running in the dashed project", that(running() != "", "no run-%s- unit is loaded", esc))
	check(t, "  ps lists it", dashed.sc("ps").shows(`a one-off .*still running`))
	check(t, "down of project NAME", plain.sc("down").ok())
	check(t, "  leaves the dashed project running", active(dashed.unit("web", ".service")))
	check(t, "  and its one-off", that(running() != "", "the dashed project's one-off was stopped"))
	check(t, "down of the dashed project", dashed.sc("down").ok())
	check(t, "  stops its one-off", that(running() == "", "still loaded: %s", running()))
	wait()
	check(t, "  leaves no link", equal("links", dashed.links(), 0))
}

// A verb given service names fails when one was skipped as not
// registered; stop and kill fail only for a name that runs but is not the
// project's, and leave it running.
func TestVerbs_NamedServicesNotRegistered(t *testing.T) {
	p := newProject(t, "pf", "pf", `name: NAME
services:
  web: {command: [sleep, infinity]}
  dbg: {command: [sleep, infinity], profiles: [dbg]}
`)
	dbg := p.unit("dbg", ".service")
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share")
	}
	dataDir = filepath.Join(dataDir, "systemd", "user")
	handWritten := []string{filepath.Join(unitDir, dbg), filepath.Join(dataDir, dbg)}
	t.Cleanup(func() {
		systemctl("stop", dbg)
		for _, f := range handWritten {
			os.Remove(f)
		}
		systemctl("daemon-reload")
	})
	// a unit of the project's name, written by hand and started
	handStart := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("[Service]\nExecStart=/bin/sleep infinity\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		systemctl("daemon-reload")
		systemctl("start", dbg)
	}
	handStop := func(path string) {
		systemctl("stop", dbg)
		os.Remove(path)
		systemctl("daemon-reload")
	}

	check(t, "up of a project with a profile off", p.sc("up").ok())
	check(t, "  start web dbg exits nonzero, dbg not registered", p.sc("start", "web", "dbg").fails())
	check(t, "  stop web dbg exits 0: what is not registered is stopped, as asked", p.sc("stop", "web", "dbg").ok())
	check(t, "  stop dbg alone exits 0: nothing of it is registered or running", p.sc("stop", "dbg").ok())
	check(t, "  kill dbg alone exits 0, likewise", p.sc("kill", "dbg").ok())
	handStart(handWritten[0])
	check(t, "  stop web dbg with dbg a hand-written unit exits nonzero", p.sc("stop", "web", "dbg").fails())
	check(t, "  stop dbg alone, the hand-written one running, exits nonzero", p.sc("stop", "dbg").fails())
	check(t, "  and leaves the hand-written one running", active(dbg))
	handStop(handWritten[0])
	// the same unit loaded from another unit directory reads "not registered"
	handStart(handWritten[1])
	check(t, "  and with it loaded from another unit directory, running, too", p.sc("stop", "web", "dbg").fails())
	handStop(handWritten[1])
	check(t, "  a bare start still exits 0", p.sc("start").ok())
	check(t, "  down exits 0", p.sc("down").ok())
	check(t, "  stop of the project, now down, exits 0", p.sc("stop").ok())
}

// run names a missing env_file, which systemd-run would fail on with exit
// 1 and no word. A .env that is a FIFO is refused by every verb at once,
// rather than wait for a writer.
func TestRun_NamesEnvFileProblems(t *testing.T) {
	p := newProject(t, "ef", "ef", "name: NAME\nservices:\n  a: {command: [sleep, infinity], env_file: missing.env}\n")
	check(t, "run with its env_file missing names the file", p.sc("run", "-T", "a", "true").says(`env_file .*missing\.env`))
	if err := syscall.Mkfifo(p.path(".env"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(t, "a project whose .env is a FIFO is refused at once", run(p.dir, nil, "timeout", "-s", "KILL", "10", sc, "ps").says(`\.env is not a regular file`))
	os.Remove(p.path(".env"))
}

// A service stopped from elsewhere while up waits for its healthcheck
// fails up, and up blames neither the healthcheck nor the service's type;
// stopped by systemctl stop, which leaves it failed, up says it was
// stopped from elsewhere.
func TestUp_ServiceStoppedFromElsewhere(t *testing.T) {
	p := newProject(t, "h", "h", `name: NAME
services:
  slow:
    command: [sh, -c, "while true; do sleep 5; done"]
    healthcheck: {test: [sh, -c, "test -f DIR/ok"], interval: 2s, timeout: 5s, start_period: 30s}
`)
	wait, _ := background(p.dir, sc, "up")
	time.Sleep(6 * time.Second)
	p.sc("stop")
	up := wait()
	check(t, "up whose service is stopped from elsewhere while it waits exits nonzero", up.fails())
	check(t, "  and gives no oneshot advice for it", up.lacks("wants oneshot"))
	check(t, "  and does not blame the healthcheck", up.lacks("healthcheck did not pass"))
	check(t, "  down of that project exits 0", p.sc("down").ok())
	wait, _ = background(p.dir, sc, "up")
	time.Sleep(6 * time.Second)
	systemctl("stop", p.unit("slow", ".service"))
	up = wait()
	check(t, "  stopped by systemctl stop while up waits, up says so", up.says("stopped from elsewhere"))
	check(t, "  down of that project exits 0 again", p.sc("down").ok())
}

// A limit the user manager cannot enforce is refused: cpus: where it has
// no cpu controller (systemd 249 delegates memory and pids only).
func TestResources_CPUNeedsDelegation(t *testing.T) {
	p := newProject(t, "c", "c", `name: NAME
resources: {cpus: 0.5}
services:
  a: {command: [sleep, infinity]}
`)
	if cpuDelegated() {
		check(t, "cpus: where the cpu controller is delegated plans", p.sc("up", "--dry-run").ok())
	} else {
		check(t, "cpus: where the cpu controller is not delegated is refused", p.sc("up", "--dry-run").says("cpu cgroup controller is not delegated"))
	}
}
