package project

import (
	"testing"
)

func TestGoneFiles_SingularForOne(t *testing.T) {
	if a, b := goneFiles(1), goneFiles(3); a != "1 rendered file in .systemd-compose is gone" || b != "3 rendered files in .systemd-compose are gone" {
		t.Errorf("goneFiles: %q, %q", a, b)
	}
}
