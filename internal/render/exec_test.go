package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// A $ in a resolved program's path stays single: systemd collapses $$ in
// arguments only, so a doubled one would name no file.
func TestExecLine_KeepsDollarInProgramPath(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := filepath.Join(t.TempDir(), "d$x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755)
	path := filepath.Join(dir, config.ConfigFileName)
	os.WriteFile(path, []byte("name: dl\nservices:\n  s: {command: \"./tool $$HOME\"}\n  l: {command: [./tool, $$HOME]}\n"), 0o644)
	p, err := config.Load(path, config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, u := range units {
		all += u.Text
		if (u.Name == "dl-s.service" || u.Name == "dl-l.service") && !strings.Contains(u.Text, "ExecStart="+dir+"/tool $$HOME\n") {
			t.Errorf("%s: want ExecStart=%s/tool $$HOME in\n%s", u.Name, dir, u.Text) // string form, list form
		}
	}
	if strings.Contains(all, "d$$x") {
		t.Errorf("the program's path doubled:\n%s", all)
	}
}

// quoteWord spells a word so a unit file reads it back as written.
func TestQuoteWord_QuotesForSystemd(t *testing.T) {
	cases := map[string]string{
		"plain":      "plain",
		"KEY=value":  "KEY=value",
		"has space":  `"has space"`,
		`say "hi"`:   `"say \"hi\""`,
		`back\slash`: `"back\\slash"`,
		"semi;colon": `"semi;colon"`,
		"":           `""`,
	}
	for in, want := range cases {
		if got := quoteWord(in); got != want {
			t.Errorf("quoteWord(%q) = %s, want %s", in, got, want)
		}
	}
}

// compose's two spellings: a list is quoted word by word, entrypoint comes
// first, and only the program word must look like a program.
func TestExecLine_QuotesListsAndPutsEntrypointFirst(t *testing.T) {
	bin := testenv.FakeBin(t, "node")
	spaced := filepath.Join(t.TempDir(), "my bin")
	os.MkdirAll(spaced, 0o755)
	os.WriteFile(filepath.Join(spaced, "tool"), []byte("#!/bin/sh\n"), 0o755)
	cases := map[string]string{
		`{command: [node, "a b", 'say "hi"', "$$HOME", "%t/s", ""]}`:            bin + `/node "a b" "say \"hi\"" $$HOME %t/s ""`,
		`{entrypoint: "node --inspect", command: [server.mjs, --port, "8080"]}`: bin + "/node --inspect server.mjs --port 8080",
		`{entrypoint: [node], command: "-e 'x'"}`:                               bin + "/node -e 'x'",
		`{entrypoint: [node, server.mjs]}`:                                      bin + "/node server.mjs",
		`{command: ["` + filepath.Join(spaced, "tool") + `", x]}`:               `"` + filepath.Join(spaced, "tool") + `" x`,
	}
	for svc, want := range cases {
		p, err := loadYAML(t, "name: cf\nservices: {a: "+svc+"}")
		if err != nil {
			t.Errorf("%s: %v", svc, err)
			continue
		}
		units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
		if err != nil {
			t.Errorf("%s: %v", svc, err)
			continue
		}
		if !strings.Contains(units[1].Text, "ExecStart="+want+"\n") {
			t.Errorf("%s: want ExecStart=%s in\n%s", svc, want, units[1].Text)
		}
	}
}
