package project

import (
	"strings"
	"testing"
)

// A flag's value names no unit: in status -n 5, the 5 is no service.
func TestNamesAUnit_SkipsFlagValues(t *testing.T) {
	for args, want := range map[string]bool{
		"-n 5": false, "-n 5 web": true, "web": true, "--lines 3 -o cat": false,
		"--no-pager": false, "-- web": true, "--": false, "-p MainPID": false,
	} {
		if got := namesAUnit(strings.Fields(args)); got != want {
			t.Errorf("namesAUnit(%q) = %v, want %v", args, got, want)
		}
	}
}
