package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

// Two services may share a listen address when no run renders both.
func TestSharedListen_AllowsAnAddressNoRunRendersTwice(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	y := "services:\n  a: {command: x, profiles: [prod], listen: \"19601\"}\n  b: {command: x, profiles: [dev], listen: \"19601\"}\n"
	if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, Options{Profiles: []string{"prod"}}); err != nil {
		t.Errorf("one profile at a time: %v", err)
	}
	if _, err := Load(path, Options{Profiles: []string{"*"}}); err == nil || !strings.Contains(err.Error(), "listens there too") {
		t.Errorf("both profiles: %v", err)
	}
}

// A listen: IP without a port is named as such, not as a socket file
// without a slash.
func TestListenAddress_NamesAnIPWithoutAPort(t *testing.T) {
	if got := listenAddress("127.0.0.1"); !strings.Contains(got, "an IP address needs a port") {
		t.Errorf("127.0.0.1: %q", got)
	}
}
