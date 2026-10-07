package systemd

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// journalValueFlags are journalctl's flags that take a separate value; a
// bare word after one of these is its value, not a service.
var journalValueFlags = map[string]bool{"-n": true, "-o": true, "-u": true, "-p": true, "-g": true, "-t": true, "-S": true, "-U": true, "-c": true, "-F": true, "-D": true, "-M": true,
	"--lines": true, "--output": true, "--unit": true, "--priority": true, "--grep": true, "--identifier": true, "--since": true, "--until": true, "--cursor": true, "--after-cursor": true, "--cursor-file": true, "--field": true, "--directory": true, "--file": true, "--root": true, "--image": true, "--machine": true, "--namespace": true, "--facility": true, "--output-fields": true}

// ComposeLogFlags rewrites compose's `logs` flags into journalctl's:
// --tail N (all: no limit), --no-log-prefix, --timestamps (journal lines
// always carry one) and, inside a project, -t for it too (outside one, -t
// is journalctl's --identifier); a bare compose duration for --since or
// --until (42m) becomes the relative form journalctl takes (-42m).
func ComposeLogFlags(args []string, project bool) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v, true
			}
			// A flag is never another flag's value. Leaving it where it is
			// costs nothing: JournalArgs pairs what follows a value-taking
			// flag with it anyway, and journalctl's own --since -1h needs no
			// rewriting.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case a == "--tail" || strings.HasPrefix(a, "--tail="):
			// journalctl's -n takes an optional count: a word that is no
			// count would become a journal match, and a missing one would
			// reach journalctl as a --tail it does not know.
			v, ok := value()
			if n, err := strconv.Atoi(v); v != "all" && (!ok || err != nil || n < 0) {
				return nil, fmt.Errorf("logs --tail needs a number of lines (50) or all")
			}
			if v != "all" {
				out = append(out, "-n", v)
			}
		case a == "--no-color":
			os.Setenv("SYSTEMD_COLORS", "0") // journalctl's switch for it; exec passes it on
		case a == "--timestamps" || a == "-t" && project:
		case a == "--no-log-prefix":
			out = append(out, "-o", "cat")
		case a == "--since" || a == "--until" || strings.HasPrefix(a, "--since=") || strings.HasPrefix(a, "--until="):
			flag, _, _ := strings.Cut(a, "=")
			v, ok := value()
			if !ok && i+1 < len(args) && len(args[i+1]) > 1 && args[i+1][0] == '-' && args[i+1][1] >= '0' && args[i+1][1] <= '9' {
				i++ // journalctl's own relative time (-1h, "-5 min"), no flag
				v, ok = args[i], true
			}
			if !ok || v == "" {
				return nil, fmt.Errorf("logs %s needs a time (2026-10-02 17:00, 1h, yesterday)", flag)
			}
			if reGoDuration.MatchString(v) {
				v = "-" + v
			}
			out = append(out, flag, v)
		default:
			out = append(out, a)
			// A journalctl flag's own value is that value, never a compose
			// flag: `logs -g --tail 5` greps for "--tail", as journalctl
			// reads it, instead of turning the pattern into -n 5.
			if journalValueFlags[a] && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		}
	}
	return out, nil
}

// reGoDuration is a bare time span for --since and --until, compose's (42m)
// and systemd's (2min, 1d, 1h 30min); journalctl takes it with a minus.
var reGoDuration = regexp.MustCompile(`^([0-9]+(\.[0-9]+)? ?(us|usec|ms|msec|s|sec|seconds?|m|min|minutes?|h|hr|hours?|d|days?|w|weeks?) ?)+$`)

// JournalArgs turns compose-style `logs [flags] [name...]` into journalctl
// arguments; toUnits maps a bare word to its units, or returns an error to
// refuse it.
func JournalArgs(args []string, toUnits func(string) ([]string, error)) ([]string, []string, error) {
	var flags, units []string
	format := true
	want := false
	for _, a := range args {
		if want {
			flags = append(flags, a)
			want = false
			continue
		}
		if a == "--output" || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output=") {
			format = false
		}
		if journalValueFlags[a] {
			want = true
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			continue
		}
		u, err := toUnits(a)
		if err != nil {
			return nil, nil, err
		}
		units = append(units, u...)
	}
	if format {
		flags = append([]string{"-o", "short-iso"}, flags...)
	}
	return flags, units, nil
}
