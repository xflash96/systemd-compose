package project

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
)

// CheckArgs checks a project verb's arguments before the yaml is read,
// answers compose's flags in their own terms, and drops the ones that ask
// for what happens anyway.
func CheckArgs(verb string, args []string) ([]string, error) {
	translate := map[string]map[string]string{
		"up": {
			"--remove-orphans":          "up already retires units the yaml no longer declares (--force for a running one)",
			"--no-deps":                 "up brings up the whole project; start SERVICE starts one",
			"--no-start":                "there is no register-only up",
			"--abort-on-container-exit": "services run under systemd, not attached to up",
		},
		"down": {
			"-v":               "there are no volumes; down unregisters the units, and the rendered files stay in .systemd-compose/",
			"--volumes":        "there are no volumes; down unregisters the units, and the rendered files stay in .systemd-compose/",
			"--rmi":            "there are no images",
			"--remove-orphans": "down always retires what an older yaml registered",
		},
		"start": {
			"--no-deps": "start SERVICE starts that service and what it requires: systemd starts a service's dependencies with it",
		},
		"stop": {
			"-t":        stopTimeout,
			"--timeout": stopTimeout,
		},
		"restart": {
			"-t":        stopTimeout,
			"--timeout": stopTimeout,
			"--no-deps": "systemd restarts what requires a service along with it; there is no restart of one alone",
		},
		"build": {
			"--no-cache": "build always runs every step (up runs them only when creates: is missing)",
			"--pull":     "there are no images to pull",
		},
	}
	known := map[string][]string{
		"up":      {"--dry-run", "--build", "--force", "--force-recreate", "--no-recreate"},
		"down":    {},
		"ps":      {},
		"config":  {},
		"start":   {},
		"stop":    {},
		"restart": {},
		"build":   {},
	}
	// The verbs that take no service names, and what to say instead; the
	// others' arguments that are not flags are service names.
	whole := map[string]string{
		"up":     "up takes no service names: it brings up the whole project (start SERVICE starts one, once up has registered it)",
		"down":   "down takes the whole project down (stop SERVICE stops one)",
		"ps":     "ps shows the whole project (logs SERVICE shows one)",
		"config": "config takes no arguments: it prints the yaml and every unit as rendered",
	}
	ok, checked := known[verb]
	if !checked {
		return args, nil
	}
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case verb == "up" && (a == "--wait-timeout" || strings.HasPrefix(a, "--wait-timeout=")):
			v, ok := strings.CutPrefix(a, "--wait-timeout=")
			if !ok {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("up --wait-timeout needs a time span (60, 90s, 2min)")
				}
				i++
				v = args[i]
			}
			if _, err := strconv.Atoi(v); err == nil {
				v += "s" // compose's seconds
			}
			if n, err := config.Seconds(v); err != nil {
				return nil, fmt.Errorf("up --wait-timeout: %v", err)
			} else if n == 0 {
				// 0 is the unset value, which would give the default watch silently
				return nil, fmt.Errorf("up --wait-timeout: 0 would watch nothing, which this flag does not do; leave it out for the default %s, or give a span (5s)", defaultWait)
			}
			out = append(out, "--wait-timeout="+v)
		case verb == "up" && (a == "-d" || a == "--detach" || a == "--wait"),
			verb == "ps" && (a == "-a" || a == "--all"):
			// what happens anyway: up returns at once, ps lists every unit
		case slices.Contains(ok, a):
			out = append(out, a)
		case translate[verb][flagName(a)] != "":
			return nil, fmt.Errorf("%s %s: %s", verb, flagName(a), translate[verb][flagName(a)])
		case strings.HasPrefix(a, "-") && verb != "config":
			return nil, fmt.Errorf("%s: unknown flag %q (%s)", verb, a, flagList(verb, ok))
		case whole[verb] == "":
			out = append(out, a)
		default:
			return nil, errors.New(whole[verb])
		}
	}
	return out, nil
}

const stopTimeout = "a service's stop timeout is its unit's: unit: {Service: {TimeoutStopSec: 30s}}"

// flagName is a flag without its =value.
func flagName(a string) string {
	name, _, _ := strings.Cut(a, "=")
	return name
}

func flagList(verb string, flags []string) string {
	if verb == "up" {
		flags = append(slices.Clone(flags), "--wait-timeout T") // matched apart, as it takes a value
	}
	if len(flags) == 0 {
		return verb + " takes no flags"
	}
	return verb + " takes " + strings.Join(flags, ", ")
}
