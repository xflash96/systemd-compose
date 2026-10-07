package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin makes a directory of executable stubs for resolve() to find.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRenderGolden(t *testing.T) {
	clearOverrides(t)
	bin := fakeBin(t, "node", "curl", "psql", "backup")
	yamlPath, _ := filepath.Abs("testdata/basic/systemd-compose.yaml")
	p, err := Load(yamlPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/opt/systemd-compose", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, u := range units {
		b.WriteString("### " + u.Name + "\n" + u.Text + "\n")
	}
	got := b.String()
	got = strings.ReplaceAll(got, filepath.Dir(yamlPath), "@DIR@")
	got = strings.ReplaceAll(got, bin, "@BIN@")

	golden := "testdata/basic/golden.txt"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(want) != got {
		t.Errorf("render differs from %s\n--- got ---\n%s", golden, got)
	}

	// UnitNames is the declared set orphans() trusts: exactly what Render
	// emits, in order (the golden pins the names themselves).
	var rendered []string
	for _, u := range units {
		rendered = append(rendered, u.Name)
	}
	if names := p.UnitNames(); strings.Join(names, ",") != strings.Join(rendered, ",") {
		t.Errorf("UnitNames = %v, Render emits %v", names, rendered)
	}
}

// Render-time refusals: things only the filesystem or the merge can decide.
func TestRenderRefusals(t *testing.T) {
	clearOverrides(t)
	bin := fakeBin(t, "node", "sh")
	cases := []struct{ name, yaml, want string }{
		{"newline in command", "services:\n  a:\n    command: \"node x\\nExecStartPre=/bin/rm -rf /\"", "newline"},
		{"newline in a pass-through value", "services:\n  a:\n    command: node x\n    unit: {Service: {TimeoutStopSec: \"10\\n[Install]\\nWantedBy=default.target\"}}", "newline"},
		{"newline in schedule", "services:\n  a:\n    command: node x\n    schedule: \"hourly\\nOnBootSec=1\"", "newline"},
		{"newline in optional env_file path", "services:\n  a:\n    command: node x\n    env_file: [{path: \"x\\nExecStartPre=/bin/id\", required: false}]", "newline"},
		{"OnCalendar owned by schedule", `
services:
  a:
    command: node x
    schedule: hourly
    unit: {Timer: {OnCalendar: daily}}`, "collides with schedule"},
		{"same variable from both sources", `
services:
  a:
    command: node x
    environment: {PORT: "8080"}
    unit: {Service: {Environment: PORT=9999}}`, "already set by environment:"},
		{"workingdirectory collides with working_dir", `
services:
  a:
    command: node x
    unit: {Service: {WorkingDirectory: /tmp}}`, "collides with working_dir"},
		{"restart collides", `
services:
  a:
    command: node x
    restart: always
    unit: {Service: {Restart: no}}`, "collides with restart"},
		{"slice is structural", `
services:
  a:
    command: node x
    unit: {Service: {Slice: other.slice}}`, "collides with systemd-compose"},
		{"unresolvable command", `
services:
  a:
    command: nosuchprogram --flag`, "not found on the search path"},
		{"relative path not executable", `
services:
  a:
    command: ./missing.sh`, "not an executable file"},
		{"working_dir missing", `
services:
  a:
    command: node x
    working_dir: nowhere`, "not a directory"},
		{"required env_file missing", `
services:
  a:
    command: node x
    env_file: nope.env`, "env_file"},
		{"non-repeatable with a list", `
services:
  a:
    command: node x
    unit: {Service: {TimeoutStopSec: ["1", "2"]}}`, "takes one value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ConfigFileName)
			if err := os.WriteFile(path, []byte(strings.TrimSpace(c.yaml)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path, Options{})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			_, err = Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

// The raw ExecStart form renders as given; a second env var concatenates.
func TestRawFormAndConcat(t *testing.T) {
	clearOverrides(t)
	bin := fakeBin(t, "node")
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	y := "services:\n  a:\n    environment: {PORT: \"8080\", PCT: \"100%%\"}\n    unit: {Service: {ExecStart: /bin/sh -c \"echo $HOME\", Environment: EXTRA=1}}\n"
	if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	text := units[1].Text // after the slice
	for _, want := range []string{"ExecStart=/bin/sh -c \"echo $HOME\"\n", "Environment=PORT=8080\n", "Environment=PCT=100%%\n", "Environment=EXTRA=1\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}

// The slice is rendered without resources: too, so it is registered with the
// project and never becomes an orphan when the cap is dropped.
func TestSliceAlwaysRendered(t *testing.T) {
	p, err := loadYAML(t, "name: plain\nservices: {a: {command: node x}}")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{fakeBin(t, "node")}})
	if err != nil {
		t.Fatal(err)
	}
	if units[0].Name != "plain.slice" || strings.Contains(units[0].Text, "[Slice]") || !strings.Contains(units[0].Text, "Project=plain\n") {
		t.Errorf("slice without resources: %s\n%s", units[0].Name, units[0].Text)
	}
}

// The marker's writer and its readers agree: the provenance gate and the
// orphan sweep find Project= and Config= where the renderer puts them.
func TestMarkerRoundTrip(t *testing.T) {
	p, err := loadYAML(t, "name: mk\nservices: {a: {command: node x}, j: {command: node x, schedule: hourly}}")
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{fakeBin(t, "node")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if markerValue(u.Text, "Project") != "mk" || markerValue(u.Text, "Config") != p.ConfigPath {
			t.Errorf("%s: marker reads Project=%q Config=%q", u.Name, markerValue(u.Text, "Project"), markerValue(u.Text, "Config"))
		}
	}
}

func TestProbeFlags(t *testing.T) {
	for _, args := range [][]string{{"--interval"}, {"--timeout", "5s", "--start-period"}, {"--bogus"}, {"--"}} {
		if err := probe(args); err == nil {
			t.Errorf("probe(%v) should refuse", args)
		}
	}
}

func TestQuoteWord(t *testing.T) {
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

func TestSeconds(t *testing.T) {
	cases := map[string]int{"5s": 5, "2min": 120, "1h 30min": 5400, "1h30min": 5400, "500ms": 1, "90": 90, "1.5s": 2, "2 min": 120}
	for in, want := range cases {
		got, err := seconds(in)
		if err != nil || got != want {
			t.Errorf("seconds(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "", "5 s x", "h", "1.2.3s"} {
		if _, err := seconds(bad); err == nil {
			t.Errorf("seconds(%q) should fail", bad)
		}
	}
}

// Specifiers pass through for systemd: a %-led program word is not
// resolved here (verify --user checks it), and environment and listen keep
// theirs.
func TestSpecifiersRender(t *testing.T) {
	p, err := loadYAML(t, `name: sp
services: {a: {command: "%h/bin/tool --sock %t/x.sock", environment: {RT: "%t/rt", PCT: "5%%"}, listen: "%t/a.sock"}}`)
	if err != nil {
		t.Fatal(err)
	}
	units, err := Render(p, RenderOptions{Exe: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	all := units[1].Text + units[2].Text
	for _, want := range []string{"ExecStart=%h/bin/tool --sock %t/x.sock\n", "Environment=RT=%t/rt\n", "Environment=PCT=5%%\n", "ListenStream=%t/a.sock\n"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}

// compose's two spellings: a list is quoted word by word, entrypoint comes
// first, and only the program word must look like a program.
func TestCommandForms(t *testing.T) {
	bin := fakeBin(t, "node")
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
