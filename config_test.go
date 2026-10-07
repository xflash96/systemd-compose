package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadYAML(t *testing.T, y string) (*Project, error) {
	t.Helper()
	t.Setenv(ProjectNameVar, "") // the tool's own override must not leak into its tests
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(y)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path, "")
}

func TestLoadRefusals(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown top-level key", "format: 3\nservices: {a: {command: x}}", `unknown key "format"`},
		{"version key by habit", "version: \"3.8\"\nservices: {a: {command: x}}", "no version key"},
		{"healthy with required false", "services: {a: {command: x, depends_on: {b: {condition: service_healthy, required: false}}}, b: {command: x, healthcheck: {test: [x]}}}", "would not enforce"},
		{"unknown service key", "services: {a: {command: x, ports: [80]}}", `unknown key "ports"`},
		{"dashed project name", "name: my-proj\nservices: {a: {command: x}}", "no dash"},
		{"bad service name", "services: {a.b: {command: x}}", "service name"},
		{"missing command", "services: {a: {working_dir: .}}", "command: is required"},
		{"prefix char", "services: {a: {command: -node x}}", "starts with a character"},
		{"unset variable", "services: {a: {command: node $HOME/x}}", "HOME is not set"},
		{"unknown specifier", "services: {a: {command: node %z/x}}", "%z in"},
		{"trailing percent", "services: {a: {command: x, environment: {A: \"50%\"}}}", "write %% for a literal"},
		{"specifier of 251", "services: {a: {command: x, environment: {A: \"%q\"}}}", "needs systemd 251"},
		{"specifier in listen", "services: {a: {command: x, listen: \"%x/s\"}}", "not a systemd specifier"},
		{"specifier in build", "services: {a: {command: x, build: {run: [\"x %h\"]}}}", "systemd-run"},
		{"specifier env with build", "services: {a: {command: x, environment: {D: \"%h/d\"}, build: {run: [x]}}}", "uses one"},
		{"bare semicolon", "services: {a: {command: node a ; node b}}", "bare ;"},
		{"bare env key", "services: {a: {command: x, environment: [PATH]}}", "reserved"},
		{"bad env key", "services: {a: {command: x, environment: {1ABC: v}}}", "not a valid variable name"},
		{"bad restart", "services: {a: {command: x, restart: sometimes}}", "use no, on-failure or always"},
		{"schedule with always", "services: {a: {command: x, schedule: hourly, restart: always}}", "finished job"},
		{"schedule with healthcheck", "services: {a: {command: x, schedule: hourly, healthcheck: {test: [x]}}}", "nothing to gate"},
		{"depends on unknown", "services: {a: {command: x, depends_on: [b]}}", "is not a service in this file"},
		{"depends on self", "services: {a: {command: x, depends_on: [a]}}", "itself"},
		{"depends on scheduled", "services: {a: {command: x, depends_on: [b]}, b: {command: x, schedule: hourly}}", "runs on its timer"},
		{"healthy without healthcheck", "services: {a: {command: x, depends_on: {b: {condition: service_healthy}}}, b: {command: x}}", "in disguise"},
		{"completed without oneshot", "services: {a: {command: x, depends_on: {b: {condition: service_completed_successfully}}}, b: {command: x}}", "oneshot: true"},
		{"bad condition", "services: {a: {command: x, depends_on: {b: {condition: ready}}}, b: {command: x}}", "use service_started"},
		{"cycle", "services: {a: {command: x, depends_on: [b]}, b: {command: x, depends_on: [a]}}", "cycle"},
		{"install refused", "services: {a: {command: x, unit: {Install: {WantedBy: x}}}}", "[Install] is refused"},
		{"io refused", "services: {a: {command: x, unit: {Service: {IOWeight: 10}}}}", "io cgroup controller"},
		{"timer without schedule", "services: {a: {command: x, unit: {Timer: {OnBootSec: 1}}}}", "[Timer] only applies"},
		{"unknown section", "services: {a: {command: x, unit: {Path: {PathExists: /x}}}}", `section "Path"`},
		{"socket without listen", "services: {a: {command: x, unit: {Socket: {Backlog: 8}}}}", "[Socket] only applies"},
		{"listen with schedule", "services: {a: {command: x, schedule: hourly, listen: 8080}}", "a scheduled job or a oneshot"},
		{"listen with oneshot", "services: {a: {command: x, oneshot: true, listen: 8080}}", "a scheduled job or a oneshot"},
		{"empty listen", "services: {a: {command: x, listen: []}}", "listen: is empty"},
		{"listen as a map", "services: {a: {command: x, listen: {stream: 80}}}", "an address or a list"},
		{"bad memory", "services: {a: {command: x, resources: {memory: lots}}}", "suffix"},
		{"bad cpus", "services: {a: {command: x, resources: {cpus: -1}}}", "positive number"},
		{"empty resources", "services: {a: {command: x, resources: {}}}", "is empty"},
		{"bad on_change", "services: {a: {command: x, on_change: maybe}}", "use restart or start-only"},
		{"env_file without path", "services: {a: {command: x, env_file: [{required: false}]}}", "path: is required"},
		{"empty healthcheck test", "services: {a: {command: x, healthcheck: {test: []}}}", "non-empty list"},
		{"empty build run", "services: {a: {command: x, build: {run: []}}}", "non-empty list of commands"},
		{"bad duration", "services: {a: {command: x, healthcheck: {test: [x], interval: soon}}}", "not a systemd time span"},
		{"duplicate service", "services: {a: {command: x}, a: {command: y}}", "already given on line"},
		{"duplicate key", "services: {a: {command: x, command: y}}", "already given on line"},
		{"duplicate services block", "services: {a: {command: x}}\nservices: {b: {command: y}}", "already given on line"},
		{"unclosed quote", "services: {a: {command: '\"true'}}", "unclosed"},
		{"start-only with restart edge", "services: {a: {command: x, on_change: start-only, depends_on: {b: {restart: true}}}, b: {command: x}}", "contradicts"},
		{"specifier in working_dir", "services: {a: {command: x, working_dir: '%h/app'}}", "must be literal"},
		{"specifier in env_file", "services: {a: {command: x, env_file: '%h/.env'}}", "must be literal"},
		{"bad directive name", "services: {a: {command: x, unit: {Service: {'Timeout Stop': 1}}}}", "not a directive name"},
		{"command and raw ExecStart", "services: {a: {command: x, unit: {Service: {ExecStart: /bin/sh -c x}}}}", "keep one"},
		{"entrypoint and raw ExecStart", "services: {a: {entrypoint: x, unit: {Service: {ExecStart: /bin/sh -c x}}}}", "keep one"},
		{"empty command list", "services: {a: {command: []}}", "is empty"},
		{"empty entrypoint", "services: {a: {entrypoint: '', command: x}}", "is empty"},
		{"prefix in a list program", "services: {a: {command: [-x, y]}}", "starts with a character"},
		{"prefix in the entrypoint", "services: {a: {entrypoint: '@x', command: y}}", "starts with a character"},
		{"empty program word", "services: {a: {command: ['', y]}}", "no command word"},
		{"specifier in a list word", "services: {a: {command: [x, '%z']}}", "not a systemd specifier"},
		{"no command no raw", "services: {a: {working_dir: .}}", "command: is required"},
		{"no services", "name: x", "services: is required"},
		{"empty services", "services: {}", "services: is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadYAML(t, c.yaml)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestLoadGood(t *testing.T) {
	p, err := loadYAML(t, `
services:
  web:
    command: node server.mjs
    depends_on: [db]
    environment: [A=1, B=two words]
    env_file: .env
  db:
    command: postgres -D data
    restart: always
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Services) != 2 || p.Services[0].Name != "web" || p.Services[1].Name != "db" {
		t.Fatalf("services out of order: %+v", p.Services)
	}
	web := p.Services[0]
	if web.WorkingDir != p.Dir {
		t.Errorf("working_dir default = %s, want project dir %s", web.WorkingDir, p.Dir)
	}
	if len(web.DependsOn) != 1 || web.DependsOn[0].Condition != "service_started" || web.DependsOn[0].Required {
		t.Errorf("depends_on short form = %+v", web.DependsOn)
	}
	if len(web.Environment) != 2 || web.Environment[1].Value != "two words" {
		t.Errorf("environment list form = %+v", web.Environment)
	}
	if !web.EnvFiles[0].Required || !filepath.IsAbs(web.EnvFiles[0].Path) {
		t.Errorf("env_file = %+v", web.EnvFiles)
	}
	if web.OnChange != "restart" {
		t.Errorf("on_change default = %q", web.OnChange)
	}
	if p.Name != sanitizeName(filepath.Base(p.Dir)) || p.NameFrom != "directory" {
		t.Errorf("name derived = %q (from %s) for %q", p.Name, p.NameFrom, p.Dir)
	}
}

// The raw form: unit: Service: ExecStart: stands in for command:.
func TestRawExecStart(t *testing.T) {
	p, err := loadYAML(t, `
services:
  a:
    unit: {Service: {ExecStart: /bin/sh -c "echo $HOME"}}
    healthcheck: {test: [x], interval: 1h30min}
`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Services[0].Command.Empty() || !p.Services[0].Unit.hasKey("Service", "ExecStart") {
		t.Errorf("raw ExecStart not carried: %+v", p.Services[0])
	}
	if p.Services[0].Healthcheck.Interval != "1h30min" {
		t.Errorf("compact time span refused: %+v", p.Services[0].Healthcheck)
	}
}

// The project name is the namespace; its sources outside the yaml let two
// copies of one project coexist without an edit.
func TestProjectNameSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	write := func(y string) {
		if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services: {a: {command: x}}\n")
	t.Setenv(ProjectNameVar, "")
	p, err := Load(path, "")
	if err != nil || p.NameFrom != "directory" || p.Name != sanitizeName(filepath.Base(dir)) {
		t.Fatalf("directory: %v %+v", err, p)
	}
	write("name: fromfile\nservices: {a: {command: x}}\n")
	if p, _ = Load(path, ""); p.Name != "fromfile" || p.NameFrom != "name:" {
		t.Errorf("name: -> %s from %s", p.Name, p.NameFrom)
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte("# local\nOTHER=1\n"+ProjectNameVar+"=\"from_dotenv\"\n"), 0o644)
	if p, _ = Load(path, ""); p.Name != "from_dotenv" || p.NameFrom != ".env" {
		t.Errorf(".env -> %s from %s", p.Name, p.NameFrom)
	}
	t.Setenv(ProjectNameVar, "from_env")
	if p, _ = Load(path, ""); p.Name != "from_env" || p.NameFrom != "environment" {
		t.Errorf("environment -> %s from %s", p.Name, p.NameFrom)
	}
	if p, _ = Load(path, "from_flag"); p.Name != "from_flag" || p.NameFrom != "-p" {
		t.Errorf("-p -> %s from %s", p.Name, p.NameFrom)
	}
	if _, err := Load(path, "no-dash"); err == nil || !strings.Contains(err.Error(), "from -p") {
		t.Errorf("dashed -p should be refused, got %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{"web": "web", "my-proj.v2": "my_proj_v2", "my-kit": "my_kit", "---": "", "a b": "a_b"}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindConfig(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindConfig(deep); got != "" {
		t.Errorf("found %q in an empty tree", got)
	}
	want := filepath.Join(root, "a", ConfigFileName)
	if err := os.WriteFile(want, []byte("services: {a: {command: x}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindConfig(deep); got != want {
		t.Errorf("FindConfig = %q, want %q", got, want)
	}
}
