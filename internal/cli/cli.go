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

  systemd-compose [--system|-s|--user] [-f FILE] [-p NAME] [--profile NAME]...
                  VERB [ARGS...]

INSIDE A PROJECT (a systemd-compose.yaml here or above, or -f FILE):
  up                       register and start the project; restart what changed
  down                     stop and unregister the project
  ps                       its units and their state
  logs [SERVICE...]        its journal
  start|stop|restart [SERVICE...]   change the run state only
  kill SERVICE...          signal a service's processes
  build [SERVICE...]       run the services' build: steps
  run|exec SERVICE [CMD...]   a one-off command in a service's environment
  config                   the yaml, then every unit as rendered
  top [SERVICE]            systemd-cgtop on the project, or on one service
  VERB [SERVICE...]        another systemctl verb, on the services' units

ANYWHERE:
  ls                       every project registered on the user instance
  import UNIT [SERVICE]    a project's yaml for a unit you wrote by hand
  version                  this program's version, then systemd's

OUTSIDE A PROJECT: -p NAME is the project registered as NAME. Without it, the
verbs act on the user instance (-s: the system instance):
  ps [-a] [PATTERN]        its services and timers
  logs [-f] [UNIT...]      journalctl
  start|stop|restart UNIT...   the same words in systemctl
  up UNIT...               enable --now
  down UNIT...             disable --now
  config UNIT...           systemctl cat
  top                      systemd-cgtop
  VERB ARGS...             systemctl VERB ARGS

More on a verb: systemd-compose help VERB; the whole manual: help man.
Every key of systemd-compose.yaml: systemd-compose help yaml.
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
	case "import":
		if system {
			return fmt.Errorf("import reads a unit of the user instance, where projects live")
		}
		if f.File != "" || f.Name != "" || len(f.Profiles) > 0 {
			return fmt.Errorf("import reads one unit of the user instance; -f, -p and --profile name a project and mean nothing to it")
		}
		if len(args) == 0 || len(args) > 2 || strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("import takes a unit and, if you like, the service's name: import UNIT [SERVICE]")
		}
		m := &systemd.Manager{User: true}
		if err := m.Reachable(); err != nil {
			return err
		}
		service := ""
		if len(args) == 2 {
			service = args[1]
		}
		return project.Import(m, args[0], service)
	}
	cfg := f.File
	if cfg == "" {
		cfg = config.FindConfig(".")
	}
	if cfg == "" && f.Name != "" {
		// compose's -p: the project of that name, wherever it is
		found, err := project.RegisteredConfig(f.Name)
		if err != nil {
			return err
		}
		if found == "" {
			return fmt.Errorf("no project %s is registered on the user instance (ls lists them), and there is %s", f.Name, noYAML)
		}
		cfg = found
	}
	if cfg != "" {
		if system {
			return fmt.Errorf("--system is refused inside a project (%s): projects live on the user instance: their services are yours, not the system's", cfg)
		}
		// Before the yaml is read: a wrong flag is answered even while the
		// file does not load yet.
		args, err := project.CheckArgs(verb, args)
		if err != nil {
			return err
		}
		return project.Run(cfg, verb, args, f)
	}
	if len(f.Profiles) > 0 {
		return fmt.Errorf("--profile belongs to a project, and there is %s", noYAML)
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
	from := project.RenderDirs(unitDir)
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if name := from[filepath.Join(dir, config.RenderDirName)]; name != "" {
			fmt.Printf("note: project %s is registered from %s, whose %s is gone (or its filesystem is not mounted): put it back and run down there, or systemd-compose -p %s down; README, \"Moving or deleting a project\"; this verb acts on the user instance\n", name, dir, config.ConfigFileName, name)
			return
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
// version a release build was stamped with, "" for any other build; d the
// documents help prints.
func Main(args []string, stamp string, d Docs) int {
	version, docs = stamp, d
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
