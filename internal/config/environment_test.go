package config

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A required env_file must be a regular file that is there; an optional
// one is left to systemd.
func TestCheckEnvFiles_RefusesWhatSystemdWouldFailOn(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	for _, c := range []struct {
		file EnvFile
		want string // "" for none
	}{
		{EnvFile{Path: missing, Required: true}, "mark it {path, required: false} if it may be absent"},
		{EnvFile{Path: dir, Required: true}, "is a directory, not a file"},
		{EnvFile{Path: fifo, Required: true}, "is not a regular file"},
		{EnvFile{Path: missing}, ""},
	} {
		err := CheckEnvFiles([]EnvFile{c.file})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%+v: %v, want none", c.file, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%+v: %v, want one that says %q", c.file, err, c.want)
		}
	}
}
