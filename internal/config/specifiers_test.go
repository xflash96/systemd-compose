package config

import (
	"slices"
	"strings"
	"testing"
)

// A | or > block ends in one newline nobody typed, and loads without it; a
// specifier in what a service runs is noted, unless it leads a path.
func TestLoad_ClipsBlockScalarsAndNotesSpecifiers(t *testing.T) {
	p, err := loadYAML(t, `
services:
  a:
    command: >
      sleep
      3600
  b:
    command: [date, "+%s %m"]
    environment: {HOME_BIN: "%h/bin", PCT: "100%%"}
  c:
    command: "%h/bin/tool --uid %U"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services[0].Command.Line; got != "sleep 3600" {
		t.Errorf("folded command = %q", got)
	}
	want := []string{
		"service b: command: systemd fills in %s (your login shell), %m (the machine ID) when it runs the service; for a literal percent sign write %%, as in %%s",
		"service c: command: systemd fills in %U (your user ID) when it runs the service; for a literal percent sign write %%, as in %%U",
	}
	if !slices.Equal(p.Notes, want) {
		t.Errorf("notes =\n%q\nwant\n%q", p.Notes, want)
	}
}

// A % that is no specifier because no letter or digit follows is the
// program's, as systemd keeps it: curl's -w '%{http_code}', a shell modulo.
func TestSpecifiers_KeepsAPercentBeforeNoLetter(t *testing.T) {
	p, err := loadYAML(t, `services: {a: {command: [sh, -c, "curl -w '%{http_code}' x; echo $$(( 7 % 2 )); echo 100% done; echo 50%"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 0 {
		t.Errorf("no specifier, no note: %q", p.Notes)
	}
}

// A build step runs through systemd-run, which expands no specifier: a %
// is the program's, and a date format is legal.
func TestParseBuild_TakesAPercentAsWritten(t *testing.T) {
	p, err := loadYAML(t, `services: {a: {command: x, build: {run: ["sh -c 'date +%s > stamp'", "printf %d 7"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services[0].Build.Run[0]; got != "sh -c 'date +%s > stamp'" {
		t.Errorf("step = %q", got)
	}
	if len(p.Notes) != 0 {
		t.Errorf("a build step's %% is no specifier to note: %q", p.Notes)
	}
}

// One message names every refused % letter of a value, so a date format
// takes one round.
func TestSpecifiers_NamesEveryRefusedLetterAtOnce(t *testing.T) {
	err := specifiers("+%Y-%m-%d")
	if err == nil || !strings.Contains(err.Error(), "%Y, %d are specifiers newer than systemd 248") || !strings.Contains(err.Error(), "%%Y %%m %%d") {
		t.Errorf("specifiers = %v", err)
	}
}

// A path the tool reads that starts with ~ is refused as unexpanded: no
// shell reads the yaml.
func TestLiteralPath_RefusesATilde(t *testing.T) {
	for _, y := range []string{
		"services:\n  a: {command: /bin/true, working_dir: ~/app}\n",
		"services:\n  a: {command: /bin/true, env_file: ~/a.env}\n",
	} {
		_, err := loadYAML(t, y)
		if err == nil || !strings.Contains(err.Error(), "~ is not expanded") {
			t.Errorf("%q: %v", y, err)
		}
	}
}
