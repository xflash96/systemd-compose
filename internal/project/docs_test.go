package project

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The man page and the dependencies guide say how long a program must run
// to count as started, and how long up watches a restart; the constants
// are what acts.
func TestDocs_StateTheWatchTimes(t *testing.T) {
	steady := fmt.Sprintf("has run %d seconds", int(steadyAfter.Seconds()))
	for doc, says := range map[string][]string{
		"../../docs/systemd-compose.1": {steady, fmt.Sprintf("up to %d seconds", int(defaultWait.Seconds())), "instead of " + defaultWait.String()},
		"../../docs/dependencies.md":   {steady},
	} {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, s := range says {
			if !strings.Contains(text, s) {
				t.Errorf("%s does not say %q", doc, s)
			}
		}
	}
}
