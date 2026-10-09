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

// mountedLate are the filesystems that may be missing when the user
// manager starts and loads its units: network ones, FUSE (sshfs, rclone),
// an eCryptfs directory unlocked at login, and an autofs mount point.
var mountedLate = map[int64]string{
	0x6969: "NFS", 0x517b: "SMB", 0xff534d42: "CIFS", 0xfe534d42: "SMB2",
	0x65735546: "FUSE", 0x01021997: "9p", 0x00c36400: "Ceph", 0x5346414f: "AFS",
	0x73757245: "Coda", 0xf15f: "eCryptfs", 0x0187: "autofs",
}

// LateFS names the filesystem dir is on when it is one of mountedLate and
// not the filesystem of unitDir: units linked into dir are then missing
// whenever the user manager starts before it is mounted. On the unit
// directory's own filesystem (an NFS home) the manager waits for it
// anyway. "" otherwise.
func LateFS(dir, unitDir string) string {
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		return ""
	}
	kind := mountedLate[int64(fs.Type)]
	if kind == "" {
		return ""
	}
	var a, b syscall.Stat_t
	if syscall.Stat(dir, &a) == nil && syscall.Stat(unitDir, &b) == nil && a.Dev == b.Dev {
		return ""
	}
	return kind
}
