package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

func loadYAML(t *testing.T, y string) (*Project, error) {
	t.Helper()
	_, path, write := testenv.ProjectFile(t, ConfigFileName)
	write(strings.TrimSpace(y) + "\n")
	return Load(path, Options{})
}

func TestLoad_RefusesWhatHasNoFaithfulForm(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown top-level key", "format: 3\nservices: {a: {command: x}}", `unknown key "format"`},
		{"version key by habit", "version: \"3.8\"\nservices: {a: {command: x}}", "no version key"},
		{"specifier in creates", "services: {a: {command: x, build: {run: [x], creates: \"%t/out\"}}}", "creates: no % here"},
		{"healthy with required false", "services: {a: {command: x, depends_on: {b: {condition: service_healthy, required: false}}}, b: {command: x, healthcheck: {test: [x]}}}", "would not enforce"},
		{"unknown service key", "services: {a: {command: x, colour: red}}", `unknown key "colour"`},
		{"a compose key", "services: {a: {command: x, ports: [80]}}", `"ports": no ports to publish`},
		{"every unknown key at once", "services: {a: {command: x, image: nginx, volumes: [x]}, b: {command: x, frob: 1}}", `3 keys this tool does not take`},
		{"a docker port mapping in listen", `services: {a: {command: x, listen: "8080:80"}}`, "docker port mapping"},
		{"a tab in the indentation", "services:\n\ta: {command: x}", "line 2: a tab in the indentation"},
		{"indented too deep", "services:\n  a:\n    command: x\n      oneshot: true", "look below it"},
		{"project name led by a dash", "name: -proj\nservices: {a: {command: x}}", "does not start with -"},
		{"dotted project name", "name: my.proj\nservices: {a: {command: x}}", "letters, digits, _ and -"},
		{"bad service name", "services: {a.b: {command: x}}", "service name"},
		{"missing command", "services: {a: {working_dir: .}}", "command: is required"},
		{"prefix char", "services: {a: {command: -node x}}", "starts with a character"},
		{"unset variable", "services: {a: {command: node $HOME/x}}", "HOME is not set"},
		{"unknown specifier", "services: {a: {command: node %z/x}}", "%z is not a systemd specifier"},
		{"percent before a digit", "services: {a: {command: x, environment: {A: \"50%5\"}}}", "for a literal percent write %%"},
		{"specifier newer than the floor", "services: {a: {command: x, environment: {A: \"%q\"}}}", "newer than systemd 248"},
		{"newline in command", "services:\n  a:\n    command: \"node x\\nExecStartPre=/bin/rm -rf /\"", "line 3: services: a: command: the value spans more than one line"},
		{"newline in optional env_file path", "services:\n  a:\n    command: node x\n    env_file: [{path: \"x\\nExecStartPre=/bin/id\", required: false}]", "spans more than one line"},
		{"literal block of two lines", "services:\n  a:\n    command: |\n      echo a\n      echo b\n", ">- folds it"},
		{"several unknown keys, in line order", "services:\n  a:\n    command: x\n    foo: 1\n    mem_limit: 5m\nversion: \"3\"\n", "line 4: service a: unknown key \"foo\"\n  line 5: service a: \"mem_limit\": resources: {memory: 5M}"},
		{"a problem in each of two services", "services:\n  a:\n    command: x\n    healthcheck: {test: [CMD, x]}\n  b:\n    command: x\n    restart: \"on-failure:5\"\n", "2 services have a problem:\n  line 4: service a: healthcheck"},
		{"an unclosed flow list", "services:\n  a:\n    command: [sh, -c, x\n  b: {command: x}\n", "the [ or { that is not closed is on that line or below it"},
		{"a socket file without a slash", "services: {a: {command: x, listen: api.sock}}", "a socket file needs a slash"},
		{"a host name in listen", "services: {a: {command: x, listen: \"localhost:8080\"}}", "write the IP (127.0.0.1:8080)"},
		{"a port out of range", "services: {a: {command: x, listen: \"70000\"}}", "a port is 1 to 65535"},
		{"two services on one port", "services:\n  a: {command: x, listen: \"127.0.0.1:19501\"}\n  b: {command: x, listen: \"19501\"}\n", "service b: listen: 19501: service a listens there too"},
		{"environment key with no value", "services:\n  a:\n    command: x\n    environment:\n      PASSTHRU:\n", `"PASSTHRU" has no value`},
		{"ulimits carried over", "services:\n  a:\n    command: x\n    ulimits:\n      nofile: {soft: 8192, hard: 16384}\n      nproc: 4096\n", "LimitNOFILE: 8192:16384, LimitNPROC: 4096"},
		{"an unset variable in two services", "services:\n  a: {command: \"x $A\"}\n  b: {command: \"x $B\"}\n", "2 services have a problem:\n  line 2: services: a: command: A is not set"},
		{"cpus carried over", "services: {a: {command: x, cpus: 0.25}}", "resources: {cpus: 0.25}"},
		{"healthcheck retries", "services: {a: {command: x, healthcheck: {test: [x], retries: 3}}}", "a shorter start_period fails sooner"},
		{"specifier in listen", "services: {a: {command: x, listen: \"%x/s\"}}", "not a systemd specifier"},
		{"specifier env with build", "services: {a: {command: x, environment: {D: \"%h/d\"}, build: {run: [x]}}}", "uses one"},
		{"bare semicolon", "services: {a: {command: node a ; node b}}", `";" is shell syntax`},
		{"a pipe", `services: {a: {command: "a --x | tee log"}}`, `"|" is shell syntax`},
		{"a redirect to a file", `services: {a: {command: "a --x 2>/tmp/log"}}`, `"2>/tmp/log" is shell syntax`},
		{"an and", `services: {a: {command: "a && b"}}`, `"&&" is shell syntax`},
		{"in a build step", `services: {a: {command: x, build: {run: ["make && make install"]}}}`, "write a shell line as sh -c"},
		{"a tilde", `services: {a: {command: "a ~/conf"}}`, "does not expand ~"},
		{"compose CMD prefix", `services: {a: {command: x, healthcheck: {test: [CMD, curl, -f, x]}}}`, `drop compose's "CMD"`},
		{"compose CMD-SHELL prefix", `services: {a: {command: x, healthcheck: {test: [CMD-SHELL, "curl -f x"]}}}`, "is [sh, -c, x] here"},
		{"bare env key", "services: {a: {command: x, environment: [PATH]}}", "write PATH=value"},
		{"bad env key", "services: {a: {command: x, environment: {1ABC: v}}}", "not a valid variable name"},
		{"bad restart", "services: {a: {command: x, restart: sometimes}}", "use no, on-failure, always or unless-stopped"},
		{"restart with a retry count", "services: {a: {command: x, restart: \"on-failure:5\"}}", "no faithful systemd form"},
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
		{"bad cpus", "services: {a: {command: x, resources: {cpus: -1}}}", "number of CPUs from 0.001"},
		{"cpus without end", "services: {a: {command: x, resources: {cpus: inf}}}", "number of CPUs from 0.001"},
		{"an io controller key, in any section", "services: {a: {command: x, listen: 8080, unit: {Socket: {IOWeight: 10}}}}", "io cgroup controller"},
		{"a blank pass-through value", "services: {a: {command: x, unit: {Service: {Nice: \"  \"}}}}", "an empty value resets"},
		{"Environment= in unit:", "services: {a: {command: x, unit: {Service: {Environment: A=1}}}}", "use environment:"},
		{"WorkingDirectory= in unit:", "services: {a: {command: x, unit: {Service: {WorkingDirectory: /tmp}}}}", "use working_dir:"},
		{"an empty pass-through list", "services: {a: {command: x, unit: {Service: {ExecStartPost: []}}}}", "an empty value resets"},
		{"cpus below what systemd applies", "services: {a: {command: x, resources: {cpus: 0.0005}}}", "number of CPUs from 0.001"},
		{"no memory", "services: {a: {command: x, resources: {memory: 0M}}}", "positive size"},
		{"environment key twice (map)", "services: {a: {command: x, environment: {X: 1, X: 2}}}", "already given"},
		{"environment key twice (list)", "services: {a: {command: x, environment: [X=1, X=2]}}", "X already given on line 1"},
		{"depends_on key twice", "services: {a: {command: x, depends_on: {b: {condition: service_started}, b: {condition: service_started}}}, b: {command: x}}", "already given"},
		{"healthcheck interval 0", "services: {a: {command: x, healthcheck: {test: [x], interval: 0}}}", "interval: must be more than 0"},
		{"healthcheck timeout 0s", "services: {a: {command: x, healthcheck: {test: [x], timeout: 0s}}}", "timeout: must be more than 0"},
		{"healthcheck on a oneshot", "services: {a: {command: x, oneshot: true, healthcheck: {test: [x]}}}", "on a oneshot has nothing to gate"},
		{"listen with a protocol", "services: {a: {command: x, listen: 8080/tcp}}", "takes no protocol"},
		{"listen IP:port with a protocol", "services: {a: {command: x, listen: \"127.0.0.1:8080/udp\"}}", "takes no protocol"},
		{"one service on one address twice", "services: {a: {command: x, listen: [18080, \"0.0.0.0:18080\"]}}", "this service already listens there"},
		{"a second yaml document", "services: {a: {command: x}}\n---\nservices: {b: {command: x}}", "a second yaml document"},
		{"env_file with a pattern", "services: {a: {command: x, env_file: [{path: \"*.env\", required: false}]}}", "no * ? or ["},
		{"unit name of 256 bytes", "name: q\nservices: {" + strings.Repeat("a", 246) + ": {command: x}}", "255 at most"},
		{"empty resources", "services: {a: {command: x, resources: {}}}", "is empty"},
		{"an unknown resources key beside a known one", "services: {a: {command: x, resources: {memory: 1G, swap: 1G}}}", `unknown key "swap"`},
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
		{"no services", "name: x", "services: is required"},
		{"unquoted specifier value", "services:\n  a:\n    command: x\n    environment:\n      D: %h/d", "must be quoted in yaml"},
		{"profiles not a list", "services: {a: {command: x, profiles: debug}}", "non-empty list of names"},
		{"bad profile name", "services: {a: {command: x, profiles: [-x]}}", "a profile name is"},
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

func TestLoad_ReadsAPlainProject(t *testing.T) {
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

// The project name is the namespace; its sources outside the yaml let two
// copies of one project coexist without an edit.
func TestLoad_TakesTheProjectNameFromEachSource(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	write := func(y string) {
		if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services: {a: {command: x}}\n")
	p, err := Load(path, Options{})
	if err != nil || p.NameFrom != "directory" || p.Name != sanitizeName(filepath.Base(dir)) {
		t.Fatalf("directory: %v %+v", err, p)
	}
	write("name: fromfile\nservices: {a: {command: x}}\n")
	if p, _ = Load(path, Options{}); p.Name != "fromfile" || p.NameFrom != "name:" {
		t.Errorf("name: -> %s from %s", p.Name, p.NameFrom)
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte("# local\nOTHER=1\n"+ProjectNameVar+"=\"from_dotenv\"\n"), 0o644)
	if p, _ = Load(path, Options{}); p.Name != "from_dotenv" || p.NameFrom != ".env" {
		t.Errorf(".env -> %s from %s", p.Name, p.NameFrom)
	}
	t.Setenv(ProjectNameVar, "from_env")
	if p, _ = Load(path, Options{}); p.Name != "from_env" || p.NameFrom != "environment" {
		t.Errorf("environment -> %s from %s", p.Name, p.NameFrom)
	}
	if p, _ = Load(path, Options{Name: "from_flag"}); p.Name != "from_flag" || p.NameFrom != "-p" {
		t.Errorf("-p -> %s from %s", p.Name, p.NameFrom)
	}
	if p, err := Load(path, Options{Name: "my-kit"}); err != nil || p.Name != "my-kit" {
		t.Errorf("a dashed -p: %v", err)
	}
	if _, err := Load(path, Options{Name: "my.kit"}); err == nil || !strings.Contains(err.Error(), "from -p") {
		t.Errorf("a dotted -p should be refused, got %v", err)
	}
}

// A project's dashes are escaped in every unit it names, as systemd-escape
// writes them: a dash would nest my-app.slice inside my.slice, and project
// my-app's api would read as project my's app-api.
func TestUnitNames_EscapeTheProjectsDashes(t *testing.T) {
	p, err := loadYAML(t, "name: my-app\nservices:\n  app-api: {command: /bin/true, schedule: daily}\n")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.UnitNames(), " ")
	want := `my\x2dapp.slice my\x2dapp-app-api.service my\x2dapp-app-api.timer my\x2dapp.target`
	if got != want {
		t.Errorf("units %s, want %s", got, want)
	}
	if p.LogIdentifier(p.Service("app-api")) != `my\x2dapp-app-api` {
		t.Errorf("log identifier %s", p.LogIdentifier(p.Service("app-api")))
	}
	for _, u := range p.UnitNames() {
		project, service := SplitUnitName(u)
		want := ""
		if strings.Contains(u, "-app-api") {
			want = "app-api"
		}
		if project != "my-app" || service != want {
			t.Errorf("SplitUnitName(%s) = %q, %q; want my-app, %q", u, project, service, want)
		}
	}
}

func TestSanitizeName_MakesADirectoryNameAProjectName(t *testing.T) {
	cases := map[string]string{"web": "web", "my-proj.v2": "my_proj_v2", "my-kit": "my_kit", "---": "", "a b": "a_b"}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindConfig_WalksUpFromTheDirectory(t *testing.T) {
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

// A merge key at the top level belongs to no key at all: the interpolation
// walk must not index an empty path for it.
func TestLoad_MergesATopLevelMergeKey(t *testing.T) {
	p, err := loadYAML(t, "x-top: &top\n  resources: {memory: 100M}\n<<: *top\nservices: {a: {command: x}}")
	if err != nil || p.Resources == nil || p.Resources.Memory != "100M" {
		t.Fatalf("a top-level << must merge: %v %+v", err, p)
	}
}

// An x- block is expanded where a service uses it: unit content aliased from
// one stays raw, an unused one is never read, and a service merely named
// x-something is a service like any other.
func TestLoad_ExpandsAnExtensionWhereItIsUsed(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PORT=8080\n"), 0o644)
	path := filepath.Join(dir, ConfigFileName)
	os.WriteFile(path, []byte(`x-raw: &raw
  Service:
    ExecStartPre: /bin/sh -c "echo $HOME"
x-unused:
  environment: {A: "${NEVER_SET}"}
services:
  a:
    command: node a
    unit: *raw
  x-api:
    command: node api
    environment: {PORT: "${PORT}"}
`), 0o644)
	p, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Service("a").Unit.Has("Service", "ExecStartPre", `/bin/sh -c "echo $HOME"`) {
		t.Errorf("aliased unit content must stay raw: %+v", p.Service("a").Unit)
	}
	if api := p.Service("x-api"); api == nil || api.Environment[0].Value != "8080" {
		t.Errorf("a service named x-api is interpolated like any other: %+v", api)
	}
}

// A symlinked yaml is a project where the link is: its name, its paths and
// its .env are the link's directory's, not the target's.
func TestLoad_PlacesASymlinkedYamlWhereTheLinkIs(t *testing.T) {
	testenv.ClearOverrides(t)
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "systemd-compose.yaml"), []byte("services:\n  a: {command: /bin/true, working_dir: .}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	here := filepath.Join(t.TempDir(), "linked_here")
	if err := os.Mkdir(here, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "systemd-compose.yaml"), filepath.Join(here, "systemd-compose.yaml")); err != nil {
		t.Fatal(err)
	}
	p, err := Load(filepath.Join(here, "systemd-compose.yaml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	hereReal, _ := filepath.EvalSymlinks(here)
	if p.Dir != hereReal || p.Name != "linked_here" || p.Service("a").WorkingDir != hereReal {
		t.Errorf("symlinked yaml: dir %q, name %q, working_dir %q; want %q, linked_here, %q", p.Dir, p.Name, p.Service("a").WorkingDir, hereReal, hereReal)
	}
}

// A project name is short enough for every unit name, a one-off's too,
// which systemd refuses with a bare line past 255 bytes.
func TestLoad_RefusesANameTooLongForAUnit(t *testing.T) {
	long := strings.Repeat("a", maxProjectName+1)
	if _, err := loadYAML(t, "name: "+long+"\nservices:\n  a: {command: /bin/true}\n"); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a %d-character name: %v", len(long), err)
	}
	if _, err := loadYAML(t, "name: "+strings.Repeat("a", maxProjectName)+"\nservices:\n  a: {command: /bin/true}\n"); err != nil {
		t.Errorf("a %d-character name: %v", maxProjectName, err)
	}
	// a dash counts as the four bytes units spell it with
	dashes := strings.Repeat("a-", maxProjectName/5) + "a"
	if _, err := loadYAML(t, "name: "+dashes+"\nservices:\n  a: {command: /bin/true}\n"); err == nil || !strings.Contains(err.Error(), "a - counting 4") {
		t.Errorf("a %d-character name of %d escaped: %v", len(dashes), len(EscapeName(dashes)), err)
	}
	if n := len("build-" + strings.Repeat("a", maxProjectName) + "-4194304-9999.service"); n > 255 {
		t.Errorf("the longest one-off name is %d bytes", n)
	}
}

// A trailing --- still leaves one document.
func TestLoad_TrailingSeparatorIsOneDocument(t *testing.T) {
	if _, err := loadYAML(t, "services: {a: {command: x}}\n---\n"); err != nil {
		t.Errorf("a trailing ---: %v", err)
	}
}
