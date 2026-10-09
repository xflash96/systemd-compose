package cli

import (
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/xflash96/systemd-compose/internal/systemd"
)

// version is the release build's stamp, which Main is given. Otherwise
// Go's own stamp names the build: a build from a tagged commit carries the
// tag (Go 1.24), any other build the commit it came from.
var version string

// rePseudo matches Go's pseudo-versions (vX.0.0-T-H before any tag,
// vX.Y.Z-0.T-H after a release, vX.Y.Z-pre.0.T-H after a pre-release),
// which repeat the commit and time the line already shows.
var rePseudo = regexp.MustCompile(`[-.][0-9]{14}-[0-9a-f]{12}$`)

// tagOf is the tag in Go's stamp of the main module, "" for a build that
// is not at one: "(devel)" or a pseudo-version. A modified tree's +dirty
// is dropped here; the line says "modified" instead.
func tagOf(stamp string) string {
	v := strings.TrimSuffix(stamp, "+dirty")
	if v == "(devel)" || rePseudo.MatchString(v) {
		return ""
	}
	return v
}

func versionLine() string {
	v := version
	var commit, when string
	dirty := false
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" {
			v = tagOf(info.Main.Version)
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

// printVersion prints this program's line, then systemd's; a failing
// systemctl is reported on stderr, so stdout holds only versions. Line 1,
// "systemd-compose V[ (meta)]", is read by scripts/check-build.sh and the
// e2e tests; line 2 by the e2e tests.
func printVersion() {
	fmt.Println(versionLine())
	line, err := systemd.Version()
	if err != nil {
		fmt.Fprintln(os.Stderr, "systemd-compose: systemctl --version:", err)
		return
	}
	fmt.Println(line)
}
