package systemd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mtime is when p was last written; zero when it cannot be read. WriteUnit
// rewrites only on change, so for a render file it is the definition's birth.
func mtime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// WriteUnit's contract with up's plan: identical text is never rewritten, so
// the mtime stays the birth of the definition; changed text is.
func TestWriteUnit_KeepsMtimeOfSameText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.service")
	if err := WriteUnit(p, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	born := time.Unix(1700000000, 0)
	if err := os.Chtimes(p, born, born); err != nil {
		t.Fatal(err)
	}
	if err := WriteUnit(p, "[Service]\n"); err != nil || !mtime(p).Equal(born) {
		t.Errorf("same text rewrote the file: mtime %v, err %v", mtime(p), err)
	}
	if err := WriteUnit(p, "[Service]\nNice=5\n"); err != nil || !mtime(p).After(born) {
		t.Errorf("changed text not written: mtime %v, err %v", mtime(p), err)
	}
}
