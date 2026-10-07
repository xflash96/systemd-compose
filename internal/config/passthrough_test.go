package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

// The raw form: unit: Service: ExecStart: stands in for command:.
func TestParseService_TakesARawExecStartForCommand(t *testing.T) {
	p, err := loadYAML(t, `
services:
  a:
    unit: {Service: {ExecStart: /bin/sh -c "echo $HOME"}}
    healthcheck: {test: [x], interval: 1h30min}
`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Services[0].Command.Empty() || len(p.Services[0].Unit.Values("Service", "ExecStart")) == 0 {
		t.Errorf("raw ExecStart not carried: %+v", p.Services[0])
	}
	if p.Services[0].Healthcheck.Interval != "1h30min" {
		t.Errorf("compact time span refused: %+v", p.Services[0].Healthcheck)
	}
}

// IOSchedulingClass= is ioprio(2), which needs no cgroup controller: it
// passes, though its name starts like the io controller's keys.
func TestParsePassThrough_AllowsIOScheduling(t *testing.T) {
	if _, err := loadYAML(t, "services: {a: {command: x, unit: {Service: {IOSchedulingClass: idle, IOSchedulingPriority: 7}}}}"); err != nil {
		t.Errorf("ioprio: %v", err)
	}
}

// A unit: EnvironmentFile= would beat environment: unchecked, never be
// hashed for a restart, and never reach build or run.
func TestParsePassThrough_RefusesEnvironmentFile(t *testing.T) {
	if _, err := loadYAML(t, "services: {a: {command: x, unit: {Service: {EnvironmentFile: /etc/x.env}}}}"); err == nil || !strings.Contains(err.Error(), "use env_file:") {
		t.Errorf("EnvironmentFile pass-through: %v", err)
	}
}

// An alias under unit: is taken as written, as unit: is, alone or in a
// list, though environment: interpolates the anchor it shares.
func TestParsePassThrough_TakesAliasesAsWritten(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("HOME=/h\nUSER=u\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("x-w: &w \"/bin/echo ${USER}\"\nservices:\n  a:\n    command: /bin/true\n    environment: {H: &h \"${HOME}/x\"}\n    unit:\n      Service:\n        ExecStartPre: [/bin/echo, *h]\n        ExecStartPost: *w\n"), 0o644)
	p, err := Load(filepath.Join(dir, ConfigFileName), Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := p.Service("a")
	if !a.Unit.Has("Service", "ExecStartPre", "${HOME}/x") || !a.Unit.Has("Service", "ExecStartPost", "/bin/echo ${USER}") {
		t.Errorf("unit: %+v", a.Unit)
	}
	if a.Environment[0].Value != "/h/x" {
		t.Errorf("environment: H = %q, want the interpolated /h/x", a.Environment[0].Value)
	}
}

// An anchor defined inside unit: and aliased from environment: is
// interpolated where the alias is, and left as written under unit:.
func TestParsePassThrough_KeepsAnAnchorInUnitAsWritten(t *testing.T) {
	testenv.ClearOverrides(t)
	for _, y := range []string{
		"services:\n  a:\n    command: /bin/true\n    unit: {Unit: {Description: &d \"v ${V}\"}}\n    environment: {D: *d}\n",
		// the unit: merged in from an x- block
		"x-base: &base\n  unit: {Unit: {Description: &d \"v ${V}\"}}\nservices:\n  a:\n    <<: *base\n    command: /bin/true\n    environment: {D: *d}\n",
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".env"), []byte("V=1\n"), 0o644)
		os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(y), 0o644)
		p, err := Load(filepath.Join(dir, ConfigFileName), Options{})
		if err != nil {
			t.Fatal(err)
		}
		a := p.Service("a")
		if !a.Unit.Has("Unit", "Description", "v ${V}") {
			t.Errorf("%s: unit: %+v, want Description as written", y, a.Unit)
		}
		if a.Environment[0].Value != "v 1" {
			t.Errorf("%s: environment: D = %q, want the interpolated v 1", y, a.Environment[0].Value)
		}
	}
}
