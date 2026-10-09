//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A service that fails at once makes up exit nonzero and name it; a quick
// command runs once per up, not twice; a build step's % reaches it as
// written; down stops before it unregisters, so a 2s stop timeout holds
// rather than systemd's 90s default (the rule: unregister in
// internal/project/orphans.go).
func TestUp_NamesAFailedService(t *testing.T) {
	p := newProject(t, "q", "q", `name: NAME
services:
  once:
    command: [sh, -c, 'echo ran >> DIR/count']
    build: {run: ["sh -c 'date +%s > stamp'"], creates: stamp}
  boom:
    command: [sh, -c, 'exit 3']
  stubborn:
    command: [sh, -c, 'trap "" TERM; while :; do sleep 1; done']
    unit: {Service: {TimeoutStopSec: 2s}}
`)
	check(t, "up with a service that fails exits nonzero", p.sc("up").fails())
	ran, _ := os.ReadFile(p.path("count"))
	check(t, "  a quick command ran once", equal("lines in count", strings.Count(string(ran), "\n"), 1))
	check(t, "  and up names the failed service", p.sc("up").says(`boom\.service failed to start`))
	check(t, "  a build step's date +%s ran as written", fileMatches(p.path("stamp"), `^[0-9]+$`))
	check(t, "  start of the failing service exits nonzero", p.sc("start", "boom").fails())
	check(t, "  start of the quick one says it ran", p.sc("start", "once").says(`ran and exited \(status 0\)`))
	begun := time.Now()
	down := p.sc("down")
	check(t, "  down of that project exits 0, within the stop timeout", firstErr(down.ok(), that(time.Since(begun) < 30*time.Second, "down took %s", time.Since(begun))))
}

// One yaml under a second name, a moved directory, a render directory
// wiped, a yaml that does not load: d requires w.
func TestCopies_SecondNameMovedDirWipedRender(t *testing.T) {
	writer := func(sleep string) string {
		return fmt.Sprintf(`name: NAME
services:
  w: {command: sleep %s}
  d:
    command: sleep infinity
    depends_on: {w: {condition: service_started, required: true}}
`, sleep)
	}
	p := newProject(t, "r", "r", writer("infinity"))
	second := prefix + "_s"
	t.Cleanup(func() { p.sc("-p", second, "down") })

	check(t, "up of a third project", p.sc("up").ok())
	check(t, "  under a second name, up notes the first", p.sc("-p", second, "up").says("also registered as project "+p.name))
	check(t, "  and -p down takes the second down", p.sc("-p", second, "down").ok())
	moved := p.dir + "-moved"
	if err := os.Rename(p.dir, moved); err != nil {
		t.Fatal(err)
	}
	check(t, "  down in the moved directory exits nonzero", run(moved, nil, sc, "down").fails())
	if err := os.Rename(moved, p.dir); err != nil {
		t.Fatal(err)
	}
	check(t, "  down after moving it back exits 0", p.sc("down").ok())
	check(t, "up of the third project again", p.sc("up").ok())
	p.write(writer("86400"))
	// row grammar
	check(t, "  the plan says d restarts along with w", p.sc("up", "--dry-run").says("^  "+p.name+`-d\.service *unchanged *restarts along with `+p.name+`-w\.service`))
	p.write(writer("infinity"))
	os.RemoveAll(p.path(".systemd-compose"))
	systemctl("daemon-reload")
	check(t, "  with its render directory wiped, ps shows w running", p.sc("ps").says("^"+p.name+`-w\.service .* active *running`))
	check(t, "  and down unregisters it", p.sc("down").ok())
	check(t, "  every link is gone", equal("links", p.links(), 0))
	check(t, "up of the third project once more", p.sc("up").ok())
	f, err := os.OpenFile(p.path("systemd-compose.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("  broken: [\n")
	f.Close()
	// The verbs a broken yaml still allows act on the units registered from
	// it, through their own code.
	check(t, "  with a yaml that does not load, ps shows w running", p.sc("ps").shows("^"+p.name+`-w\.service .* active *running`))
	// 255 says "Started UNIT - DESCRIPTION.", 249 "Started DESCRIPTION."
	check(t, "  logs w shows systemd's line about w", p.sc("logs", "--no-color", "w").shows("Started "+p.name+`(-w|: w)`))
	stop := p.sc("stop", "w")
	check(t, "  stop w stops d along, and says so", firstErr(stop.ok(), stop.says(p.name+`-d\.service +stopped too, through a dependency`), stop.says("once the yaml loads")))
	check(t, "  with a yaml that does not load, down unregisters it", p.sc("down").says("as registered from it"))
	check(t, "  every link is gone", equal("links", p.links(), 0))
	p.write(writer("infinity"))
}

// A crash loop whose restart delay hides it from one look is caught, a
// service that fails once and then stays up is not, and a % that no letter
// follows reaches the program as written.
func TestUp_CatchesADelayedCrashLoop(t *testing.T) {
	p := newProject(t, "w", "w", `name: NAME
services:
  loop:
    command: [sh, -c, 'sleep 1; exit 1']
    restart: {policy: always, delay: 2s}
  retry:
    command: [sh, -c, 'test -f DIR/tried || { touch DIR/tried; exit 1; }; exec sleep infinity']
    restart: {policy: on-failure, delay: 1s}
  pct:
    command: [sh, -c, 'echo "pct=%{x} 100% done"; exec sleep infinity']
`)
	up := p.sc("up")
	check(t, "up of a project with a delayed crash loop exits nonzero", up.fails())
	check(t, "  and names the loop", up.says(p.name+`-loop\.service keeps restarting`))
	check(t, "  and not the service that came up on its retry", up.lacks("^systemd-compose: "+p.name+"-retry"))
	check(t, "  a %{ and a lone % reach the program as written", p.sc("logs", "--no-log-prefix", "pct").shows(regexp.QuoteMeta("pct=%{x} 100% done")))
	check(t, "  down of that project exits 0", p.sc("down").ok())
}

// The render directory is the tool's own: a symlink planted at a file it
// writes is refused rather than followed, and so is a directory others
// can write, which could hold such a symlink.
func TestRenderDir_IsTheToolsOwn(t *testing.T) {
	p := newProject(t, "sy", "sy", "name: NAME\nservices:\n  s: {command: [sleep, infinity]}\n")
	render := p.path(".systemd-compose")
	if err := os.Mkdir(render, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(render, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	check(t, "up with a symlink at a file it writes is refused", p.sc("up").says("is not a regular file"))
	check(t, "  and the file it points at is untouched", fileMatches(victim, "must survive"))
	os.Remove(filepath.Join(render, ".gitignore"))
	os.Chmod(render, 0o777)
	check(t, "up with a render directory others can write is refused", p.sc("up").says("can be written by others"))
	check(t, "  and up --dry-run says so too", p.sc("up", "--dry-run").says("can be written by others"))
	os.Chmod(render, 0o755)
	check(t, "  and with it fixed, up exits 0", p.sc("up").ok())
	check(t, "  down exits 0", p.sc("down").ok())
}

// down of a socket-activated project with a connection waiting in the
// backlog stops the socket and the service apart, so the stop succeeds and
// down unregisters everything.
func TestDown_SocketWithAWaitingConnection(t *testing.T) {
	port := freePort(t)
	p := newProject(t, "sk", "sk", fmt.Sprintf("name: NAME\nservices:\n  s:\n    command: [sleep, infinity]\n    listen: %d\n", port))
	check(t, "up of a socket-activated project", p.sc("up").ok())
	if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		t.Logf("connect to %d: %v", port, err)
	} else {
		go func() { time.Sleep(15 * time.Second); conn.Close() }()
	}
	time.Sleep(2 * time.Second)
	check(t, "  down with a connection waiting exits 0", p.sc("down").ok())
	check(t, "  and unregisters it all", equal("links", p.links(), 0))
	systemctl("stop", p.unit("s", ".service"), p.unit("s", ".socket"), p.name+".slice")
}

// A service that dies two seconds in, under restart: on-failure, fails the
// up that starts it: a single sample at 2s would see it alive.
func TestUp_FailsAServiceThatDiesLate(t *testing.T) {
	p := newProject(t, "k", "k", `name: NAME
services:
  svc:
    command: [sh, -c, "sleep 2; exit 1"]
    restart: on-failure
  calm:
    command: [sleep, infinity]
`)
	check(t, "up of a service that dies two seconds in exits nonzero", p.sc("up").fails())
	check(t, "  down of that project exits 0", p.sc("down").ok())
}

// In a directory with a space and a %: a healthcheck that never passes
// under a restart policy fails up, a program its build makes comes up from
// a clean tree, a healthcheck test that leaves a child holding its output
// passes, and a calendar that has passed is said to be one.
func TestUp_HealthcheckBuildAndOddPaths(t *testing.T) {
	p := newProject(t, "e d%d", "e", "")
	p.file("tool", "#!/bin/sh\nexec sleep infinity\n")
	p.file("mk", "#!/bin/sh\nmkdir -p out && cp tool out/app\n")
	os.Chmod(p.path("tool"), 0o755)
	os.Chmod(p.path("mk"), 0o755)
	p.write(`name: NAME
services:
  ready:
    command: ./tool
    restart: {policy: on-failure, delay: 1s}
    healthcheck: {test: [sh, -c, 'exit 1'], interval: 1s, timeout: 1s, start_period: 2s}
  built:
    command: ./out/app
    build: {run: [./mk], creates: out/app}
  child:
    command: ./tool
    healthcheck: {test: [sh, -c, 'sleep 30 & exit 0'], interval: 1s, timeout: 5s, start_period: 10s}
  once:
    command: [sh, -c, 'echo once']
    schedule: "2020-01-01 00:00:00"
`)
	up := p.sc("up")
	check(t, "up of a project whose healthcheck never passes, under restart:, exits nonzero", up.fails())
	check(t, "  and names it", up.says(p.name+`-ready\.service keeps restarting \(restart #[0-9]*\): its healthcheck has not passed`))
	ps := p.sc("ps")
	check(t, "  a program in a directory with a space and a % runs", ps.says("^"+p.name+`-child\.service .* active *running *ready`))
	check(t, "  so does one its build made from a clean tree", ps.says("^"+p.name+`-built\.service .* active *running`))
	check(t, "  a calendar that has passed is noted", up.says(`schedule: "2020-01-01 00:00:00" has no next run`))
	check(t, "  and its timer is not said to be running a job", ps.says(p.name+`-once\.timer .*no next run`))
	restart := p.sc("restart", "ready")
	check(t, "  restart of the never-ready one exits nonzero and says why", firstErr(restart.fails(), restart.says("keeps restarting")))
	check(t, "  down of that project exits 0", p.sc("down").ok())
}

// Names that are not the project's: one of systemd's own units is
// refused; a verb that would act on the whole instance, or on what is not
// the project's, is refused; down where a twin of the same name runs stops
// nothing.
func TestNames_SystemdsOwnAndTwins(t *testing.T) {
	yaml := "name: NAME\nservices:\n  s: {command: sleep infinity}\n"
	t.Cleanup(func() { systemctl("unmask", prefix+"_t-s.service") }) // after both downs
	t1 := newProject(t, "t1", "t", yaml)
	t2 := newProject(t, "t2", "t", yaml)
	name := t1.name
	unit := t1.unit("s", ".service")

	check(t, "a project named like one of systemd's own units is refused", t1.sc("-p", "default", "up", "--dry-run").says(`default\.target is already a unit of its own`))
	check(t, "  top before up says nothing of it runs", t1.sc("top").says("nothing of project "+name+" runs"))
	check(t, "up of a project with a twin elsewhere", t1.sc("up").ok())
	check(t, "  a systemctl verb with no service named is refused, not run on the instance", t1.sc("reset-failed").says("acts on the whole user instance"))
	check(t, "  one that changes units, given what is not the project's, is refused", t1.sc("reset-failed", "*").says("acts outside this project"))
	check(t, "  given the project's own unit name, it passes", t1.sc("reset-failed", unit).ok())
	check(t, "  a flag's value as a word of its own is refused", t1.sc("reset-failed", "--job-mode", "replace", "s").says("write it as --job-mode=VALUE"))
	check(t, "  after a --, every word is a unit, a flag-like one too", t1.sc("clean", "--", "--what", "s").says("acts outside this project"))
	check(t, "  disable of the project's service is refused (up and down own the link)", t1.sc("disable", "s").says("up registers the project's units"))
	// a mode systemctl rejects: a binary without the refusal fails harmlessly
	check(t, "  preset-all, which takes no unit, is refused", t1.sc("preset-all", "--preset-mode", "bogus").says("preset-all resets the enablement"))
	check(t, "  cancel with no job named is refused", t1.sc("cancel").says("cancels every pending job"))
	check(t, "  cancel --help still reaches systemctl's help", t1.sc("cancel", "--help").ok())
	check(t, "  top in the twin's directory, its slice running, shows it", t2.sc("top", "-n", "1").lacks("nothing of project"))
	t1.write(strings.Replace(yaml, "sleep infinity", "sleep 86400", 1))
	check(t, "  ps under -p says to apply a change with the same -p", t1.sc("-p", name, "ps").says("systemd-compose -p "+name+" up applies them"))
	check(t, "  down in the twin's directory exits nonzero", t2.sc("down").fails())
	check(t, "  and leaves the first copy running", firstErr(active(unit), active(name+".target"), active(name+".slice")))
	check(t, "  down of the first exits 0", t1.sc("down").ok())
	systemctl("mask", unit)
	check(t, "  up of a masked project unit says to unmask it", t1.sc("up", "--dry-run").says("is masked: unmask it"))
	check(t, "  and unmask of the project's own masked unit passes", t1.sc("unmask", "s").ok())
	check(t, "  then top in the twin's directory says nothing runs", t2.sc("top", "-n", "1").says("nothing of project "+name+" runs"))
	check(t, "  and the project's own unit name, unregistered, is no outsider", t1.sc("reset-failed", unit).lacks("not of project"))
}

// TestRegistration_CopiesLoadWithoutTheProject checks registration: copy:
// the units are files of their own, so systemd keeps them loaded while
// the project's directory is gone (a filesystem not mounted), and -p NAME
// reaches them from elsewhere. up switches between copies and links
// either way, the target's boot link following; an orphan's copy is
// retired, and down leaves nothing.
func TestRegistration_CopiesLoadWithoutTheProject(t *testing.T) {
	yaml := func(reg, extra string) string {
		return "name: NAME\n" + reg + "services:\n  a: {command: [sleep, infinity]}\n" + extra
	}
	p := newProject(t, "cp", "cp", yaml("registration: copy\n", ""))
	unit := p.unit("a", ".service")
	isCopy := func(name string) error {
		st, err := os.Lstat(filepath.Join(unitDir, name))
		return that(err == nil && st.Mode().IsRegular(), "%s is no copy in the unit directory: %v", name, err)
	}
	isLink := func(name string) error {
		t, err := os.Readlink(filepath.Join(unitDir, name))
		return that(err == nil && strings.HasSuffix(t, "/.systemd-compose/"+name), "%s is no link into the render directory: %q %v", name, t, err)
	}
	wants := func() string {
		t, _ := os.Readlink(filepath.Join(unitDir, "default.target.wants", p.name+".target"))
		return t
	}

	check(t, "up of a project with registration: copy", p.sc("up").shows(regexp.QuoteMeta(unit)+` +new +copy, start`))
	check(t, "  copies its units", isCopy(unit))
	check(t, "  and the target's boot link names the copy", equal("wants", wants(), filepath.Join(unitDir, p.name+".target")))
	check(t, "  ps says copied", p.sc("ps").shows(regexp.QuoteMeta(unit)+` +loaded +active +running +copied`))
	check(t, "  and import refuses a project's copy", run("", nil, sc, "import", unit).says("is a project's already"))

	away := p.dir + ".away"
	if err := os.Rename(p.dir, away); err != nil {
		t.Fatal(err)
	}
	systemctl("daemon-reload")
	check(t, "with the directory gone and a reload, the unit stays loaded", equal("LoadState", property(unit, "LoadState"), "loaded"))
	check(t, "  and -p NAME ps elsewhere shows it", run(t.TempDir(), nil, sc, "-p", p.name, "ps").shows(regexp.QuoteMeta(unit)+` +loaded +active`))
	if err := os.Rename(away, p.dir); err != nil {
		t.Fatal(err)
	}

	p.write(yaml("", ""))
	check(t, "up without registration: links in place of the copies", p.sc("up").shows(regexp.QuoteMeta(unit)+` +unchanged +link, in place of its copy`))
	check(t, "  the unit is a link", isLink(unit))
	check(t, "  the target's boot link names its render file", that(strings.HasSuffix(wants(), "/.systemd-compose/"+p.name+".target"), "wants %s", wants()))
	check(t, "  and the service ran on", p.sc("ps").shows(regexp.QuoteMeta(unit)+` +loaded +active +running`))

	p.write(yaml("registration: copy\n", "  b: {command: [sleep, infinity]}\n"))
	check(t, "up with registration: copy again copies in place of the links", p.sc("up").shows(regexp.QuoteMeta(unit)+` +unchanged +copy, in place of its link`))
	check(t, "  the unit is a copy", isCopy(unit))
	p.write(yaml("registration: copy\n", ""))
	check(t, "up --force retires a dropped service's copy", p.sc("up", "--force").shows("retired orphan "+regexp.QuoteMeta(p.unit("b", ".service"))))
	check(t, "  and removes it", missing(filepath.Join(unitDir, p.unit("b", ".service"))))
	check(t, "down", p.sc("down").ok())
	check(t, "  leaves no copy or link", equal("entries", p.links(), 0))
	check(t, "  nor the boot link", missing(filepath.Join(unitDir, "default.target.wants", p.name+".target")))
}
