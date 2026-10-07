// Package testenv is what the packages' tests share: a TestMain that keeps
// the caller's environment overrides out and watches memory, and helpers
// for projects in temporary directories.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// overrides are the tool's own environment overrides, as the package's
// TestMain names them.
var overrides []string

// Main sets the overrides to junk for a package's tests, whatever the
// caller's shell holds, so a test that loads a project without
// ClearOverrides fails rather than passing by luck; and it watches the
// test binary's memory.
func Main(m *testing.M, vars ...string) {
	overrides = vars
	for _, v := range vars {
		os.Setenv(v, "ci_junk")
	}
	go memoryWatchdog(512 << 20)
	os.Exit(m.Run())
}

// memoryWatchdog ends the test binary as soon as it holds more than limit
// bytes: a test that makes something grow without bound (a probe filling
// a buffer from a test that never stops writing) then fails in a fraction
// of a second at a known size, instead of growing until the host has no
// memory left. It reads the resident set from /proc, so it sees growth
// the Go heap statistics would show late or not at all.
func memoryWatchdog(limit int64) {
	page := int64(os.Getpagesize())
	for range time.Tick(50 * time.Millisecond) {
		data, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return // no /proc: nothing to watch with
		}
		f := strings.Fields(string(data))
		if len(f) < 2 {
			return
		}
		pages, _ := strconv.ParseInt(f[1], 10, 64)
		if rss := pages * page; rss > limit {
			fmt.Fprintf(os.Stderr, "memoryWatchdog: the test binary holds %d MB, over the %d MB a test may use: a test makes something grow without bound\n", rss>>20, limit>>20)
			os.Exit(3)
		}
	}
}

// ClearOverrides keeps the tool's own environment overrides out of a test.
func ClearOverrides(t *testing.T) {
	t.Helper()
	for _, v := range overrides {
		t.Setenv(v, "")
	}
}

// FakeBin makes a directory of executable stubs for the program lookup to find.
func FakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ProjectFile is a project file called name in a new temporary directory,
// with the overrides cleared, and a function that writes it as given.
func ProjectFile(t *testing.T, name string) (dir, path string, write func(string)) {
	t.Helper()
	ClearOverrides(t)
	dir = t.TempDir()
	path = filepath.Join(dir, name)
	write = func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, path, write
}
