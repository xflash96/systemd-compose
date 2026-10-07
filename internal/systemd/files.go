package systemd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// WriteUnit is compare-then-swap, and never rewrites identical text: up's
// plan reads the file's mtime as the birth of the definition ("changed
// (not applied)"), so a rewrite would restart every running service on
// every up (TestWriteUnit_KeepsMtimeOfSameText).
//
// The file is the tool's own, a regular file only this user reads (it may
// hold environment: values). A symlink in its place, or at a fixed temp
// name, would be followed, and a file outside the project overwritten.
func WriteUnit(path, text string) error {
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (a symlink?); the tool writes its own files there, so move it aside", path)
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil && string(old) == text:
		return os.Chmod(path, 0o600) // an older version's render is readable by all
	case err != nil && !os.IsNotExist(err):
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*") // O_EXCL, a name no one chose
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// PrivateDir makes dir, or takes it as it is, only if it is a directory of
// this user's that no one else can write: files planted there would be
// followed or read as the tool's own. It is then made 0700.
func PrivateDir(dir string) error {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := CheckPrivateDir(dir); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// CheckPrivateDir is PrivateDir's test alone, for a directory that may not
// be there yet (up --dry-run, and up before its plan reads the files).
func CheckPrivateDir(dir string) error {
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sys := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.IsDir():
		return fmt.Errorf("%s is not a directory (a symlink?); the tool keeps its own files there, so move it aside", dir)
	case int(sys.Uid) != os.Getuid():
		// moving it aside would break that user's running project
		owner := strconv.Itoa(int(sys.Uid))
		if u, err := user.LookupId(owner); err == nil {
			owner = u.Username
		}
		who := "them (or root)"
		if sys.Uid == 0 {
			who = "root" // not "them (or root)", which names root twice
		}
		return fmt.Errorf("%s belongs to %s, whose units may run from it; work from a checkout of your own (another clone, or a git worktree), or, if nothing of theirs runs from it, have %s remove it", dir, owner, who)
	case st.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s can be written by others (mode %o), who could plant files the tool would read as its own; chmod go-w it, or move it aside", dir, st.Mode().Perm())
	}
	return nil
}

// SweepEmptyWants removes .wants/.requires directories the disables left
// empty, so the unit directory does not accumulate husks.
func SweepEmptyWants(unitDir string) {
	entries, err := os.ReadDir(unitDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !(strings.HasSuffix(e.Name(), ".wants") || strings.HasSuffix(e.Name(), ".requires")) {
			continue
		}
		p := filepath.Join(unitDir, e.Name())
		if inner, err := os.ReadDir(p); err == nil && len(inner) == 0 {
			os.Remove(p)
		}
	}
}
