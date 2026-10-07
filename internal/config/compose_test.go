package config

import (
	"strings"
	"testing"
)

// compose's ulimits: -1 is advised as systemd's infinity, since systemd
// refuses LimitNOFILE: -1.
func TestUlimits_AdvisesSystemdLimitValues(t *testing.T) {
	if _, err := loadYAML(t, "services: {a: {command: x, ulimits: {nofile: -1, nproc: {soft: -1, hard: 4096}}}}"); err == nil || !strings.Contains(err.Error(), "LimitNOFILE: infinity") || !strings.Contains(err.Error(), "LimitNPROC: infinity:4096") {
		t.Errorf("ulimits advice: %v", err)
	}
}
