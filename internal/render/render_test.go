package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

func loadYAML(t *testing.T, y string) (*config.Project, error) {
	t.Helper()
	_, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	write(strings.TrimSpace(y) + "\n")
	return config.Load(path, config.Options{})
}

// CPUQuota= takes a percent with two decimals at most, and cpus*100 in
// floating point is 28.999999999999996 for 0.29, which systemd refuses.
func TestCPUQuota_TwoDecimalsAtMost(t *testing.T) {
	for cpus, want := range map[float64]string{0.29: "29%", 1.1: "110%", 2.3: "230%", 0.125: "12.5%", 0.0729: "7.29%", 2: "200%", 0.57: "57%"} {
		if got := CPUQuota(cpus); got != want {
			t.Errorf("CPUQuota(%v) = %q, want %q", cpus, got, want)
		}
	}
}

// A value the yaml writes (schedule: accuracy:) never gives way to a unit:
// pass-through of the same directive.
func TestRender_RefusesUnitOverWrittenValue(t *testing.T) {
	p, err := loadYAML(t, "services: {a: {command: /bin/true, schedule: {calendar: daily, accuracy: 1min}, unit: {Timer: {AccuracySec: 5s}}}}")
	if err == nil {
		_, err = Render(p, RenderOptions{Exe: "/x"})
	}
	if err == nil || !strings.Contains(err.Error(), "collides with schedule: accuracy") {
		t.Errorf("%v, want it to collide with schedule: accuracy", err)
	}
}

// A healthcheck probe that the service's build makes renders before the
// build has run, as a command: the build makes does.
func TestRender_HealthcheckProbeTheBuildMakes(t *testing.T) {
	p, err := loadYAML(t, "name: p\nservices:\n  a:\n    command: /bin/true\n    build: {run: [/bin/true], creates: dist}\n    healthcheck: {test: [./dist/ready]}\n")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/opt/systemd-compose"})
	if err != nil {
		t.Fatalf("a probe in creates: before the build: %v", err)
	}
	if !strings.Contains(units[1].Text, filepath.Join(p.Dir, "dist", "ready")) {
		t.Errorf("the probe's path is not the one creates: makes:\n%s", units[1].Text)
	}
}

// A oneshot takes several ExecStart= lines through unit:, as systemd
// allows; a long-running service takes one.
func TestRender_OneshotTakesSeveralExecStart(t *testing.T) {
	p, err := loadYAML(t, "services:\n  a:\n    oneshot: true\n    unit: {Service: {ExecStart: [/bin/true, /bin/false]}}\n")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x"})
	if err != nil {
		t.Fatalf("a oneshot with two ExecStart=: %v", err)
	}
	if text := units[1].Text; !strings.Contains(text, "ExecStart=/bin/true\nExecStart=/bin/false\n") {
		t.Errorf("render:\n%s", text)
	}
	p, err = loadYAML(t, "services:\n  a:\n    unit: {Service: {ExecStart: [/bin/true, /bin/false]}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(p, RenderOptions{Exe: "/x"}); err == nil {
		t.Error("a long-running service took two ExecStart=")
	}
}

// An address in both listen: and unit: Socket: ListenStream= is refused at
// the render: the socket would bind it twice and fail with "in use".
func TestRender_RefusesAddressListenedTwice(t *testing.T) {
	p, err := loadYAML(t, "services:\n  a:\n    command: /bin/true\n    listen: [\"127.0.0.1:8080\"]\n    unit: {Socket: {ListenStream: \"127.0.0.1:8080\"}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(p, RenderOptions{Exe: "/x"}); err == nil || !strings.Contains(err.Error(), "listed already") {
		t.Errorf("twice: %v", err)
	}
}

// The render of testdata/basic matches its golden file; UPDATE_GOLDEN=1
// writes the file anew.
func TestRender_MatchesGolden(t *testing.T) {
	testenv.ClearOverrides(t)
	bin := testenv.FakeBin(t, "node", "curl", "psql", "backup")
	yamlPath, _ := filepath.Abs("testdata/basic/systemd-compose.yaml")
	p, err := config.Load(yamlPath, config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/opt/systemd-compose", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, u := range units {
		b.WriteString("### " + u.Name + "\n" + u.Text + "\n")
	}
	got := b.String()
	got = strings.ReplaceAll(got, filepath.Dir(yamlPath), "@DIR@")
	got = strings.ReplaceAll(got, bin, "@BIN@")

	golden := "testdata/basic/golden.txt"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(want) != got {
		t.Errorf("render differs from %s\n--- got ---\n%s", golden, got)
	}

	// UnitNames is the declared set orphans() trusts: exactly what Render
	// emits, in order (the golden pins the names themselves).
	var rendered []string
	for _, u := range units {
		rendered = append(rendered, u.Name)
	}
	if names := p.UnitNames(); strings.Join(names, ",") != strings.Join(rendered, ",") {
		t.Errorf("UnitNames = %v, Render emits %v", names, rendered)
	}
}

// Render refuses what only the filesystem or the merge can decide.
func TestRender_RefusesByFilesystemAndMerge(t *testing.T) {
	testenv.ClearOverrides(t)
	bin := testenv.FakeBin(t, "node", "sh")
	cases := []struct{ name, yaml, want string }{
		{"newline in a pass-through value", "services:\n  a:\n    command: node x\n    unit: {Service: {TimeoutStopSec: \"10\\n[Install]\\nWantedBy=default.target\"}}", "newline"},
		{"OnCalendar owned by schedule", `
services:
  a:
    command: node x
    schedule: hourly
    unit: {Timer: {OnCalendar: daily}}`, "collides with schedule"},
		{"restart collides", `
services:
  a:
    command: node x
    restart: always
    unit: {Service: {Restart: no}}`, "collides with restart"},
		{"slice is structural", `
services:
  a:
    command: node x
    unit: {Service: {Slice: other.slice}}`, "collides with systemd-compose"},
		{"unresolvable command", `
services:
  a:
    command: nosuchprogram --flag`, "not found on the search path"},
		{"relative path not executable", `
services:
  a:
    command: ./missing.sh`, "not an executable file"},
		{"an env file overriding environment", `
services:
  a:
    command: node x
    environment: {PORT: "8080"}
    env_file: [ports.env]`, "environment: PORT would lose to env_file ports.env"},
		{"the last env file setting a key decides", `
services:
  a:
    command: node x
    environment: {PORT: "8080"}
    env_file: [same.env, ports.env]`, "would lose to env_file ports.env"},
		{"an export line systemd drops", `
services:
  a:
    command: node x
    env_file: [export.env]`, `env_file export.env: line 1: "export TOKEN": systemd drops this line`},
		{"working_dir missing", `
services:
  a:
    command: node x
    working_dir: nowhere`, "does not exist"},
		{"required env_file missing", `
services:
  a:
    command: node x
    env_file: nope.env`, "env_file"},
		{"non-repeatable with a list", `
services:
  a:
    command: node x
    unit: {Service: {TimeoutStopSec: ["1", "2"]}}`, "takes one value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, path, write := testenv.ProjectFile(t, config.ConfigFileName)
			write(strings.TrimSpace(c.yaml) + "\n")
			for name, text := range map[string]string{"ports.env": "PORT=3000\n", "same.env": "PORT=8080\n", "export.env": "export TOKEN=x\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := config.Load(path, config.Options{})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			_, err = Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

// The raw ExecStart form renders as given, and each environment variable
// gets an Environment= line of its own.
func TestRender_RawExecStartAndEnvironmentLines(t *testing.T) {
	bin := testenv.FakeBin(t, "node")
	p, err := loadYAML(t, "services:\n  a:\n    environment: {PORT: \"8080\", PCT: \"100%%\"}\n    unit: {Service: {ExecStart: /bin/sh -c \"echo $HOME\"}}\n")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	text := units[1].Text // after the slice
	for _, want := range []string{"ExecStart=/bin/sh -c \"echo $HOME\"\n", "Environment=PORT=8080\n", "Environment=PCT=100%%\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}

// The slice is rendered without resources: too, so it is registered with the
// project and never becomes an orphan when the cap is dropped.
func TestRender_AlwaysRendersSlice(t *testing.T) {
	p, err := loadYAML(t, "name: plain\nservices: {a: {command: node x}}")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "node")}})
	if err != nil {
		t.Fatal(err)
	}
	if units[0].Name != "plain.slice" || strings.Contains(units[0].Text, "[Slice]") || !strings.Contains(units[0].Text, "Project=plain\n") {
		t.Errorf("slice without resources: %s\n%s", units[0].Name, units[0].Text)
	}
}

// Specifiers pass through for systemd: a %-led program word is not
// resolved here (verify --user checks it), and environment and listen keep
// theirs.
func TestRender_PassesSpecifiersThrough(t *testing.T) {
	p, err := loadYAML(t, `name: sp
services: {a: {command: "%h/bin/tool --sock %t/x.sock", environment: {RT: "%t/rt", PCT: "5%%"}, listen: "%t/a.sock"}}`)
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	all := units[1].Text + units[2].Text
	for _, want := range []string{"ExecStart=%h/bin/tool --sock %t/x.sock\n", "Environment=RT=%t/rt\n", "Environment=PCT=5%%\n", "ListenStream=%t/a.sock\n"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}

// The yaml's value and an env file's may differ when a later file sets
// the yaml's value again: nothing is lost.
func TestEnvOverride_LaterFileRestoresValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.env")
	later := filepath.Join(dir, "b.env")
	os.WriteFile(later, []byte("A=9\n"), 0o644)
	os.WriteFile(p, []byte("A=1\n"), 0o644)
	s := &config.Service{Environment: []config.KV{{Key: "A", Value: "9"}}, EnvFiles: []config.EnvFile{{Path: p}, {Path: later}}}
	if err := envOverride(s, dir); err != nil {
		t.Errorf("a later file setting the same value: %v", err)
	}
}

// What systemd would misread is refused: a value ending in a backslash
// would take the next directive (Slice=) as its continuation; a directory
// as an env_file would fail only at start. A backslash of its own,
// doubled, passes.
func TestRender_RefusesWhatSystemdWouldMisread(t *testing.T) {
	dir, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	os.Mkdir(filepath.Join(dir, "adir"), 0o755)
	for y, want := range map[string]string{
		`services: {a: {command: "/bin/printf a\\", resources: {memory: 64M}}}`: "ending in a backslash",
		`services: {a: {command: /bin/true, env_file: adir}}`:                   "is a directory",
	} {
		write(y)
		p, err := config.Load(path, config.Options{})
		if err == nil {
			_, err = Render(p, RenderOptions{Exe: "/x"})
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", y, err, want)
		}
	}
	write(`services: {a: {command: "/bin/printf a\\\\"}}`)
	if p, err := config.Load(path, config.Options{}); err != nil {
		t.Error(err)
	} else if _, err := Render(p, RenderOptions{Exe: "/x"}); err != nil {
		t.Errorf("a backslash of its own (doubled): %v", err)
	}
}
