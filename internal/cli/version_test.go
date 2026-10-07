package cli

import (
	"testing"
)

// A pseudo-version repeats the commit and time the line already shows, in
// each of Go's three forms, built from a modified tree or not; a release or
// pre-release tag is the version.
func TestTagOf_KeepsOnlyATag(t *testing.T) {
	for stamp, want := range map[string]string{
		"v0.0.0-20261003080410-903d43f11483":              "", // before any tag
		"v0.1.1-0.20261003080410-905e483eeb1b":            "", // a commit after v0.1.0
		"v0.2.0-rc.1.0.20261003080410-905e483eeb1b":       "", // a commit after v0.2.0-rc.1
		"v0.0.0-20261003080410-903d43f11483+dirty":        "",
		"v0.1.1-0.20261003080410-905e483eeb1b+dirty":      "",
		"v0.2.0-rc.1.0.20261003080410-905e483eeb1b+dirty": "",
		"(devel)":      "",
		"":             "",
		"v0.1.0":       "v0.1.0",
		"v0.1.0+dirty": "v0.1.0",
		"v0.2.0-rc.1":  "v0.2.0-rc.1",
	} {
		if got := tagOf(stamp); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", stamp, got, want)
		}
	}
}
