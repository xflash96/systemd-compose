package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xflash96/systemd-compose/internal/testenv"
)

func TestInterpolate_ExpandsComposeForms(t *testing.T) {
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
		"${B:-$$literal}":          "$literal",
		"${B:-${A:+set}}":          "set",
	}
	for in, want := range good {
		got, err := interpolate(in, vars)
		if err != nil || got != want {
			t.Errorf("interpolate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"$B":              "B is not set",
		"$A_B and ${A}_B": "A_B is not set", // $A_B is the variable A_B, not $A + "_B"
		"${B}":            "B is not set",
		"${EMPTY:?gone}":  "EMPTY: gone",
		"${B?no ${A}}":    "B: no a",
		"${A":             "unclosed",
		"$1":              "write $$",
		"cost $ 5":        "write $$",
		"${}":             "no variable name",
		"${A/x/y}":        "unknown form",
	}
	for in, want := range bad {
		if _, err := interpolate(in, vars); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("interpolate(%q): want error containing %q, got %v", in, want, err)
		}
	}
}

func TestReadDotenv_ReadsComposeDotenv(t *testing.T) {
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
	os.WriteFile(p, []byte("A=\"x\" # note\nB='y' # note\n"), 0o644)
	if v, err := readDotenv(p); err != nil || v["A"] != "x" || v["B"] != "y" {
		t.Errorf("a comment after a quoted value must not leave the quotes in it: %q %v", v, err)
	}
	os.WriteFile(p, []byte(`A="say \"hi\"" # note`+"\n"), 0o644)
	if v, err := readDotenv(p); err != nil || v["A"] != `say "hi"` {
		t.Errorf("an escaped quote is not the closing one: %q %v", v, err)
	}
	os.WriteFile(p, []byte("A=\"x\" junk\n"), 0o644)
	if _, err := readDotenv(p); err == nil || !strings.Contains(err.Error(), "after the closing") {
		t.Errorf("junk after the closing quote: %v", err)
	}
	os.WriteFile(p, []byte("A=\"x\nB=1\n"), 0o644)
	if _, err := readDotenv(p); err == nil || !strings.Contains(err.Error(), ":1: unclosed") {
		t.Errorf("unclosed quote: %v", err)
	}
	os.WriteFile(p, []byte("CERT=\"-----BEGIN-----\nabc\n-----END-----\" # pem\nNEXT='a\nb'\nAFTER=$NEXT\n"), 0o644)
	if v, err := readDotenv(p); err != nil || v["CERT"] != "-----BEGIN-----\nabc\n-----END-----" || v["NEXT"] != "a\nb" || v["AFTER"] != "a\nb" {
		t.Errorf("multi-line quoted values: %q %v", v, err)
	}
	if v, err := readDotenv(filepath.Join(t.TempDir(), "none")); err != nil || len(v) != 0 {
		t.Errorf("missing .env: %v %v", v, err)
	}
	os.WriteFile(p, []byte("PORT=1\nnot a line\n"), 0o644)
	if _, err := readDotenv(p); err == nil || !strings.Contains(err.Error(), ":2: expected KEY=value") {
		t.Errorf("malformed line: %v", err)
	}
}

// A $NAME filled in from .env that the service also sets itself is noted:
// the program sees the render-time value, never its own; and the unset
// error offers $$NAME, the spelling that leaves it to the service.
func TestLoad_NotesAVariableTheServiceSetsItself(t *testing.T) {
	testenv.ClearOverrides(t)
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".env", "INTERVAL=5\nGREETING=hi\n")
	write("app.env", "INTERVAL=61\n")
	write(ConfigFileName, `services:
  a:
    command: [sh, -c, 'sleep $INTERVAL; echo ${GREETING}; echo $$HOME']
    env_file: [app.env]
  b:
    command: [sh, -c, 'echo $GREETING']
    environment: {OTHER: "1"}
`)
	p, err := Load(filepath.Join(dir, ConfigFileName), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "service a: $INTERVAL") || !strings.Contains(p.Notes[0], "$$INTERVAL") {
		t.Errorf("notes = %q", p.Notes)
	}
	write(ConfigFileName, "services: {a: {command: [sh, -c, 'echo ${PORT:-80}'], environment: {PORT: \"80\"}}}\n")
	if p, err := Load(filepath.Join(dir, ConfigFileName), Options{}); err != nil || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "from the default the yaml gives it") {
		t.Errorf("a default is not the .env: %v %q", err, p.Notes)
	}
	write(ConfigFileName, "services: {a: {command: [sh, -c, 'echo $NOPE']}}\n")
	if _, err := Load(filepath.Join(dir, ConfigFileName), Options{}); err == nil || !strings.Contains(err.Error(), "write $$NOPE") {
		t.Errorf("unset: %v", err)
	}
}
