package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// Profiles: the enabled set is what up renders and the target wants; the
// declared set, every profile, is what the project owns.
func TestProfiles_ChooseEnabledUnits(t *testing.T) {
	dir, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	write(`name: pf
services:
  web: {command: x, depends_on: [debug]}
  debug: {command: x, profiles: [debug]}
  job: {command: x, schedule: hourly, profiles: [debug, nightly]}
`)
	load := func(o config.Options) *config.Project {
		t.Helper()
		p, err := config.Load(path, o)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := load(config.Options{})
	if got := strings.Join(p.UnitNames(), ","); got != "pf.slice,pf-web.service,pf.target" {
		t.Errorf("no profile: UnitNames = %s", got)
	}
	if got := strings.Join(p.DeclaredNames(), ","); got != "pf.slice,pf-web.service,pf-debug.service,pf-job.service,pf-job.timer,pf.target" {
		t.Errorf("DeclaredNames = %s", got)
	}
	if len(p.Service("web").DependsOn) != 0 {
		t.Errorf("an optional edge into an inactive profile must be dropped: %+v", p.Service("web").DependsOn)
	}
	if p = load(config.Options{Profiles: []string{"nightly"}}); len(p.UnitNames()) != 5 || p.ProfilesFrom != "--profile" {
		t.Errorf("nightly: %v from %s", p.UnitNames(), p.ProfilesFrom)
	}
	if p = load(config.Options{Profiles: []string{"*"}}); len(p.UnitNames()) != 6 || len(p.Service("web").DependsOn) != 1 {
		t.Errorf("*: %v, web edges %v", p.UnitNames(), p.Service("web").DependsOn)
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte(config.ProfilesVar+"=nightly\n"), 0o644)
	if p = load(config.Options{}); p.ProfilesFrom != ".env" || !p.Enabled(p.Service("job")) || p.Enabled(p.Service("debug")) {
		t.Errorf(".env: from %s, %v", p.ProfilesFrom, p.Profiles)
	}
	t.Setenv(config.ProfilesVar, "debug, nightly")
	if p = load(config.Options{}); p.ProfilesFrom != "environment" || len(p.Profiles) != 2 {
		t.Errorf("environment: from %s, %v", p.ProfilesFrom, p.Profiles)
	}
	if p = load(config.Options{Profiles: []string{"nightly"}}); p.ProfilesFrom != "--profile" || p.Enabled(p.Service("debug")) {
		t.Errorf("the flag must replace the variable: %v from %s", p.Profiles, p.ProfilesFrom)
	}
	if _, err := config.Load(path, config.Options{Profiles: []string{"debgu"}}); err == nil || !strings.Contains(err.Error(), `profile "debgu" (from --profile) is no service's`) {
		t.Errorf("a typo in a profile must be loud: %v", err)
	}
	units, err := Render(load(config.Options{Profiles: []string{"nightly"}}), RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "x")}})
	if err != nil {
		t.Fatal(err)
	}
	target := units[len(units)-1].Text
	if !strings.Contains(target, "Wants=pf-job.timer") || strings.Contains(target, "pf-debug") {
		t.Errorf("the target is the enabled set:\n%s", target)
	}
}

// compose's reuse: x- blocks are ignored, merge keys apply (explicit keys
// win, the earlier of two sources wins), interpolation reaches a merged
// value, and a merged unit: stays raw systemd.
func TestLoad_ExtensionsAndMergesAsCompose(t *testing.T) {
	dir, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PORT=8080\n"), 0o644)
	write(`x-common: &common
  restart: always
  environment: {PORT: "${PORT}"}
  unit:
    Service:
      ExecStartPre: /bin/sh -c "echo $HOME"
x-quiet: &quiet
  restart: "no"
  on_change: start-only
services:
  a:
    <<: *common
    command: node a
    x-note: anything
  b:
    <<: [*quiet, *common]
    command: node b
  c:
    <<: *common
    restart: on-failure
    command: node c
`)
	p, err := config.Load(path, config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := p.Service("a"), p.Service("b"), p.Service("c")
	if a.Restart.Policy != "always" || len(a.Environment) != 1 || a.Environment[0].Value != "8080" {
		t.Errorf("a: %+v %+v", a.Restart, a.Environment)
	}
	if b.Restart.Policy != "no" || b.OnChange != "start-only" || len(b.Environment) != 1 {
		t.Errorf("b: the earlier source must win: %+v %s %+v", b.Restart, b.OnChange, b.Environment)
	}
	if c.Restart.Policy != "on-failure" {
		t.Errorf("c: the explicit key must win: %+v", c.Restart)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "node")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(units[1].Text, `ExecStartPre=/bin/sh -c "echo $HOME"`) {
		t.Errorf("a merged unit: must stay raw:\n%s", units[1].Text)
	}
	for y, want := range map[string]string{
		"services: {a: {command: x, <<: 5}}":                            "<< merges a map",
		"services: {a: {command: x, <<: [1, 2]}}":                       "<< merges a map",
		"services: {a: {command: x, healthcheck: {test: [x], x-b: 1}}}": `unknown key "x-b"`,
		// What a merged block refuses is reported as itself, not as "<<".
		"x-d: &d\n  restart: always\n  restart: \"no\"\nservices: {a: {<<: *d, command: x}}": `key "restart" already given`,
	} {
		if _, err := loadYAML(t, y); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", y, want, err)
		}
	}
}

// Interpolation through Load: values from .env, keys and unit: untouched,
// a plain scalar retyped, a literal $ written $$ in ExecStart.
func TestInterpolation_FillsValuesFromDotenv(t *testing.T) {
	bin := testenv.FakeBin(t, "node")
	dir, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PORT=8080\nONE=true\nNAME=interp\nGREETING=hello world\n"), 0o644)
	y := `name: ${NAME}
services:
  a:
    command: node server.mjs --port ${PORT} --price $$5
    environment: {GREETING: "${GREETING}", MODE: "${MODE:-prod}"}
    oneshot: ${ONE}
  b:
    unit:
      Service:
        ExecStart: /bin/sh -c "echo $HOME ${PORT}"
`
	write(y)
	p, err := config.Load(path, config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "interp" || !p.Services[0].Oneshot {
		t.Fatalf("name %q oneshot %v", p.Name, p.Services[0].Oneshot)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := units[1].Text, units[2].Text
	for _, want := range []string{"ExecStart=" + bin + "/node server.mjs --port 8080 --price $$5\n", `Environment="GREETING=hello world"`, "Environment=MODE=prod\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in\n%s", want, a)
		}
	}
	if !strings.Contains(b, `ExecStart=/bin/sh -c "echo $HOME ${PORT}"`) {
		t.Errorf("unit: was interpolated:\n%s", b)
	}
}
