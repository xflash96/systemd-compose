package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/systemd"
)

// VERB --help and help VERB print the manual's entry for that verb in
// each section that has one: its flags too, which the overview leaves out.
func TestVerbHelp_PrintsTheVerbsEntries(t *testing.T) {
	up := verbHelp("up")
	for _, want := range []string{"INSIDE A PROJECT:", "up [--dry-run]", "--wait-timeout T\n", "OUTSIDE A PROJECT:", "up UNIT...", "systemd-compose help"} {
		if !strings.Contains(up, want) {
			t.Errorf("verbHelp(up) lacks %q:\n%s", want, up)
		}
	}
	if strings.Contains(up, "down ") {
		t.Errorf("verbHelp(up) has other verbs:\n%s", up)
	}
	if !strings.Contains(verbHelp("restart"), "start|stop|restart") {
		t.Error("a verb in a combined line is found")
	}
}

// help WORD answers a systemctl verb with a line, and a word that is no
// verb with an error.
func TestHelpFor_AnswersVerbsAndRefusesOtherWords(t *testing.T) {
	if verbHelp("frobnicate") != "" || helpFor("frobnicate") == nil || !strings.Contains(fmt.Sprint(helpFor("lgos")), `did you mean "logs"`) {
		t.Error("help for a word that is no verb is an error")
	}
	if helpFor("status") != nil || !strings.Contains(verbHelp("exec"), "exec [-e KEY=VAL]") || !strings.Contains(verbHelp("ls"), "ANYWHERE:") {
		t.Error("help for a systemctl verb, for exec and for ls answers")
	}
	if helpFor("yaml") != nil || helpFor("man") != nil {
		t.Error("help yaml and help man answer")
	}
}

// --help is the verb's, except after run's service name, in probe's
// arguments, and in a systemctl verb's.
func TestWantsHelp_OnlyForOurVerbs(t *testing.T) {
	if !wantsHelp("up", []string{"--help"}) || wantsHelp("run", []string{"api", "npm", "--help"}) || !wantsHelp("run", []string{"--help"}) {
		t.Error("wantsHelp: --help is the verb's, except after run's service")
	}
	// The healthcheck runs as `probe ... -- CMD`: a -h in CMD is CMD's.
	if wantsHelp("probe", []string{"--interval", "2s", "--", "pg_isready", "-h", "127.0.0.1"}) || wantsHelp("status", []string{"--help"}) {
		t.Error("wantsHelp: probe's and systemctl's arguments are theirs")
	}
}

// compose's verbs are answered; a typo of ours gets the verb it is near;
// systemctl's verbs and anything else pass through.
func TestUnknownVerb_AnswersComposeVerbsAndTypos(t *testing.T) {
	for verb, want := range map[string]string{
		"pull":          "no images",
		"pause":         "stop SERVICE",
		"rebuild":       `did you mean "build"`,
		"logz":          `did you mean "logs"`,
		"status":        "",
		"list-timers":   "",
		"daemon-reload": "",
		"frobnicate":    "",
	} {
		err := unknownVerb(verb)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v, want it to pass through", verb, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", verb, err, want)
		}
	}
}

// Outside a project, up and down take no flags: up --dry-run UNIT handed to
// systemctl would be enable --now, and enable the unit.
func TestInstanceMode_RefusesProjectFlags(t *testing.T) {
	m := &systemd.Manager{User: true}
	for _, c := range [][]string{{"up", "--dry-run", "x.service"}, {"up", "--build", "x.service"}, {"up", "--force", "x.service"}, {"down", "-v", "x.service"}} {
		if err := instanceMode(m, c[0], c[1:]); err == nil || !strings.Contains(err.Error(), "needs a project") {
			t.Errorf("%q: %v", c, err)
		}
	}
}

// A bare ExitError passes its code on in silence. A step of ours that
// failed under one (down's "stop:") exits 1 and says the step: errors.As
// would see the ExitError inside and drop the words.
func TestOutcome_SilentOnlyForABareExitCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		msg  string
	}{
		{nil, 0, ""},
		{systemd.ExitError{Code: 5}, 5, ""},
		{fmt.Errorf("stop: %w", systemd.ExitError{Code: 5}), 1, "stop: exit 5"},
		{errors.New("no"), 1, "no"},
	} {
		if code, msg := outcome(c.err); code != c.code || msg != c.msg {
			t.Errorf("outcome(%v) = %d %q, want %d %q", c.err, code, msg, c.code, c.msg)
		}
	}
}

// An empty -p or --profile (an unset $NAME) is refused, not read as no name.
func TestRun_RefusesAnEmptyName(t *testing.T) {
	for _, args := range [][]string{{"-p", "", "config"}, {"--project-name=", "config"}, {"--profile=", "config"}, {"--profile", "", "config"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "needs a name") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// An unknown flag before the verb is refused in the tool's words: handed
// on, systemctl would deny the verb ("Unknown command verb 'logs'"). The
// flags that are verbs of their own pass.
func TestRun_RefusesAnUnknownFlagBeforeTheVerb(t *testing.T) {
	for _, args := range [][]string{{"--all", "ps"}, {"--no-pager", "logs"}, {"--dry-run", "up"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%q: %v", args, err)
		}
	}
	// the ones that are verbs of their own
	for _, args := range [][]string{{"--version"}, {"--help"}, {"-h"}} {
		if err := run(args); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}
