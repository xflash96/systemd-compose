package config

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

const schemaPath = "../../config-schema.json"

// schema is config-schema.json, resolved for validation.
func schema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	return resolve(t, data)
}

// resolve is a JSON Schema, resolved for validation.
func resolve(t *testing.T, data []byte) *jsonschema.Resolved {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// instance is a yaml document as the schema sees it: merge keys applied,
// then through JSON, so numbers and keys are JSON's.
func instance(t *testing.T, y string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(y), &v); err != nil {
		t.Fatalf("%q: %v", y, err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%q: %v", y, err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The reference example and every project under examples/ validate
// against the schema, and start with the modeline that names it by its $id.
func TestSchema_AcceptsTheExamples(t *testing.T) {
	s := schema(t)
	files, _ := filepath.Glob("../../examples/*/" + ConfigFileName)
	files = append(files, "../../docs/config.example.yaml")
	modeline := "# yaml-language-server: $schema=" + schemaID + "\n"
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(instance(t, string(data))); err != nil {
			t.Errorf("%s does not match %s:\n%v", f, schemaPath, err)
		}
		if !strings.HasPrefix(string(data), modeline) {
			t.Errorf("%s should start with %q", f, modeline)
		}
	}
	if len(files) < 4 {
		t.Errorf("found %d example files", len(files))
	}
}

// The schema and the parser agree on what a file may say: each case is
// accepted by both or refused by both. The parser refuses more (what only
// the filesystem or a cross-key rule decides); those cases are not here.
func TestSchema_AgreesWithTheParser(t *testing.T) {
	s := schema(t)
	cases := []struct {
		yaml string
		ok   bool
	}{
		// the top level
		{"services: {a: {command: /bin/true}}", true},
		{"name: web_1\nservices: {a: {command: /bin/true}}", true},
		{"name: web-1\nservices: {a: {command: /bin/true}}", true},
		{"name: -web\nservices: {a: {command: /bin/true}}", false},
		{"name: web.1\nservices: {a: {command: /bin/true}}", false},
		{"x-base: {restart: always}\nservices: {a: {command: /bin/true}}", true},
		{"version: '3'\nservices: {a: {command: /bin/true}}", false},
		{"networks: {}\nservices: {a: {command: /bin/true}}", false},
		{"name: p", false},
		{"services: {}", false},
		{"resources: {memory: 1G, pids: 100}\nservices: {a: {command: /bin/true}}", true},
		{"resources: {}\nservices: {a: {command: /bin/true}}", false},
		{"resources: {memory: lots}\nservices: {a: {command: /bin/true}}", false},
		{"resources: {cpus: 0}\nservices: {a: {command: /bin/true}}", false},
		{"resources: {pids: 0}\nservices: {a: {command: /bin/true}}", false},
		{"resources: {swap: 1G}\nservices: {a: {command: /bin/true}}", false},
		{"resources: {memory: 1G, swap: 1G}\nservices: {a: {command: /bin/true}}", false},
		{"x-more: &more {b: {command: /bin/true}}\nservices: {<<: *more, a: {command: /bin/true}}", true},
		{"x-more: &more {b: {command: /bin/true, bogus: 1}}\nservices: {<<: *more, a: {command: /bin/true}}", false},
		{"x-s: &s {a: {command: /bin/true}}\nservices: *s", true},
		{"x-s: &s {a: {command: /bin/true, bogus: 1}}\nservices: *s", false},

		// a value given as a yaml alias, whole or as a list item
		{"x-e: &e {A: \"1\"}\nservices: {a: {command: /bin/true, environment: *e}}", true},
		{"x-c: &c [/bin/echo, hi]\nservices: {a: {command: *c}}", true},
		{"x-d: &d [b]\nservices: {b: {command: /bin/true}, a: {command: /bin/true, depends_on: *d}}", true},
		{"x-t: &t [/bin/true]\nservices: {a: {command: /bin/true, healthcheck: {test: *t}}}", true},
		{"x-f: &f a.env\nservices: {a: {command: /bin/true, env_file: [*f]}}", true},
		{"x-r: &r sometimes\nservices: {a: {command: /bin/true, restart: *r}}", false},

		// x- keys only where the spec takes them: the top level and a service
		{"services: {a: {command: /bin/true, restart: {policy: always, x-a: 1}}}", false},
		{"services: {b: {command: /bin/true}, a: {command: /bin/true, depends_on: {b: {condition: service_started, x-a: 1}}}}", false},
		{"services: {a: {command: /bin/true, schedule: {calendar: daily, x-a: 1}}}", false},
		{"services: {a: {command: /bin/true, healthcheck: {test: [/bin/true], x-a: 1}}}", false},
		{"services: {a: {command: /bin/true, build: {run: [make], x-a: 1}}}", false},
		{"services: {a: {command: /bin/true, resources: {memory: 1G, x-a: 1}}}", false},
		{"services: {a: {command: /bin/true, env_file: [{path: a.env, x-a: 1}]}}", false},

		// service names and the program
		{"services: {-a: {command: /bin/true}}", false},
		{"services: {a: {}}", false},
		{"services: {a: {command: [/bin/echo, hi]}}", true},
		{"services: {a: {command: []}}", false},
		{"services: {a: {entrypoint: /bin/echo}}", true},
		{"services: {a: {unit: {Service: {ExecStart: /bin/true}}}}", true},
		{"services: {a: {unit: {Service: {Type: simple}}}}", false},
		{"services: {a: {command: /bin/true, image: x}}", false},
		{"services: {a: {command: /bin/true, x-note: anything}}", true},
		{"services: {a: {command: /bin/true, bogus: 1}}", false},
		{"services: {a: {command: /bin/true, working_dir: \"\"}}", false},

		// environment and env_file
		{"services: {a: {command: /bin/true, environment: {A: 1, B_2: two}}}", true},
		{"services: {a: {command: /bin/true, environment: [A=1, B=]}}", true},
		{"services: {a: {command: /bin/true, environment: [A]}}", false},
		{"services: {a: {command: /bin/true, environment: {A: }}}", false},
		{"services: {a: {command: /bin/true, environment: {1A: x}}}", false},
		{"services: {a: {command: /bin/true, environment: A=1}}", false},
		{"services: {a: {command: /bin/true, env_file: .env}}", true},
		{"services: {a: {command: /bin/true, env_file: [.env, {path: .env.local, required: false}]}}", true},
		{"services: {a: {command: /bin/true, env_file: {required: false}}}", false},
		{"services: {a: {command: /bin/true, env_file: {path: .env, format: raw}}}", false},

		// restart and on_change
		{"services: {a: {command: /bin/true, restart: unless-stopped}}", true},
		{"services: {a: {command: /bin/true, restart: {policy: on-failure, delay: 3s}}}", true},
		{"services: {a: {command: /bin/true, restart: sometimes}}", false},
		{"services: {a: {command: /bin/true, restart: {delay: 3s}}}", false},
		{"services: {a: {command: /bin/true, restart: {policy: always, delay: soon}}}", false},
		{"services: {a: {command: /bin/true, on_change: start-only}}", true},
		{"services: {a: {command: /bin/true, on_change: never}}", false},

		// depends_on
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: [a]}}", true},
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: {a: {}}}}", true},
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: {a: }}}", true},
		{"services: {a: {command: /bin/true, oneshot: true}, b: {command: /bin/true, depends_on: {a: {condition: service_completed_successfully, restart: true}}}}", true},
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: {a: {condition: service_ready}}}}", false},
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: {a: {wait: true}}}}", false},
		{"services: {a: {command: /bin/true}, b: {command: /bin/true, depends_on: a}}", false},

		// schedule
		{"services: {a: {command: /bin/true, schedule: daily}}", true},
		{"services: {a: {command: /bin/true, schedule: {calendar: daily, accuracy: 1min, persistent: false, randomized_delay: 5min}}}", true},
		{"services: {a: {command: /bin/true, schedule: {accuracy: 1min}}}", false},
		{"services: {a: {command: /bin/true, schedule: {calendar: daily, jitter: 5min}}}", false},

		// healthcheck
		{"services: {a: {command: /bin/true, healthcheck: {test: [/bin/true], interval: 1s, timeout: 3, start_period: 1min}}}", true},
		{"services: {a: {command: /bin/true, healthcheck: {interval: 1s}}}", false},
		{"services: {a: {command: /bin/true, healthcheck: {test: []}}}", false},
		{"services: {a: {command: /bin/true, healthcheck: {test: /bin/true}}}", false},
		{"services: {a: {command: /bin/true, healthcheck: {test: [/bin/true], retries: 3}}}", false},
		{"services: {a: {command: /bin/true, healthcheck: {test: [/bin/true], interval: often}}}", false},

		// oneshot, build, resources, listen, profiles
		{"services: {a: {command: /bin/true, oneshot: true}}", true},
		{"services: {a: {command: /bin/true, oneshot: maybe}}", false},
		{"services: {a: {command: /bin/true, build: {run: [make], creates: out}}}", true},
		{"services: {a: {command: /bin/true, build: {creates: out}}}", false},
		{"services: {a: {command: /bin/true, build: {run: []}}}", false},
		{"services: {a: {command: /bin/true, build: {run: [make], context: .}}}", false},
		{"services: {a: {command: /bin/true, build: {run: [make], creates: \"\"}}}", false},
		{"services: {a: {command: /bin/true, resources: {memory: 64M, cpus: 0.5, pids: 32}}}", true},
		{"services: {a: {command: /bin/true, resources: {memory: 64m}}}", false},
		{"services: {a: {command: /bin/true, listen: 8080}}", true},
		{"services: {a: {command: /bin/true, listen: [8080, \"127.0.0.1:8081\", /run/a.sock, \"@a\"]}}", true},
		{"services: {a: {command: /bin/true, listen: []}}", false},
		{"services: {a: {command: /bin/true, profiles: [debug, x.y-z]}}", true},
		{"services: {a: {command: /bin/true, profiles: []}}", false},
		{"services: {a: {command: /bin/true, profiles: debug}}", false},
		{"services: {a: {command: /bin/true, profiles: [-x]}}", false},

		// unit:
		{"services: {a: {command: /bin/true, unit: {Unit: {After: [x.service, y.service]}, Service: {Nice: 10}}}}", true},
		{"services: {a: {command: /bin/true, unit: {Install: {WantedBy: default.target}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Exec: {Nice: 10}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {Environment: A=1}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {EnvironmentFile: /x}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {WorkingDirectory: /x}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {IOWeight: 100}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {Nice: {x: 1}}}}}", false},
		{"services: {a: {command: /bin/true, unit: {Service: {9Nice: 1}}}}", false},
	}
	for _, c := range cases {
		schemaErr := s.Validate(instance(t, c.yaml))
		_, loadErr := loadYAML(t, c.yaml)
		if (schemaErr == nil) != c.ok {
			t.Errorf("schema on %q: %v, want ok=%v", c.yaml, schemaErr, c.ok)
		}
		if (loadErr == nil) != c.ok {
			t.Errorf("Load on %q: %v, want ok=%v", c.yaml, loadErr, c.ok)
		}
	}
}

const schemaID = "https://raw.githubusercontent.com/xflash96/systemd-compose/main/config-schema.json"

// config-schema.json is what the spec generates. UPDATE_SCHEMA=1 writes it:
// UPDATE_SCHEMA=1 go test ./internal/config -run TestSchema_IsGeneratedFromTheSpec
func TestSchema_IsGeneratedFromTheSpec(t *testing.T) {
	want := generatedSchema()
	if os.Getenv("UPDATE_SCHEMA") != "" {
		if err := os.WriteFile(schemaPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is not what the spec generates; UPDATE_SCHEMA=1 go test ./internal/config -run TestSchema_IsGeneratedFromTheSpec writes it", schemaPath)
	}
}

// The pattern the schema gives a variable name is the rule IsVarName keeps.
func TestSpec_VarNamePatternIsIsVarName(t *testing.T) {
	re := regexp.MustCompile(varNamePattern)
	for _, s := range []string{"A", "a1", "_x", "A_B9", "", "1A", "A-B", "A B", "é", "a.b"} {
		if re.MatchString(s) != IsVarName(s) {
			t.Errorf("%q: pattern %v, IsVarName %v", s, re.MatchString(s), IsVarName(s))
		}
	}
}

// The pattern the schema gives a time span takes the units Seconds takes:
// each the pattern names, and none of a few it does not.
func TestSpec_DurationPatternIsSeconds(t *testing.T) {
	re := regexp.MustCompile(durationPattern)
	units := regexp.MustCompile(`\(([a-z|]+)\)`).FindStringSubmatch(durationPattern)[1]
	spans := []string{"1..5s", "1.2.3s", "1. .5s", "12.34s.56", ".5s", "5.s", ".", "5 m s", "1 0s", " 5 s "}
	for _, u := range append(strings.Split(units, "|"), "", "y", "mon", "month", "secs", "mins", "M", "ns", "µs") {
		spans = append(spans, "5"+u, "1.5 "+u, "1"+u+" 2"+u)
	}
	for _, s := range spans {
		_, err := Seconds(s)
		if re.MatchString(s) != (err == nil) {
			t.Errorf("%q: pattern %v, Seconds err %v", s, re.MatchString(s), err)
		}
	}
}

// Every default the table states is one config.Default reaches by its
// key's path, and a value the key's own forms take.
func TestSpec_DefaultsAreValuesTheirKeysTake(t *testing.T) {
	var walk func(f Form, path string, fn func(k Key, path string))
	walk = func(f Form, path string, fn func(Key, string)) {
		for _, k := range f.Keys {
			p := strings.TrimPrefix(path+"."+k.Name, ".")
			fn(k, p)
			for _, kf := range k.Forms {
				walk(kf, p, fn)
			}
		}
		for _, item := range f.Items {
			walk(item, path, fn)
		}
	}
	inService, all := 0, 0
	walk(serviceMap, "", func(k Key, path string) {
		if k.Default == "" {
			return
		}
		inService++
		if got := Default(path); got != k.Default {
			t.Errorf("Default(%q) is %q, want %q", path, got, k.Default)
		}
		g := schemaGen{defs: map[string]any{}}
		s := g.forms(k.Forms)
		s["$schema"], s["definitions"] = draft07, g.defs
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolve(t, data).Validate(instance(t, k.Default)); err != nil {
			t.Errorf("%s: its default %q is not a value it takes: %v", path, k.Default, err)
		}
	})
	walk(Spec, "", func(k Key, _ string) {
		if k.Default != "" {
			all++
		}
	})
	if all != inService {
		t.Errorf("%d defaults sit outside a service, where config.Default does not reach", all-inService)
	}
}

const draft07 = "http://json-schema.org/draft-07/schema#"

// generatedSchema is config-schema.json as Spec states it.
func generatedSchema() []byte {
	g := schemaGen{defs: map[string]any{}}
	root := g.form(Spec)
	root["$schema"] = draft07
	root["$id"] = schemaID
	root["title"] = "systemd-compose.yaml"
	root["description"] = "A systemd-compose project: services that run as systemd user units. docs/config.example.yaml shows every key."
	root["definitions"] = g.defs
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(out, '\n')
}

// shared are the forms that occur in more than one place. The schema
// states each once, under definitions, by its name in spec.go, and refers
// to it everywhere it occurs.
var shared = []struct {
	name  string
	forms []Form
}{
	{"interpolated", []Form{interpolated}},
	{"boolean", boolean},
	{"spanForms", spanForms},
	{"words", words},
	{"restartPolicy", []Form{restartPolicy}},
	{"resourcesMap", []Form{resourcesMap}},
	{"envFileMap", []Form{envFileMap}},
	{"unitSection", []Form{unitSection(nil)}},
}

// schemaGen writes forms in JSON Schema; defs are the shared ones it has
// written.
type schemaGen struct{ defs map[string]any }

// forms is a value that takes any of forms: a $ref when they are shared.
func (g schemaGen) forms(forms []Form) map[string]any {
	for _, sh := range shared {
		if reflect.DeepEqual(forms, sh.forms) {
			if g.defs[sh.name] == nil {
				g.defs[sh.name] = g.alts(forms)
			}
			return map[string]any{"$ref": "#/definitions/" + sh.name}
		}
	}
	return g.alts(forms)
}

// alts is forms written out: the one form, or anyOf them.
func (g schemaGen) alts(forms []Form) map[string]any {
	if len(forms) == 1 {
		return g.form(forms[0])
	}
	var alts []any
	for _, f := range forms {
		alts = append(alts, g.forms([]Form{f}))
	}
	return map[string]any{"anyOf": alts}
}

// form is a Form in JSON Schema.
func (g schemaGen) form(f Form) map[string]any {
	s := map[string]any{}
	switch f.Kind {
	case Text:
		s["type"] = "string"
		if f.Pattern != "" {
			s["pattern"] = f.Pattern
		}
		if f.MinLen > 0 {
			s["minLength"] = f.MinLen
		}
		if f.MaxLen > 0 {
			s["maxLength"] = f.MaxLen
		}
	case Word:
		s["enum"] = f.Words
	case Bool:
		s["type"] = "boolean"
	case Number, Integer:
		s["type"] = map[Kind]string{Number: "number", Integer: "integer"}[f.Kind]
		if f.Bounded {
			s["minimum"] = f.Min
		}
		if f.Max > 0 {
			s["maximum"] = f.Max
		}
	case Scalar:
		s["type"] = []string{"string", "number", "boolean"}
	case Null:
		s["type"] = "null"
	case List:
		s["type"] = "array"
		if f.MinLen > 0 {
			s["minItems"] = f.MinLen
		}
		s["items"] = g.forms(f.Items)
	case Map:
		s["type"] = "object"
		if f.MinLen > 0 {
			s["minProperties"] = f.MinLen
		}
		names := map[string]any{}
		if f.Pattern != "" {
			names["pattern"] = f.Pattern
		}
		if len(f.Refused) > 0 {
			names["not"] = map[string]any{"enum": slices.Sorted(maps.Keys(f.Refused))}
		}
		if len(names) > 0 {
			s["propertyNames"] = names
		}
		s["additionalProperties"] = g.forms(f.Items)
	case Object:
		s["type"] = "object"
		props := map[string]any{}
		var required []string
		for _, k := range f.Keys {
			props[k.Name] = g.key(k)
			if k.Required {
				required = append(required, k.Name)
			}
		}
		s["properties"] = props
		if len(required) > 0 {
			s["required"] = required
		}
		if f.MinLen > 0 {
			s["minProperties"] = f.MinLen
		}
		s["additionalProperties"] = false
		if f.Extensions {
			s["patternProperties"] = map[string]any{"^x-": map[string]any{"description": "Ignored: a place for yaml anchors that services merge in with <<:."}}
		}
		var alts []any
		for _, p := range f.RequireAny {
			alts = append(alts, requirePath(strings.Split(p, ".")))
		}
		if len(alts) > 0 {
			s["anyOf"] = alts
		}
	}
	return s
}

// key is a key's value, with its documentation and default.
func (g schemaGen) key(k Key) map[string]any {
	s := g.forms(k.Forms)
	d := k.Doc
	if k.Default != "" {
		d = strings.TrimSpace(d + " Default: " + k.Default + ".")
	}
	if d != "" {
		s["description"] = d
	}
	return s
}

// requirePath requires a path of keys, a.b.c: a, then b in it, then c.
func requirePath(p []string) map[string]any {
	s := map[string]any{"required": []string{p[0]}}
	if len(p) > 1 {
		s["properties"] = map[string]any{p[0]: requirePath(p[1:])}
	}
	return s
}
