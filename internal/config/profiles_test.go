package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

// A mistyped --profile is its own error: no verb takes it for a yaml that
// fails to load and acts on what is registered.
func TestApplyProfiles_RefusesAnUnknownProfileAsATypo(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "systemd-compose.yaml")
	os.WriteFile(path, []byte("services:\n  a: {command: /bin/true, profiles: [debug]}\n"), 0o644)
	_, err := Load(path, Options{Profiles: []string{"debgu"}})
	var typo UnknownProfile
	if !errors.As(err, &typo) {
		t.Errorf("--profile debgu: %v, want an UnknownProfile", err)
	}
	if _, err := Load(path, Options{Profiles: []string{"debug"}}); err != nil {
		t.Errorf("--profile debug: %v", err)
	}
}

// A required depends_on into a profile that is off loads, with the reason
// kept for the verbs that start or render; ps, logs, stop and down work.
func TestApplyProfiles_LoadsARequiredEdgeIntoAnInactiveProfile(t *testing.T) {
	p, err := loadYAML(t, "services: {a: {command: /bin/true, depends_on: {b: {required: true}}}, b: {command: /bin/true, profiles: [data]}}")
	if err != nil {
		t.Fatalf("it loads: %v", err)
	}
	if !strings.Contains(p.NeedsProfile, "add --profile data") {
		t.Errorf("NeedsProfile = %q", p.NeedsProfile)
	}
	if len(p.Service("a").DependsOn) != 0 {
		t.Errorf("the edge to a service that is off stays: %v", p.Service("a").DependsOn)
	}
}

// Profiles set in the environment for every project load a file that lacks
// them; --profile names this project's, so a missing one is a typo.
func TestLoad_ProfilesFromTheEnvironmentNeedNotExist(t *testing.T) {
	p, err := loadYAML(t, "services: {a: {command: x}}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ProfilesVar, "debug,other")
	if _, err := Load(p.ConfigPath, Options{}); err != nil {
		t.Errorf("profiles from the environment this file lacks: %v", err)
	}
	if _, err := Load(p.ConfigPath, Options{Profiles: []string{"debug"}}); err == nil {
		t.Errorf("--profile debug, which the file lacks, is a typo to refuse")
	}
}
