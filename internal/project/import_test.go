package project

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xflash96/systemd-compose/internal/render"
)

// Environment= splits as systemd splits it: quotes group, a backslash
// escapes.
func TestEnvWords_SplitAsSystemdDoes(t *testing.T) {
	got := envWords(`"A=hello world" B=1 'C=x y' D=a\ b E=\x41`)
	want := []string{"A=hello world", "B=1", "C=x y", "D=a b", "E=A"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The import keeps what systemd would run: a drop-in's empty assignment
// resets, a later variable wins, a $ stays literal, ~ and %h are the home
// directory, and what a service's unit: cannot say is left out, with a
// note.
func TestImportService_KeepsWhatSystemdWouldRun(t *testing.T) {
	dirs := render.ReadUnit(`[Unit]
Description=d
[Service]
WorkingDirectory=-~/app
Environment="A=one two" B=$HOME
Environment=A=three
EnvironmentFile=-%h/x.env
EnvironmentFile=/etc/*.env
Slice=background.slice
ExecStart=/bin/true
Nice=5
[X-Extra]
Key=v
[Install]
WantedBy=default.target
`)
	dirs = append(dirs, render.ReadUnit("[Service]\nNice=\nNice=7\n")...)
	svc, notes := importService(dirs, "/home/u")
	out, err := yaml.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"working_dir: /home/u/app\n",
		"A: three\n", "B: $$HOME\n",
		"- {path: /home/u/x.env, required: false}\n",
		"Description: d\n", "ExecStart: /bin/true\n", `Nice: "7"` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, gone := range []string{"Slice", "WantedBy", "X-Extra", "*.env", "Nice: \"5\""} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should be left out of\n%s", gone, got)
		}
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{"Slice=background.slice", "WantedBy=default.target", "[X-Extra]", "EnvironmentFile=/etc/*.env"} {
		if !strings.Contains(all, want) {
			t.Errorf("no note for %s in\n%s", want, all)
		}
	}
}

// A user unit with no WorkingDirectory= runs in the home directory, so
// the import says so: a project's service would run in the yaml's.
func TestImportService_HomeIsTheDefaultWorkingDir(t *testing.T) {
	svc, _ := importService(render.ReadUnit("[Service]\nExecStart=/bin/true\n"), "/home/u")
	out, _ := yaml.Marshal(svc)
	if !strings.Contains(string(out), "working_dir: /home/u # where the unit ran") {
		t.Errorf("got\n%s", out)
	}
}

func TestHomePath(t *testing.T) {
	cases := map[string]string{"~": "/home/u", "%h/a": "/home/u/a", "/srv/$x": "/srv/$$x", "%t/a": "", "rel": "", "~user": ""}
	for in, want := range cases {
		got, ok := homePath(in, "/home/u")
		if got != want || ok != (want != "") {
			t.Errorf("homePath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
