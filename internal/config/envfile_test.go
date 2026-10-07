package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An env file is read as systemd reads it (load_env_file), and the last
// file setting a key is the one compared with environment:.
func TestReadEnvFile_ReadsAsSystemdDoes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.env")
	text := "# c\n  ; c\nB = two  \nC='$X lit'\nD=\"q\\\"x\"\nE=$HOME/x\nF=a\\\nb\nG=\"q\" tail\nH=1 # c\nI=x#y\nJ=\r\nexport K=1\nnoequals\n"
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	vars, dropped, differs, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// each as systemd 255 delivers it through systemd-run -p EnvironmentFile=
	want := map[string]string{"B": "two", "C": "$X lit", "D": `q"x`, "E": "$HOME/x", "F": "ab", "G": "qtail", "H": "1 # c", "I": "x#y", "J": ""}
	for k, v := range want {
		if got, ok := vars[k]; !ok || got != v {
			t.Errorf("%s = %q (set %v), want %q", k, got, ok, v)
		}
	}
	if len(dropped) != 2 || !strings.Contains(dropped[0], `"export K"`) || !strings.Contains(dropped[1], `"noequals" has no =`) {
		t.Errorf("dropped = %q", dropped)
	}
	if len(differs) != 2 || !strings.Contains(differs[0], ": G") || !strings.Contains(differs[1], ": H") {
		t.Errorf("differs = %q", differs)
	}
}

// What systemd reads differently from what was meant: a value that is only
// a comment, a quote never closed, a comment ending in a backslash.
func TestReadEnvFile_ReportsWhatSystemdReadsDifferently(t *testing.T) {
	dir := t.TempDir()
	for text, want := range map[string]string{
		"A= # c\n":             "differs line 1: A",
		"A=#tag\n":             "",
		"A=\"open\nB=2\n":      `dropped line 1: A: its quote is never closed`,
		"# note \\\nB=2\n":     "dropped line 1: a comment ending in a backslash",
		"CERT=\"a\nb\"\nC=3\n": "",
	} {
		p := filepath.Join(dir, "x.env")
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, dropped, differs, err := ReadEnvFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if len(dropped) > 0 {
			got = "dropped " + dropped[0]
		} else if len(differs) > 0 {
			got = "differs " + differs[0]
		}
		if want == "" && got != "" || want != "" && !strings.HasPrefix(got, want) {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
