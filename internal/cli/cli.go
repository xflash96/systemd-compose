// Package cli is the command line: the global flags, the help, and the
// choice of mode. Anything unrecognised passes through to systemctl
// untouched.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/probe"
	"github.com/xflash96/systemd-compose/internal/project"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

const help = `systemd-compose — docker-compose verbs over systemd.

  systemd-compose [--system|-s|--user] [-f FILE] [-p NAME] [--profile NAME]... VERB [ARGS...]
  systemd-compose help VERB       (or VERB --help) for one verb

INSIDE A PROJECT (a systemd-compose.yaml here or in any parent directory, as
compose finds its file, or the one -f names) every verb is scoped to it, on
the user instance; --system is refused. The project NAME is the namespace:
every unit, the target and the slice carry it. It comes from, in order: -p
NAME, the SYSTEMD_COMPOSE_PROJECT_NAME variable in the environment, the same
variable in a .env beside the yaml, name: in the yaml, the directory's name.
A service with profiles: runs only when one of them is active: --profile
NAME (repeatable), else SYSTEMD_COMPOSE_PROFILES (comma list) from the
environment or the .env; * is all. down and stop take every profile.

  up [--dry-run] [--build] [--force] [--force-recreate|--no-recreate] [--wait-timeout T]
                           render, verify, build what creates: says is
                           missing, register and start the project;
                           restart what changed (on_change: start-only warns);
                           exits 1 naming any unit that failed to start;
                           --force-recreate restarts every running service,
                           --no-recreate none; --force retires active orphans;
                           --build runs every build:, creates: or not;
                           --dry-run prints the plan (and any refusal) and stops;
                           --wait-timeout T watches a restarting service for T
  down                     stop and unregister every unit, and retire what an
                           older yaml left registered; the current files stay
  ps                       the project's units and their state
  logs [-f] [--tail N] [--since T] [-t] [--no-log-prefix] [--no-color] [SERVICE...]
                           the project's journal; no SERVICE = all of it
  start|stop|restart [SERVICE...]   run state only; no SERVICE = all; each
                           says what started or stopped, dependents too
  kill [-s SIGNAL] SERVICE...  signal the service's processes; SIGTERM unless
                           -s says otherwise (compose's kill sends SIGKILL:
                           -s KILL)
  list-timers              (systemctl's) when each of the project's
                           scheduled jobs runs next
  build [SERVICE...]       run build: steps in the service's own environment
  run [-e K=V] [-w DIR] [-T] SERVICE [CMD...]
                           a one-off command in the service's environment
                           (no CMD: the service's own); exec needs a CMD
  config                   the yaml, then every unit as it would be rendered
  top [SERVICE]            systemd-cgtop on the project slice, or on one service
  VERB [SERVICE...]        systemctl --user VERB, each service name mapped to
                           its unit; a scheduled job's is its timer (status,
                           cat, reset-failed: its run's unit too; is-failed:
                           only that); one that would take the whole
                           instance with no name (status, reset-failed...)
                           needs one, and one that changes units (reset-failed,
                           freeze, clean...) takes this project's services only;
                           enable, disable, reenable, mask and preset are
                           refused (up and down own the registration)

ANYWHERE:

  ls                       every project registered on the user instance
  version                  this program's version, then systemd's

OUTSIDE A PROJECT: the same words on the user instance (the system instance
when run as root); --system or -s for the system instance explicitly, --user
for your user instance as root.

  ps [-a] [PATTERN]        list-units --type=service,timer   (-a: stopped too)
  logs [-f] [UNIT...]      journalctl, one -u per UNIT; no UNIT = the whole journal
  start|stop|restart UNIT...   the same words in systemctl
  up UNIT...               enable --now: register for boot and start
  down UNIT...             disable --now: stop and unregister; the file stays
  config UNIT...           systemctl cat
  top                      systemd-cgtop on the instance's cgroup subtree
  VERB ARGS...             systemctl VERB ARGS
`

func run(args []string) error {
	var f project.Flags
	var user, system bool
	for len(args) > 0 {
		// --project-name=NAME, --profile=NAME and --file=PATH are the
		// flag and its value
		if k, v, ok := strings.Cut(args[0], "="); ok && slices.Contains([]string{"--project-name", "--profile", "--file"}, k) {
			args = append([]string{k, v}, args[1:]...)
		}
		switch args[0] {
		case "--user":
			user = true
		case "--system", "-s":
			system = true
		case "-p", "--project-name":
			// An empty one (an unset $NAME) is refused: read as no name, the
			// next source would name the project, and down would take a
			// project nobody named.
			if len(args) < 2 || args[1] == "" {
				return fmt.Errorf("%s needs a name", args[0])
			}
			f.Name = args[1]
			args = args[1:]
		case "--profile":
			if len(args) < 2 || args[1] == "" {
				return fmt.Errorf("--profile needs a name")
			}
			f.Profiles = append(f.Profiles, args[1])
			args = args[1:]
		case "-f", "--file":
			if len(args) < 2 {
				return fmt.Errorf("%s needs a path to a systemd-compose.yaml", args[0])
			}
			if err := setFile(&f, args[1]); err != nil {
				return err
			}
			args = args[1:]
		default:
			goto parsed
		}
		args = args[1:]
	}
parsed:
	verb := "help"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	if verb == "help" && len(args) > 0 {
		return helpFor(args[0])
	}
	if wantsHelp(verb, args) {
		return helpFor(verb)
	}
	if err := unknownVerb(verb); err != nil {
		return err
	}
	switch verb {
	case "help", "-h", "--help":
		fmt.Print(help)
		return nil
	case "version", "--version":
		printVersion()
		return nil
	case probe.Verb:
		return probe.Run(args)
	case "ls":
		if system {
			return fmt.Errorf("ls lists projects, and projects live on the user instance")
		}
		if f.File != "" || f.Name != "" || len(f.Profiles) > 0 {
			return fmt.Errorf("ls lists every project registered here; -f, -p and --profile name one project and mean nothing to it")
		}
		m := &systemd.Manager{User: true}
		if err := m.Reachable(); err != nil {
			return err
		}
		return project.List(m)
	}
	cfg := f.File
	if cfg == "" {
		cfg = config.FindConfig(".")
	}
	if cfg != "" {
		if system {
			return fmt.Errorf("--system is refused inside a project (%s): projects live on the user instance, which cannot link files under /home into /etc", cfg)
		}
		// Before the yaml is read: a wrong flag is answered even while the
		// file does not load yet.
		args, err := project.CheckArgs(verb, args)
		if err != nil {
			return err
		}
		return project.Run(cfg, verb, args, f)
	}
	if f.Name != "" || len(f.Profiles) > 0 {
		return fmt.Errorf("-p and --profile belong to a project, and there is %s", noYAML)
	}
	// User instance by default, system as root; either flag is explicit.
	m := &systemd.Manager{User: !system && (user || os.Getuid() != 0)}
	if m.User {
		yamlGone(m)
	}
	return instanceMode(m, verb, args)
}

// noYAML is why a verb has no project.
const noYAML = "no " + config.ConfigFileName + " here or above"

// yamlGone points the way out when this directory, or one above, is where
// a registered project's units were rendered, though its yaml is gone (a
// branch switch, a deleted checkout). The verb that follows acts on the
// whole user instance and would say nothing of the project left registered.
func yamlGone(m *systemd.Manager) {
	unitDir, err := m.UnitDir()
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(unitDir)
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		render := filepath.Join(dir, config.RenderDirName)
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join(unitDir, e.Name())); err == nil && filepath.Dir(target) == render {
				name, _, _ := strings.Cut(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), "-")
				fmt.Printf("note: project %s is registered from %s, whose %s is gone: put it back and run down there, or see README, \"Moving or deleting a project\"; this verb acts on the user instance\n", name, dir, config.ConfigFileName)
				return
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// setFile takes -f: one file, which must be there. compose merges several
// -f files; this tool reads one, so a second is refused rather than half
// honoured.
func setFile(f *project.Flags, path string) error {
	if f.File != "" {
		return fmt.Errorf("-f given twice: compose merges several files, this tool reads one")
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("-f %s: %v", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("-f %s: a directory; name the yaml (or cd there)", path)
	}
	f.File = path
	return nil
}

// Main runs the command line and returns the exit code. stamp is the
// version a release build was stamped with, "" for any other build.
func Main(args []string, stamp string) int {
	version = stamp
	code, msg := outcome(run(args))
	if msg != "" {
		fmt.Fprintln(os.Stderr, "systemd-compose:", msg)
	}
	return code
}

// outcome is the exit code and the message for what run returned. A bare
// ExitError is a code passed through (run's, exec's, the probe's) whose
// words were already said; one wrapped in a message is a step of ours
// that failed, and says which.
func outcome(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if ee, ok := err.(systemd.ExitError); ok {
		return ee.Code, ""
	}
	return 1, err.Error()
}
