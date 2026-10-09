package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// The provenance gate: ours is a link to this project's own render file, or
// a copy whose marker names this project's yaml; another yaml's link or
// copy is that project's; a file with no marker is foreign.
func TestRegistrationOf_OnlyOwnLinksAndCopiesAreOurs(t *testing.T) {
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
	cfg := filepath.Join(proj, "systemd-compose.yaml")
	pr := &project{p: &config.Project{Name: "demo", Dir: proj, ConfigPath: cfg}, m: &systemd.Manager{User: true, Dir: unitDir}, renderDir: render} // never the host's manager

	os.WriteFile(filepath.Join(render, "demo-a.service"), []byte("[Service]\n"), 0o644)
	os.Symlink(filepath.Join(render, "demo-a.service"), filepath.Join(unitDir, "demo-a.service"))
	os.WriteFile(filepath.Join(otherRender, "demo-b.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig=/elsewhere/systemd-compose.yaml\n"), 0o644)
	os.Symlink(filepath.Join(otherRender, "demo-b.service"), filepath.Join(unitDir, "demo-b.service"))
	os.WriteFile(filepath.Join(unitDir, "demo-c.service"), []byte("[Service]\n"), 0o644)
	os.Symlink("/nowhere/demo-d.service", filepath.Join(unitDir, "demo-d.service"))
	os.WriteFile(filepath.Join(unitDir, "demo-f.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig="+cfg+"\n"), 0o600)
	os.WriteFile(filepath.Join(unitDir, "demo-g.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig=/elsewhere/systemd-compose.yaml\n"), 0o600)
	otherCfg := filepath.Join(filepath.Dir(otherRender), "systemd-compose.yaml")
	os.WriteFile(otherCfg, []byte("services: {}\n"), 0o644)
	os.WriteFile(filepath.Join(unitDir, "demo-h.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig="+otherCfg+"\n"), 0o600)
	os.WriteFile(filepath.Join(unitDir, "demo-r.service"), []byte("[X-SystemdCompose]\nProject=demo\nConfig=rel/systemd-compose.yaml\n"), 0o600)

	cases := map[string]string{"demo-a.service": "ours", "demo-b.service": "project", "demo-c.service": "foreign", "demo-d.service": "foreign", "demo-e.service": "none", "demo-f.service": "ours", "demo-g.service": "project", "demo-h.service": "project", "demo-r.service": "foreign"}
	for name, want := range cases {
		if got := pr.registrationOf(unitDir, name); got.kind != want {
			t.Errorf("%s: kind %q, want %q (%s)", name, got.kind, want, got.owner)
		}
	}
	if r := pr.registrationOf(unitDir, "demo-b.service"); r.owner != "project demo from /elsewhere/systemd-compose.yaml" {
		t.Errorf("other project's owner = %q", r.owner)
	}
	// a copy is gone when the yaml its marker names is, as a link that
	// dangles is: the project was moved, deleted or is not mounted
	if r := pr.registrationOf(unitDir, "demo-g.service"); !r.gone || r.owner != "a project whose files are gone (/elsewhere: moved, deleted or not mounted)" {
		t.Errorf("a copy whose yaml is gone: gone %v, owner %q", r.gone, r.owner)
	}
	// gone, a copy is retired by -p NAME from outside a project; a link,
	// which held no name but its target's, only from where it was
	if r := pr.registrationOf(unitDir, "demo-g.service"); !strings.HasPrefix(r.retire(), "cd / && systemd-compose -p demo down") {
		t.Errorf("a gone copy's way out: %q", r.retire())
	}
	os.Symlink("/gone/demo/.systemd-compose/demo-k.service", filepath.Join(unitDir, "demo-k.service"))
	if r := pr.registrationOf(unitDir, "demo-k.service"); !r.gone || !strings.HasPrefix(r.retire(), "move it back") {
		t.Errorf("a gone link: gone %v, way out %q", r.gone, r.retire())
	}
	if r := pr.registrationOf(unitDir, "demo-h.service"); r.gone || r.owner != "project demo from "+otherCfg+" (a copy)" {
		t.Errorf("another project's copy: gone %v, owner %q", r.gone, r.owner)
	}
	if r := pr.registrationOf(unitDir, "demo-f.service"); !r.copied {
		t.Error("an own copy is not known as a copy")
	}
	if r := pr.registrationOf(unitDir, "demo-a.service"); r.copied {
		t.Error("an own link is taken for a copy")
	}
	// A mask is named as one, with the way to unmask it, not as a foreign
	// link to rename the project around.
	os.Symlink("/dev/null", filepath.Join(unitDir, "demo-m.service"))
	if r := pr.registrationOf(unitDir, "demo-m.service"); r.kind != "foreign" || !strings.HasPrefix(r.owner, "masked (systemctl --user unmask demo-m.service") {
		t.Errorf("a masked unit: %q %q", r.kind, r.owner)
	}
	ours, others, err := pr.ownedUnits([]string{"demo-a.service", "demo-b.service", "demo-c.service", "demo-e.service", "demo-f.service", "demo-g.service"})
	if err != nil || len(ours) != 2 || ours[0] != "demo-a.service" || ours[1] != "demo-f.service" || len(others) != 4 {
		t.Errorf("ownedUnits = %v, %v, %v", ours, others, err)
	}
}

// With copies, systemd loads the copy, so up compares with it: an edit of
// the copy by hand is a change up undoes, though the render file matches.
func TestChangeOf_ComparesWithTheCopySystemdLoads(t *testing.T) {
	unitDir := t.TempDir()
	proj := t.TempDir()
	render := filepath.Join(proj, ".systemd-compose")
	if err := os.MkdirAll(render, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(proj, "systemd-compose.yaml")
	pr := &project{p: &config.Project{Name: "demo", Dir: proj, ConfigPath: cfg}, m: &systemd.Manager{User: true, Dir: unitDir}, renderDir: render}
	text := "[Service]\nExecStart=/bin/sleep infinity\n[X-SystemdCompose]\nProject=demo\nConfig=" + cfg + "\n"
	os.WriteFile(filepath.Join(render, "demo-a.service"), []byte(text), 0o644)
	os.WriteFile(filepath.Join(unitDir, "demo-a.service"), []byte(strings.Replace(text, "infinity", "12345", 1)), 0o600)
	if got, err := pr.changeOf("demo-a.service", text, systemd.UnitState{}); err != nil || got != changeText {
		t.Errorf("a copy edited by hand: %q %v, want %q", got, err, changeText)
	}
	os.WriteFile(filepath.Join(unitDir, "demo-a.service"), []byte(text), 0o600)
	os.Remove(filepath.Join(render, "demo-a.service"))
	if got, err := pr.changeOf("demo-a.service", text, systemd.UnitState{}); err != nil || got != changeNone {
		t.Errorf("a copy with its render file gone: %q %v, want %q", got, err, changeNone)
	}
}
