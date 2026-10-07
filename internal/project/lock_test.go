package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The lock's holder is read as it wrote itself, or named plainly when the
// file is empty or holds the line of a holder that is gone.
func TestReadHolder_NamesOnlyALiveHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	os.WriteFile(p, []byte(fmt.Sprintf("systemd-compose up (pid %d)\n", os.Getpid())), 0o600)
	if got := readHolder(p); got != fmt.Sprintf("systemd-compose up (pid %d)", os.Getpid()) {
		t.Errorf("a live holder: %q", got)
	}
	c := exec.Command("true")
	c.Run()
	os.WriteFile(p, []byte(fmt.Sprintf("systemd-compose down (pid %d)\n", c.Process.Pid)), 0o600)
	if got := readHolder(p); got != "another systemd-compose" {
		t.Errorf("a holder gone: %q", got)
	}
	os.WriteFile(p, nil, 0o600)
	if got := readHolder(p); got != "another systemd-compose" {
		t.Errorf("an empty file: %q", got)
	}
}
