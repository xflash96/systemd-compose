package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lock keeps up, down, build, start and restart of one project from
// running at once: a down during up's build would see the build end
// looking finished and the up re-register what the down took off. A second
// one waits, and says for what.
func (pr *project) lock(verb string) (func(), error) {
	if _, err := scratchDir(); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	f, err := os.OpenFile(pr.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Printf("waiting for %s to finish with project %s first\n", readHolder(f.Name()), pr.p.Name)
		// A build it runs is why the wait can be long; say how to end it.
		if offs, err := pr.oneOffs(); err == nil {
			for _, u := range offs {
				if build := pr.oneOffPrefix("build"); strings.HasPrefix(u, build) {
					fmt.Printf("  it is running %s; to stop that build instead: ^C that command, or systemctl --user stop '%s*'\n", u, build)
				}
			}
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, fmt.Errorf("lock: %w", err)
		}
	}
	// over the last holder's line, then trimmed: a truncate first would
	// leave the file empty for a reader in between
	line := fmt.Sprintf("systemd-compose %s (pid %d)\n", verb, os.Getpid())
	f.WriteAt([]byte(line), 0)
	f.Truncate(int64(len(line)))
	return func() { f.Close() }, nil // closing releases the lock
}

// readHolder is the lock's holder, as it wrote itself. A line read while it
// is being written, or the line of a holder that is gone, is read again for
// a moment, then named plainly as another systemd-compose.
func readHolder(path string) string {
	for range 20 {
		b, _ := os.ReadFile(path)
		line := strings.TrimSpace(string(b))
		if _, pid, ok := strings.Cut(line, "(pid "); ok {
			if n, err := strconv.Atoi(strings.TrimSuffix(pid, ")")); err == nil && syscall.Kill(n, 0) == nil {
				return line
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "another systemd-compose"
}

// lockPath is the project's lock file.
func (pr *project) lockPath() string { return filepath.Join(scratchPath(), pr.p.Name+".lock") }

// lockHolder names the up, down or build working on this project, or "".
func (pr *project) lockHolder() string {
	f, err := os.Open(pr.lockPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		return "" // nobody holds it
	}
	return readHolder(f.Name())
}
