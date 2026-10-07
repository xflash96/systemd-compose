package config

import (
	"path/filepath"
	"testing"
)

// compose's restart: unless-stopped is systemd's always.
func TestParseRestart_TakesUnlessStoppedAsAlways(t *testing.T) {
	p, err := loadYAML(t, "services: {a: {command: x, restart: unless-stopped}}")
	if err != nil || p.Services[0].Restart.Policy != "always" {
		t.Fatalf("%v %+v", err, p)
	}
}

// up looks for a build's creates: where the build ran, the service's
// working directory, not the project's; an absolute path stands.
func TestParseBuild_ResolvesCreatesInTheWorkingDir(t *testing.T) {
	p, err := loadYAML(t, `
services:
  rel: {command: x, working_dir: app, build: {run: [x], creates: dist}}
  abs: {command: x, build: {run: [x], creates: /opt/out}}
  none: {command: x, build: {run: [x]}}
  plain: {command: x}
`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"rel":   filepath.Join(p.Dir, "app", "dist"),
		"abs":   "/opt/out",
		"none":  "",
		"plain": "",
	} {
		s, err := p.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if s.Build != nil {
			got = s.Build.Creates
		}
		if got != want {
			t.Errorf("%s: creates = %q, want %q", name, got, want)
		}
	}
}

// The parser reads every key the spec gives a service: each, given a value
// that is wrong for it, is refused rather than ignored.
func TestParseService_ReadsEveryKeyOfTheSpec(t *testing.T) {
	for _, k := range ServiceKeys {
		wrong := "{zz_unknown: 1}"
		if k == "environment" { // a map of any names is right for it
			wrong = "[nope]"
		}
		y := "services: {a: {command: /bin/true, " + k + ": " + wrong + "}}"
		if k == "command" {
			y = "services: {a: {command: " + wrong + "}}"
		}
		if _, err := loadYAML(t, y); err == nil {
			t.Errorf("%s: %s was taken", k, wrong)
		}
	}
}
