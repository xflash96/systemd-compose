package project

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSelfCmd_RepeatsTheProjectFlags(t *testing.T) {
	for _, c := range []struct {
		f    Flags
		want string
	}{
		{Flags{}, "systemd-compose"},
		{Flags{Name: "beta"}, "systemd-compose -p beta"},
		{Flags{File: "/a b/y.yaml", Name: "n", Profiles: []string{"debug"}}, "systemd-compose -f '/a b/y.yaml' -p n --profile debug"},
	} {
		if got := selfCmd(c.f, "/a b/y.yaml"); got != c.want {
			t.Errorf("selfCmd(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// A printed command reaches a dashed project's units: sh reads each name
// back as written, \x2d and all.
func TestUnitWords_ShReadsThemBack(t *testing.T) {
	names := []string{`my\x2dapp-job.service`, "plain-web.service", `my\x2dapp.slice`}
	out, err := exec.Command("sh", "-c", "printf '%s\\n' "+unitWords(names...)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(out)); strings.Join(got, " ") != strings.Join(names, " ") {
		t.Errorf("sh read %q, want %q", got, names)
	}
}
