package systemd

import (
	"strings"
	"testing"
)

// A unit name as a D-Bus object path label (systemd's bus_label_escape).
func TestBusEscape_AsSystemdLabels(t *testing.T) {
	cases := map[string]string{"my_app-web.service": "my_5fapp_2dweb_2eservice", "9p-a.service": "_39p_2da_2eservice", "a9": "a9"}
	for in, want := range cases {
		if got := busEscape(in); got != want {
			t.Errorf("busEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// systemd's advice to daemon-reload is dropped, whatever the write sizes;
// every other line passes.
func TestStaleFilter_DropsOnlyTheReloadAdvice(t *testing.T) {
	var out strings.Builder
	f := &staleFilter{w: &out}
	in := "Created symlink a -> b.\nWarning: The unit file, source configuration file or drop-ins of x.service changed on disk. Run 'systemctl --user daemon-reload' to reload units.\nFailed to start x.service: Unit x.service not found.\nno newline"
	for i := 0; i < len(in); i += 7 {
		f.Write([]byte(in[i:min(i+7, len(in))]))
	}
	f.Flush()
	if want := "Created symlink a -> b.\nFailed to start x.service: Unit x.service not found.\nno newline"; out.String() != want {
		t.Errorf("filtered:\n%q\nwant\n%q", out.String(), want)
	}
}

// The unit directory is the manager's, read from its UnitPath, whose first
// entry is its control directory. This process's XDG_CONFIG_HOME or HOME
// may differ, and every unit would then read as unregistered.
func TestConfigDir_ReadsTheManagersUnitPath(t *testing.T) {
	path := "/home/u/.config/systemd/user.control /run/user/1000/systemd/user.control /run/user/1000/systemd/transient /home/u/.config/systemd/user /etc/systemd/user"
	if got := configDir(path); got != "/home/u/.config/systemd/user" {
		t.Errorf("configDir = %q", got)
	}
	if got := configDir("/run/user/1000/systemd/user.control /etc/systemd/user"); got != "" {
		t.Errorf("no persistent control dir: %q", got)
	}
}
