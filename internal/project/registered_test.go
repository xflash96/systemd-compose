package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// The provenance gate: only a link to this project's own render file is ours.
func TestRegistrationOf_OnlyOwnLinksAreOurs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	unitDir := filepath.Join(home, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(t.TempDir(), "demo")
	render := filepath.Join(proj, ".systemd-compose")
	otherRender := filepath.Join(t.TempDir(), "demo", ".systemd-compose")
	for _, d := range []string{render, otherRender} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pr := &project{p: &config.Project{Name: "demo"}, m: &systemd.Manager{User: true, Dir: unitDir}, renderDir: render} // never the host's manager

	os.WriteFile(filepath.Join(render, "demo-a.service"), []byte("[Service]\n"), 0o644)
	os.Symlink(filepath.Join(render, "demo-a.service"), filepath.Join(unitDir, "demo-a.service"))
	os.WriteFile(filepath.Join(otherRender, "demo-b.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig=/elsewhere/systemd-compose.yaml\n"), 0o644)
	os.Symlink(filepath.Join(otherRender, "demo-b.service"), filepath.Join(unitDir, "demo-b.service"))
	os.WriteFile(filepath.Join(unitDir, "demo-c.service"), []byte("[Service]\n"), 0o644)
	os.Symlink("/nowhere/demo-d.service", filepath.Join(unitDir, "demo-d.service"))

	cases := map[string]string{"demo-a.service": "ours", "demo-b.service": "project", "demo-c.service": "foreign", "demo-d.service": "foreign", "demo-e.service": "none"}
	for name, want := range cases {
		if got := pr.registrationOf(unitDir, name); got.kind != want {
			t.Errorf("%s: kind %q, want %q (%s)", name, got.kind, want, got.owner)
		}
	}
	if r := pr.registrationOf(unitDir, "demo-b.service"); r.owner != "project demo from /elsewhere/systemd-compose.yaml" {
		t.Errorf("other project's owner = %q", r.owner)
	}
	// A mask is named as one, with the way to unmask it, not as a foreign
	// link to rename the project around.
	os.Symlink("/dev/null", filepath.Join(unitDir, "demo-m.service"))
	if r := pr.registrationOf(unitDir, "demo-m.service"); r.kind != "foreign" || !strings.HasPrefix(r.owner, "masked (systemctl --user unmask demo-m.service") {
		t.Errorf("a masked unit: %q %q", r.kind, r.owner)
	}
	ours, others, err := pr.ownedUnits([]string{"demo-a.service", "demo-b.service", "demo-c.service", "demo-e.service"})
	if err != nil || len(ours) != 1 || ours[0] != "demo-a.service" || len(others) != 3 {
		t.Errorf("ownedUnits = %v, %v, %v", ours, others, err)
	}
}
