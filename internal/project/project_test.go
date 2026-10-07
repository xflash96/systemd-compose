package project

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

func loadYAML(t *testing.T, y string) (*config.Project, error) {
	t.Helper()
	_, path, write := testenv.ProjectFile(t, config.ConfigFileName)
	write(strings.TrimSpace(y) + "\n")
	return config.Load(path, config.Options{})
}

// Root in a user's project is pointed at the yaml's owner: root's own user
// instance would start a second copy of every service, from the user's
// files.
func TestOtherOwner_NamesTheOwnerToRoot(t *testing.T) {
	f := filepath.Join(t.TempDir(), config.ConfigFileName)
	os.WriteFile(f, []byte("services: {a: {command: x}}\n"), 0o644)
	if os.Getuid() == 0 {
		t.Skip("the file would be root's own")
	}
	if got := otherOwner(f, 0); got == "" {
		t.Errorf("root in a user's project: no owner named")
	}
	if got := otherOwner(f, os.Getuid()); got != "" {
		t.Errorf("the owner in their own project: %q", got)
	}
}

// A hint naming another project name keeps the -f and --profile it was
// reached with, since a -p alone outside the project's directory is
// refused.
func TestCmdAs_KeepsTheOtherFlags(t *testing.T) {
	pr := &project{p: &config.Project{ConfigPath: "/a b/y.yaml"}, sel: Flags{File: "/a b/y.yaml", Name: "x", Profiles: []string{"debug"}}}
	if got := pr.cmdAs("other"); got != "systemd-compose -f '/a b/y.yaml' -p other --profile debug" {
		t.Errorf("cmdAs = %q", got)
	}
}

// An env_file systemd-run fails on without a word is refused for one-offs
// and build steps; a FIFO is refused before any open, which would wait for
// a writer for ever.
func TestCheckEnvFiles_RefusesWhatSystemdRunFailsOn(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.env")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	locked := filepath.Join(dir, "locked.env")
	os.WriteFile(locked, []byte("A=1\n"), 0o000)
	ok := filepath.Join(dir, "ok.env")
	os.WriteFile(ok, []byte("A=1\n"), 0o600)
	done := make(chan error, 1)
	go func() { done <- checkEnvFiles([]config.EnvFile{{Path: fifo, Required: true}}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("a FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO env_file: the check waited for a writer")
	}
	if os.Getuid() != 0 {
		if err := checkEnvFiles([]config.EnvFile{{Path: locked, Required: true}}); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("an unreadable file: %v", err)
		}
	}
	if err := checkEnvFiles([]config.EnvFile{{Path: ok, Required: true}, {Path: dir, Required: false}}); err != nil {
		t.Errorf("a readable file and an optional directory: %v", err)
	}
}
