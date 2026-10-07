package render

import (
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

// The marker's writer and its readers agree: the provenance gate and the
// orphan sweep find Project= and Config= where the renderer puts them.
func TestReadMarker_ReadsWhatMarkerWrites(t *testing.T) {
	p, err := loadYAML(t, "name: mk\nservices: {a: {command: node x}, j: {command: node x, schedule: hourly}}")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{testenv.FakeBin(t, "node")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		want := Marker{Project: "mk", Config: p.ConfigPath}
		switch u.Name {
		case "mk-a.service":
			want.Service = "a"
		case "mk-j.service", "mk-j.timer":
			want.Service = "j"
		}
		if m := ReadMarker(u.Text); m != want {
			t.Errorf("%s: marker reads %+v, want %+v", u.Name, m, want)
		}
	}
}
