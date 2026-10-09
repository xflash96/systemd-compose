package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// help man says every word of the manual that groff prints, in order:
// only the line breaks and the spacing may differ.
func TestRenderMan_SaysWhatGroffSays(t *testing.T) {
	if _, err := exec.LookPath("groff"); err != nil {
		t.Skip("no groff here")
	}
	out, err := exec.Command("groff", "-man", "-Tutf8", "-P-cbou", "-rLL=80n", "../../docs/systemd-compose.1").Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	theirs := strings.Join(strings.Fields(strings.Join(lines[1:len(lines)-1], "\n")), "") // no header, no footer
	ours := strings.Join(strings.Fields(renderMan(docs.Man)), "")
	if ours != theirs {
		i := 0
		for i < len(ours) && i < len(theirs) && ours[i] == theirs[i] {
			i++
		}
		t.Errorf("help man departs from groff at byte %d:\nours:  %.80s\ngroff: %.80s", i, ours[i:], theirs[i:])
	}
}

// help VERB finds a verb in a tag that names several, and not in a
// stand-in's tag.
func TestTagNames(t *testing.T) {
	for _, c := range []struct {
		tag, verb string
		want      bool
	}{
		{`.BR start | stop | restart " \fR[\fISERVICE\fR...]"`, "stop", true},
		{`.BR version ", " \-\-version`, "version", true},
		{`.B up \fIUNIT\fR...`, "up", true},
		{`.B up \fIUNIT\fR...`, "down", false},
		{`.I VERB \fR[\fISERVICE\fR...]`, "VERB", false},
	} {
		if got := tagNames(c.tag, c.verb); got != c.want {
			t.Errorf("tagNames(%q, %q) = %v", c.tag, c.verb, got)
		}
	}
}
