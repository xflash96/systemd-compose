package project

import (
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
