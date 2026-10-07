package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// A value with whitespace at either end is refused, since systemd strips
// it. On an Exec line the spaces only separate words.
func TestUnitFile_RefusesPaddedValues(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := filepath.Join(t.TempDir(), "d$x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755)
	path := filepath.Join(dir, config.ConfigFileName)
	os.WriteFile(path, []byte("name: dl\nservices:\n  s: {command: ./tool, unit: {Unit: {Description: \"x \"}}}\n"), 0o644)
	if p, err := config.Load(path, config.Options{}); err != nil {
		t.Fatal(err)
	} else if _, err := Render(p, RenderOptions{Exe: "/x"}); err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") {
		t.Errorf("a padded value: %v", err)
	}
	// on an Exec line the spaces only separate words, so a ${VAR:-} that
	// comes out empty leaves one, and passes
	os.WriteFile(path, []byte("name: dl\nservices:\n  s: {command: \"./tool ${NOPE:-}\"}\n"), 0o644)
	if p, err := config.Load(path, config.Options{}); err != nil {
		t.Fatal(err)
	} else if _, err := Render(p, RenderOptions{Exe: "/x"}); err != nil {
		t.Errorf("a command padded by an empty interpolation: %v", err)
	}
	// systemd strips spaces and tabs only: a no-break space is the value's
	os.WriteFile(path, []byte("name: dl\nservices:\n  s: {command: [./tool, \"hi\u00a0\"]}\n"), 0o644)
	if p, err := config.Load(path, config.Options{}); err != nil {
		t.Fatal(err)
	} else if _, err := Render(p, RenderOptions{Exe: "/x"}); err != nil {
		t.Errorf("a trailing no-break space: %v", err)
	}
}
