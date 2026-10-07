//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBasics(t *testing.T) {
	check(t, "--version answers", run("", nil, sc, "--version").ok())
	// the two lines of printVersion (internal/cli/version.go)
	v := run("", nil, sc, "version")
	check(t, "version prints this program's line, then systemd's", that(match(`\Asystemd-compose \S+.*\nsystemd [0-9]`, v.out), "version printed:\n%s", v.out))
	check(t, "a probe whose command has -h fails when it fails", run("", nil, sc, "probe", "--interval", "1s", "--timeout", "1s", "--start-period", "1s", "--", "false", "-h", "127.0.0.1").fails())
}

// mainYAML is the project of TestProject: web's X, an optional extra
// service, and the project's memory cap.
func mainYAML(x, extra, memory string) string {
	return fmt.Sprintf(`name: NAME
resources: {memory: %s}
x-base: &base
  restart: unless-stopped
services:
  web:
    <<: *base
    command: [sh, -c, 'echo "web X=$$X G=$$G RT=$$RT fds=$$LISTEN_FDS"; exec sleep infinity']
    environment: {X: "%s", G: "${GREETING}", RT: "%%t/NAME"}
    listen: "%%t/NAME.sock"
    healthcheck: {test: [true], interval: 1s}
  job:
    command: [sh, -c, "echo job ran"]
    schedule: "*-*-* 04:00:00"
    build: {run: ["sh -c 'cat /proc/self/cgroup > built; cat /sys/fs/cgroup$$(dirname $$(cut -d: -f3 /proc/self/cgroup))/memory.max >> built'"], creates: built}
  debug:
    command: sleep infinity
    profiles: [debug]
%s`, memory, x, extra)
}

// TestProject takes one project through its life: a dry run, up, an
// unchanged up, a change, one-offs, the documented invocations, profiles,
// orphans and down. Each step builds on the one before.
func TestProject(t *testing.T) {
	p := newProject(t, "p", "", mainYAML("1", "", "256M"))
	p.file(".env", "GREETING=hello\n")
	name := p.name
	units := func() int {
		if l := loaded(name); l != "" {
			return len(strings.Split(l, "\n"))
		}
		return 0
	}
	// 1. a dry run on a fresh project writes nothing
	check(t, "up --dry-run on a fresh project exits 0", p.sc("up", "--dry-run").ok())
	check(t, "  and writes no render directory", missing(p.path(".systemd-compose")))
	check(t, "  and registers nothing", equal("links", p.links(), 0))
	check(t, "run before any up works from the yaml", p.sc("run", "job").shows("job ran"))
	check(t, "  and leaves nothing loaded (no on-demand slice)", equal("units loaded", units(), 0))

	// 2. up: everything declared and enabled comes up
	check(t, "up exits 0", p.sc("up").ok())
	for _, u := range []string{"web.service", "web.socket", "job.timer"} {
		check(t, u+" is active", active(name+"-"+u))
	}
	check(t, "the slice is active", active(name+".slice"))
	check(t, "  and carries the project's memory cap", equal("MemoryMax", property(name+".slice", "MemoryMax"), "268435456"))
	check(t, "the target is active", active(name+".target"))
	check(t, "the first up's build ran in the slice", fileMatches(p.path("built"), "/"+name+`\.slice/`))
	check(t, "a disabled profile is not registered", missing(filepath.Join(unitDir, name+"-debug.service")))
	check(t, "ps shows HEALTH ready", p.sc("ps").shows(" ready "))
	// the manager's unit directory, not the shell's XDG_CONFIG_HOME, says
	// what is registered
	check(t, "ps under another XDG_CONFIG_HOME still sees the project registered",
		run(p.dir, []string{"XDG_CONFIG_HOME=" + filepath.Join(t.TempDir(), "elsewhere")}, sc, "-f", p.path("systemd-compose.yaml"), "ps").shows("^"+name+`-web\.service .*linked`))
	check(t, "ls lists the project", run("", nil, sc, "ls").shows("^"+name+" "))
	time.Sleep(time.Second)
	check(t, "the service saw .env, a specifier and its socket", p.sc("logs", "--no-log-prefix", "web").shows(regexp.QuoteMeta("web X=1 G=hello RT="+runtimeDir()+"/"+name+" fds=1")))

	// 3. a second up changes nothing
	p0 := mainPID(name + "-web.service")
	check(t, "a second up reports every row unchanged", noChange(p.sc("up"), name))
	check(t, "  and names the build up to date", p.sc("up", "--dry-run").shows(`^  build job: up to date, `))
	check(t, "  and restarts nothing", equal("web's pid", mainPID(name+"-web.service"), p0))

	// 4. a change restarts the changed service only
	p.write(mainYAML("2", "", "256M"))
	check(t, "ps notes the edit up has not applied", p.sc("ps").shows("has changes up has not applied"))
	check(t, "up after an edit exits 0", p.sc("up").ok())
	check(t, "  web was restarted", that(mainPID(name+"-web.service") != p0, "web's pid is still %s", p0))
	check(t, "  and runs the new definition", p.sc("run", "web", "sh", "-c", `test "$X" = 2`).ok())

	// 4b. a later up's build runs under that up's limits (the rule:
	// runBuilds in internal/project/up.go)
	check(t, "the first build saw the 256M cap", equal("memory.max", line(p.path("built"), 2), "268435456"))
	p.write(mainYAML("2", "", "512M"))
	check(t, "up --build under a raised cap exits 0", p.sc("up", "--build").ok())
	check(t, "  and the build saw the new cap", equal("memory.max", line(p.path("built"), 2), "536870912"))

	// 5. run and exec
	check(t, "run carries the exit code", p.sc("run", "web", "sh", "-c", "exit 3").exits(3))
	check(t, "run -e beats the service's value", p.sc("run", "-e", "X=9", "web", "sh", "-c", `test "$X" = 9`).ok())
	check(t, "run lands in the project's slice", p.sc("run", "-T", "web", "cat", "/proc/self/cgroup").shows("/"+name+`\.slice/`))
	argv := p.sc("run", "-T", "web", "sh", "-c", "ps -eo args")
	check(t, "run puts no environment value on a command line others can read", firstErr(argv.ok(), argv.lacks("Environment=X=")))
	check(t, "  and run still gets the service's environment", p.sc("run", "-T", "web", "sh", "-c", `test "$X" = 2`).ok())
	mode := ""
	if st, err := os.Stat(p.path(".systemd-compose/" + name + "-web.service")); err == nil {
		mode = fmt.Sprintf("%o", st.Mode().Perm())
	}
	check(t, "rendered units, which may hold secrets, are readable by their owner alone", equal("mode", mode, "600"))
	check(t, "run job runs the job's own command", p.sc("run", "job").shows("job ran"))
	check(t, "exec needs a command", p.sc("exec", "web").fails())
	check(t, "kill -s cont (any case) says web is still running", p.sc("kill", "-s", "cont", "web").says("sent SIGCONT; still running"))

	// 5b. every invocation README and help show is taken as written: none
	// is answered by a parse refusal
	parses := func(r result) error {
		return r.lacks("unknown flag|unknown verb|Unknown command verb|unrecognized option|takes no flags|needs a name|is not a systemd time span|needs a number|needs a time")
	}
	cfg := p.path("systemd-compose.yaml")
	check(t, "documented: ps, ps -a", parses(p.sc("ps", "-a")))
	check(t, "documented: config", parses(p.sc("config")))
	check(t, "documented: logs --tail 5 web", parses(p.sc("logs", "--tail", "5", "web")))
	check(t, "documented: logs --tail=all", parses(p.sc("logs", "--tail=all")))
	check(t, "documented: logs --since 10m, --since -1h, --since=-1h", parses(p.sc("logs", "--since", "10m", "--since", "-1h", "--since=-1h")))
	check(t, "documented: logs -t, --no-log-prefix, --no-color, -n 3", parses(p.sc("logs", "--no-color", "-n", "3")))
	check(t, "documented: logs --no-log-prefix web", parses(p.sc("logs", "--no-log-prefix", "web")))
	check(t, "documented: up --dry-run, -d", parses(p.sc("up", "-d", "--dry-run")))
	check(t, "documented: up --wait-timeout 30, =30s", parses(p.sc("up", "--wait-timeout", "30", "--wait-timeout=30s", "--dry-run")))
	check(t, "documented: up --build, --force-recreate, --force", parses(p.sc("up", "--build", "--force-recreate", "--force", "--dry-run")))
	check(t, "documented: up --no-recreate", parses(p.sc("up", "--no-recreate", "--dry-run")))
	check(t, "documented: list-timers", parses(p.sc("list-timers")))
	check(t, "documented: status, cat, is-active SERVICE", parses(p.sc("status", "web")))
	check(t, "documented: top", parses(p.sc("top", "-n", "1")))
	check(t, "top SERVICE shows that service's processes", p.sc("top", "web", "-n", "1", "-b").shows(name+`-web\.service`))
	check(t, "top with a mistyped service is refused", p.sc("top", "wbe").fails())
	check(t, "status --help reaches systemctl's help", p.sc("status", "--help").shows("status"))
	check(t, "list-timers with a mistyped service is refused", p.sc("list-timers", "jbo").fails())
	check(t, "documented: help up", parses(p.sc("help", "up")))
	check(t, "documented: kill -s CONT", parses(p.sc("kill", "-s", "CONT", "web")))
	check(t, "documented: run -T, exec -T", parses(p.sc("exec", "-T", "web", "true")))
	check(t, "documented: -p NAME, --project-name=NAME", parses(p.sc("--project-name="+name, "-p", name, "ps")))
	check(t, "documented: --profile '*', --profile=debug", parses(p.sc("--profile", "*", "--profile=debug", "ps")))
	check(t, "documented: -f FILE, --file=FILE", parses(p.sc("-f", cfg, "ps")))
	check(t, "documented: --file=FILE", parses(p.sc("--file="+cfg, "ps")))
	check(t, "documented: ls, and ps -a, logs, -s ps outside a project", parses(run("/", nil, "sh", "-c", fmt.Sprintf("'%[1]s' ls && '%[1]s' ps -a && '%[1]s' logs -n 1 && '%[1]s' -s ps", sc))))

	// 6. profiles
	check(t, "--profile with a typo is refused, even by ps", p.sc("--profile", "debgu", "ps").fails())
	check(t, "--profile debug up registers debug", p.sc("--profile", "debug", "up").ok())
	check(t, "  debug is active", active(name+"-debug.service"))
	check(t, "a bare stop stops every profile", p.sc("stop").ok())
	check(t, "  debug included", inactive(name+"-debug.service"))
	check(t, "start brings the enabled set back", p.sc("start").ok())
	check(t, "  web is active again", active(name+"-web.service"))

	// 7. a dropped service is retired: by up (an active one only with
	// --force), and by down; debug, of an inactive profile, is still
	// registered from 6. Two extras, so a refusal that cut the plan short
	// would lose the second; and a job whose timer waits, which is not
	// running.
	extra := "  extra:\n    command: sleep infinity\n  extra2:\n    command: sleep infinity\n  late:\n    command: [\"true\"]\n    schedule: \"*-*-* 04:00:00\"\n"
	p.write(mainYAML("2", extra, "256M"))
	check(t, "up with two extra services and a job", p.sc("up").ok())
	p.write(mainYAML("2", "", "256M"))
	os.Remove(p.path("built")) // so the refused up also plans a build
	ps := p.sc("ps")
	check(t, "ps says up refuses the running extra", ps.shows("^"+name+`-extra\.service .*orphan: not in the yaml, and running`))
	check(t, "  and that up retires the job's waiting timer", ps.shows("^"+name+`-late\.timer .*orphan: not in the yaml \(the next up or down retires it\)`))
	refused := "orphan *ACTIVE, not in the yaml: refused without --force"
	up := p.sc("up")
	check(t, "up refuses to retire the running extras", up.fails())
	// row grammar
	check(t, "  and its plan shows the first orphan", up.says("^  "+name+`-extra\.service *`+refused))
	check(t, "  and does not refuse the waiting timer", up.lacks(name+`-late\.timer *`+refused))
	check(t, "  and the second", up.says("^  "+name+`-extra2\.service *`+refused))
	check(t, "  and the build it would run", up.says(`^  build job: will run, 1 step`))
	check(t, "  up --dry-run refuses too, with the same plan", p.sc("up", "--dry-run").says("^  "+name+`-extra2\.service *`+refused))
	check(t, "  built nothing", missing(p.path("built")))
	check(t, "  and leaves the extras registered", exists(filepath.Join(unitDir, name+"-extra.service")))
	check(t, "up --force retires them", p.sc("up", "--force").ok())
	check(t, "  extra's link is gone", missing(filepath.Join(unitDir, name+"-extra.service")))
	check(t, "  extra2's link is gone", missing(filepath.Join(unitDir, name+"-extra2.service")))
	p.write(mainYAML("2", extra, "256M"))
	check(t, "up with the extras again", p.sc("up").ok())
	p.write(mainYAML("2", "", "256M"))
	check(t, "a bare down after dropping them exits 0", p.sc("down").ok())
	check(t, "  every link is gone, debug's too", equal("links", p.links(), 0))
	check(t, "run after a down works from the yaml", p.sc("run", "job").shows("job ran"))
}

// firstErr is the first error that is not nil.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
