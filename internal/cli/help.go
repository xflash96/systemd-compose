package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/xflash96/systemd-compose/internal/project"
)

// wantsHelp is compose's `VERB --help`. For run and exec only before the
// service name: after it, --help belongs to the command.
func wantsHelp(verb string, args []string) bool {
	// Only this program's verbs: probe's own arguments are the healthcheck's
	// command (pg_isready -h HOST), and a passed-through verb is systemctl's
	// to explain.
	if verb == "help" || !slices.Contains(ourVerbs, verb) {
		return false
	}
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" && verb != "logs" {
			return true
		}
		if (verb == "run" || verb == "exec") && !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return false
}

// helpFor answers `help WORD`: the manual's entry for one of this
// program's verbs, the key reference for yaml, the whole manual for man, a
// line for a verb of systemctl's that passes through, and an error for a
// word that is none of them, so help tells which verbs exist.
func helpFor(verb string) error {
	switch verb {
	case "yaml":
		fmt.Print(docs.Keys)
		return nil
	case "man":
		fmt.Print(renderMan(docs.Man))
		return nil
	case "help":
		fmt.Print(help)
		return nil
	}
	if text := verbHelp(verb); text != "" {
		fmt.Print(text)
		return nil
	}
	if verb == "cancel" {
		fmt.Println("cancel is systemctl's and takes a JOB (systemctl --user list-jobs lists them), not a unit: inside a project a bare cancel is refused, as it cancels every pending job of the user instance; outside one, systemctl cancel ARGS.")
		return nil
	}
	if verb == "preset-all" {
		fmt.Println("preset-all is systemctl's and takes no unit: inside a project it is refused, as it resets every unit file of the user instance; outside one, systemctl preset-all ARGS.")
		return nil
	}
	if slices.Contains(project.Registers, verb) {
		fmt.Printf("%s is systemctl's: inside a project it is refused, since up registers the project's units and down unregisters them; outside one, systemctl %s ARGS.\n", verb, verb)
		return nil
	}
	if slices.Contains(systemctlVerbs, verb) {
		fmt.Printf("%s is systemctl's, passed through: in a project, systemd-compose %s SERVICE... is systemctl --user %s on the services' units; outside one, systemctl %s ARGS. systemctl %s --help lists its flags.\n", verb, verb, verb, verb, verb)
		return nil
	}
	if err := unknownVerb(verb); err != nil {
		return fmt.Errorf("help: %w", err)
	}
	return fmt.Errorf("help: no verb %q (help lists the verbs; help yaml shows every key of the file, help man the whole manual)", verb)
}

// verbHelp is the manual's entries for one of this program's verbs, in
// each section that has one, or "" for a word it has none for.
func verbHelp(verb string) string {
	text := manEntries(docs.Man, verb)
	if text == "" {
		return ""
	}
	return text + "(systemd-compose help: every verb; help man: the whole manual; help yaml: every key)\n"
}

// ourVerbs are the verbs this program answers itself.
var ourVerbs = []string{"up", "down", "ps", "logs", "start", "stop", "restart", "kill", "build", "run", "exec", "config", "top", "ls", "import", "version", "help"}

// systemctlVerbs pass through to systemctl unchanged.
var systemctlVerbs = strings.Fields(`list-units list-automounts list-paths list-sockets list-timers
	is-active is-failed status show cat help list-dependencies start stop reload restart
	try-restart reload-or-restart try-reload-or-restart isolate kill clean freeze thaw
	set-property bind mount-image service-log-level service-log-target reset-failed whoami
	list-unit-files enable disable reenable preset preset-all is-enabled mask unmask link
	revert add-wants add-requires edit get-default set-default list-machines list-jobs
	cancel show-environment set-environment unset-environment import-environment
	daemon-reload daemon-reexec log-level log-target service-watchdogs is-system-running
	default rescue emergency halt poweroff reboot kexec soft-reboot exit switch-root
	suspend hibernate hybrid-sleep suspend-then-hibernate sleep condreload condrestart condstop force-reload`)

// composeVerbs are compose's verbs with no counterpart here, and what to do.
var composeVerbs = map[string]string{
	"pull":    "there are no images: a service is a program on this host (build runs its build: steps)",
	"push":    "there are no images",
	"images":  "there are no images: a service is a program on this host",
	"pause":   "no pause: stop SERVICE, then start SERVICE",
	"unpause": "no pause: start SERVICE",
	"rm":      "no containers to remove: down unregisters the project (its rendered files stay in .systemd-compose/)",
	"create":  "no create: up registers and starts",
	"stats":   "top shows the project's cgroups",
	"events":  "logs -f follows the journal",
	"cp":      "no containers: a service uses this host's files",
	"port":    "no port mappings: a host process binds what it binds",
	"attach":  "logs -f SERVICE follows its output",
	"wait":    "up already waits for every service to start",
	"scale":   "one process per service: declare a second service",
	"watch":   "no watch: up after an edit applies it",
}

// unknownVerb answers a verb that is neither ours nor systemctl's: compose's
// own verbs with what to do instead, a typo of ours with the verb it is
// near. Anything else still passes through, for systemctl to judge.
func unknownVerb(verb string) error {
	if verb == "-h" || verb == "--help" || verb == "--version" {
		return nil // verbs of their own
	}
	if strings.HasPrefix(verb, "-") {
		// Handed on, systemctl would judge the verb ("Unknown command verb
		// 'logs'"), and in a project the verb would run with no report.
		return fmt.Errorf("unknown flag %q before the verb (before it: -f FILE, -p NAME, --profile NAME, --user, --system; a verb's own flags go after it)", verb)
	}
	if slices.Contains(ourVerbs, verb) || slices.Contains(systemctlVerbs, verb) {
		return nil
	}
	if why, ok := composeVerbs[verb]; ok {
		return fmt.Errorf("%s: %s", verb, why)
	}
	for _, v := range ourVerbs {
		if editDistance(verb, v) <= 2 && len(verb) > 2 {
			return fmt.Errorf("unknown verb %q; did you mean %q? (help lists the verbs; systemctl's own pass through)", verb, v)
		}
	}
	return nil
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
