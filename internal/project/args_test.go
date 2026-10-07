package project

import (
	"strings"
	"testing"
)

// A project verb's flags are checked before the yaml is read, compose's
// answered in their own terms.
func TestCheckArgs_AnswersComposeFlags(t *testing.T) {
	for _, c := range []struct {
		verb string
		args []string
		want string // "" = accepted
		out  int    // arguments kept
	}{
		{"up", []string{"-d", "--build"}, "", 1},
		{"up", []string{"--remove-orphans"}, "already retires", 0},
		{"up", []string{"--frob"}, `unknown flag "--frob"`, 0},
		{"up", []string{"api"}, "no service names", 0},
		{"down", []string{"-v"}, "no volumes", 0},
		{"down", []string{"api"}, "stop SERVICE", 0},
		{"ps", []string{"-a"}, "", 0},
		{"ps", []string{"api"}, "logs SERVICE", 0},
		{"config", []string{"--services"}, "takes no arguments", 0},
		{"logs", []string{"-f", "api"}, "", 2},
		{"stop", []string{"-t", "5", "api"}, "TimeoutStopSec", 0},
		{"stop", []string{"--timeout=5"}, "TimeoutStopSec", 0},
		{"start", []string{"--no-deps", "api"}, "what it requires", 0},
		{"build", []string{"--no-cache"}, "always runs every step", 0},
		{"restart", []string{"--frob"}, `restart: unknown flag "--frob" (restart takes no flags)`, 0},
		{"restart", []string{"api", "db"}, "", 2},
		{"up", []string{"--wait-timeout", "0"}, "0 would watch nothing", 0},
		{"up", []string{"--wait-timout", "30"}, "--wait-timeout T", 0}, // up's flag list names it
	} {
		out, err := CheckArgs(c.verb, c.args)
		switch {
		case c.want == "" && (err != nil || len(out) != c.out):
			t.Errorf("%s %v: %v %v", c.verb, c.args, out, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s %v: %v, want %q", c.verb, c.args, err, c.want)
		}
	}
}
