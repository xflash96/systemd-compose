package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolate(t *testing.T) {
	vars := map[string]string{"A": "a", "EMPTY": "", "PORT": "8080"}
	good := map[string]string{
		"plain":                    "plain",
		"$A":                       "a",
		"${A}x":                    "ax",
		"$$A":                      "$A",
		"a$$":                      "a$",
		"${B:-def}":                "def",
		"${EMPTY:-def}":            "def",
		"${EMPTY-def}":             "",
		"${B-def}":                 "def",
		"${A:+yes}":                "yes",
		"${EMPTY:+yes}":            "",
		"${EMPTY+yes}":             "yes",
		"${B+yes}":                 "",
		"${B:-${C:-${PORT}}}":      "8080",
		"--port=${PORT} --host=$A": "--port=8080 --host=a",
		"${A:?never}":              "a",
		"${B:-a b}/x":              "a b/x",
		"${B:-}":                   "",
		"price: $$5 for ${A}pples": "price: $5 for apples",
		"${PORT}${PORT}":           "80808080",
		"$A_B and ${A}_B":          "", // $A_B is the variable A_B
		"${B:-$$literal}":          "$literal",
		"${B:-${A:+set}}":          "set",
	}
	for in, want := range good {
		got, err := interpolate(in, vars)
		if in == "$A_B and ${A}_B" {
			if err == nil || !strings.Contains(err.Error(), "A_B is not set") {
				t.Errorf("%q: want A_B unset, got %q %v", in, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("interpolate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"$B":             "B is not set",
		"${B}":           "B is not set",
		"${EMPTY:?gone}": "EMPTY: gone",
		"${B?no ${A}}":   "B: no a",
		"${A":            "unclosed",
		"$1":             "write $$",
		"cost $ 5":       "write $$",
		"${}":            "no variable name",
		"${A/x/y}":       "unknown form",
	}
	for in, want := range bad {
		if _, err := interpolate(in, vars); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("interpolate(%q): want error containing %q, got %v", in, want, err)
		}
	}
}

func TestReadDotenv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte(`# comment
PORT=8080
export HOST=127.0.0.1
URL=http://${HOST}:$PORT/x # trailing comment
LIT='${HOST} stays'
QUOTED="tab\there ${PORT}"
EMPTY=
`), 0o644)
	v, err := readDotenv(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PORT": "8080", "HOST": "127.0.0.1", "URL": "http://127.0.0.1:8080/x", "LIT": "${HOST} stays", "QUOTED": "tab\there 8080", "EMPTY": ""}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if v, err := readDotenv(filepath.Join(t.TempDir(), "none")); err != nil || len(v) != 0 {
		t.Errorf("missing .env: %v %v", v, err)
	}
	os.WriteFile(p, []byte("PORT=1\nnot a line\n"), 0o644)
	if _, err := readDotenv(p); err == nil || !strings.Contains(err.Error(), ":2: expected KEY=value") {
		t.Errorf("malformed line: %v", err)
	}
}

// Interpolation through Load: values from .env, keys and unit: untouched,
// a plain scalar retyped, a literal $ written $$ in ExecStart.
func TestInterpolationInYaml(t *testing.T) {
	t.Setenv(ProjectNameVar, "")
	bin := fakeBin(t, "node")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PORT=8080\nONE=true\nNAME=interp\nGREETING=hello world\n"), 0o644)
	y := `name: ${NAME}
services:
  a:
    command: node server.mjs --port ${PORT} --price $$5
    environment: {GREETING: "${GREETING}", MODE: "${MODE:-prod}"}
    oneshot: ${ONE}
  b:
    unit:
      Service:
        ExecStart: /bin/sh -c "echo $HOME ${PORT}"
`
	path := filepath.Join(dir, ConfigFileName)
	os.WriteFile(path, []byte(y), 0o644)
	p, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "interp" || !p.Services[0].Oneshot {
		t.Fatalf("name %q oneshot %v", p.Name, p.Services[0].Oneshot)
	}
	units, err := Render(p, RenderOptions{Exe: "/x", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := units[1].Text, units[2].Text
	for _, want := range []string{"ExecStart=" + bin + "/node server.mjs --port 8080 --price $$5\n", `Environment="GREETING=hello world"`, "Environment=MODE=prod\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in\n%s", want, a)
		}
	}
	if !strings.Contains(b, `ExecStart=/bin/sh -c "echo $HOME ${PORT}"`) {
		t.Errorf("unit: was interpolated:\n%s", b)
	}

	os.WriteFile(path, []byte("services:\n  a:\n    command: node x\n    environment: {X: \"${NOPE}\"}\n"), 0o644)
	if _, err := Load(path, Options{}); err == nil || !strings.Contains(err.Error(), "line 4: services: a: environment: X: NOPE is not set") {
		t.Errorf("unset variable: %v", err)
	}
}
