// version.go — what `systemd-compose --version` says: this program's
// version, then systemd's, since a report about one needs the other.
package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"
)

// version is set by a release build (-ldflags "-X main.version=v0.1.0").
// Otherwise Go's own stamp names it: a build from a tagged commit carries
// the tag (Go 1.24), any other build the commit it came from.
var version = ""

// rePseudo matches Go's pseudo-version for an untagged commit, which
// repeats the commit and its time that the line already shows.
var rePseudo = regexp.MustCompile(`-[0-9]{14}-[0-9a-f]{12}`)

func versionLine() string {
	v := version
	var commit, when string
	dirty := false
	if info, ok := debug.ReadBuildInfo(); ok {
		if mv := strings.TrimSuffix(info.Main.Version, "+dirty"); v == "" && mv != "" && mv != "(devel)" && !rePseudo.MatchString(mv) {
			v = mv
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.time":
				when, _, _ = strings.Cut(s.Value, "T")
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if v == "" {
		v = "devel"
	}
	var meta []string
	if commit != "" {
		meta = append(meta, "commit "+commit[:min(12, len(commit))])
	}
	if when != "" {
		meta = append(meta, when)
	}
	if dirty {
		meta = append(meta, "modified")
	}
	if len(meta) > 0 {
		v += " (" + strings.Join(meta, ", ") + ")"
	}
	return "systemd-compose " + v
}

func printVersion() error {
	fmt.Println(versionLine())
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		fmt.Println("systemd: systemctl --version failed:", err)
		return nil
	}
	first, _, _ := strings.Cut(string(out), "\n")
	fmt.Println(first)
	return nil
}
