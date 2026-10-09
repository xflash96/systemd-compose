// Package config reads a systemd-compose.yaml: it loads the file, fills in
// variables from the .env beside it, and refuses what is malformed,
// contradictory, or has no faithful systemd form. The result is a
// validated Project for the renderer.
//
// Names in the high-level keys are service names from the file; systemd
// unit names appear only inside the unit: pass-through. An unknown key is
// an error with a line number, since a misspelled key that is ignored runs
// the service with the wrong settings and no error.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFileName is the project file that discovery looks for.
const ConfigFileName = "systemd-compose.yaml"

// RenderDirName is the directory in the project that holds the rendered
// units. A link into one is how a registered unit names the project it
// belongs to, so the name has one owner here.
const RenderDirName = ".systemd-compose"

// ProjectNameVar overrides the project name from the environment or from a
// `.env` file beside the yaml. The project name is the namespace: every
// unit, the target and the slice carry it, so two clones of one project, or
// two variants of your own, coexist by taking different names without an
// edit to the yaml. Precedence: -p flag, this variable in the environment,
// this variable in .env, `name:` in the file, the directory's name.
const ProjectNameVar = "SYSTEMD_COMPOSE_PROJECT_NAME"

// ProfilesVar names the active profiles, comma-separated, from the
// environment or the .env beside the yaml; --profile replaces it, as
// compose's --profile replaces COMPOSE_PROFILES.
const ProfilesVar = "SYSTEMD_COMPOSE_PROFILES"

// OverrideVars are the environment overrides the tool reads. Each is
// SYSTEMD_COMPOSE_*: the e2e tests unset them by that prefix, and the unit
// tests set them to junk and clear them per test.
var OverrideVars = []string{ProjectNameVar, ProfilesVar}

// Options are what the command line says about loading: -p and --profile.
type Options struct {
	Name     string
	Profiles []string
}

// Project is the validated model of one systemd-compose.yaml.
type Project struct {
	Name         string
	NameFrom     string // where Name came from: -p | environment | .env | name: | directory
	Dir          string // directory holding the yaml, absolute
	ConfigPath   string // the yaml, absolute
	Resources    *Resources
	Services     []*Service // in file order, every profile
	Profiles     []string   // the active profiles; "*" is all
	ProfilesFrom string     // --profile | environment | .env
	Notes        []string   // what up says before its plan: legal, but likely not meant
	NeedsProfile string     // a required depends_on into a profile that is off: why nothing may start
}

// ---- lookup ---------------------------------------------------------------

// FindConfig walks up from dir looking for systemd-compose.yaml, the way
// compose discovers its file. It returns "" when nothing is found.
func FindConfig(dir string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, ConfigFileName)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ---- loading --------------------------------------------------------------

var (
	reProjectName = regexp.MustCompile(projectNamePattern)
	reServiceName = regexp.MustCompile(serviceNamePattern)
	reMemory      = regexp.MustCompile(memoryPattern)
	reBadName     = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	reDirective   = regexp.MustCompile(directivePattern)
	reProfile     = regexp.MustCompile(profilePattern)
)

// Load reads and validates the yaml at path. Options carry -p and
// --profile; the environment and a .env beside the yaml are consulted next.
func Load(path string, o Options) (*Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The directory is resolved, so one project reached by two paths is one
	// project; the yaml itself is not: a systemd-compose.yaml symlinked from
	// elsewhere (a dotfiles checkout) is a project where the link is, as in
	// compose. Following it would move the name, the paths and the render
	// directory to the target's directory.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	abs = filepath.Join(dir, filepath.Base(abs))
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	vars, err := readDotenv(filepath.Join(filepath.Dir(abs), ".env"))
	if err != nil {
		return nil, err
	}
	override, from := nameOverride(o.Name, vars)
	p, err := parse(data, abs, override, from, vars)
	if err == nil {
		err = applyProfiles(p, o.Profiles, vars)
	}
	if err == nil {
		err = sharedListen(p) // of what this run renders: two profiles may take turns
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(abs), err)
	}
	return p, nil
}

// nameOverride resolves the project name sources that live outside the yaml.
func nameOverride(flag string, dotenv map[string]string) (name, from string) {
	if flag != "" {
		return flag, "-p"
	}
	if v := os.Getenv(ProjectNameVar); v != "" {
		return v, "environment"
	}
	if v := dotenv[ProjectNameVar]; v != "" {
		return v, ".env"
	}
	return "", ""
}

func parse(data []byte, abs string, override, from string, vars map[string]string) (*Project, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil && err != io.EOF {
		return nil, yamlError(err, data)
	}
	// a second document after a --- would be dropped without a word
	var more yaml.Node
	if err := dec.Decode(&more); err == nil && len(more.Content) > 0 && more.Content[0].Tag != "!!null" {
		return nil, fmt.Errorf("line %d: a second yaml document starts here (after a ---); this file holds one", more.Content[0].Line)
	} else if err != nil && err != io.EOF {
		return nil, yamlError(err, data)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	clipBlocks(&doc)
	// The keys first: a ported compose file's unknown keys are its news,
	// ahead of a $VAR it does not set.
	root := doc.Content[0]
	top, err := mapping(root, "top level")
	if err != nil {
		return nil, err
	}
	top = withoutExtensions(top)
	if err := allUnknownKeys(top); err != nil {
		return nil, err
	}
	detachUnitAliases(&doc, nil, map[*yaml.Node]bool{})
	filled, err := interpolateTree(&doc, vars)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: filepath.Dir(abs), ConfigPath: abs}

	switch {
	case override != "":
		if err := projectName(override, "from "+from); err != nil {
			return nil, err
		}
		p.Name, p.NameFrom = override, from
	case top.get("name") != nil:
		n := top.get("name")
		s, err := scalar(n, "name")
		if err != nil {
			return nil, err
		}
		if err := projectName(s, fmt.Sprintf("line %d: name:", n.Line)); err != nil {
			return nil, err
		}
		p.Name, p.NameFrom = s, "name:"
	default:
		p.Name, p.NameFrom = sanitizeName(filepath.Base(p.Dir)), "directory"
		if p.Name == "" {
			return nil, fmt.Errorf("the directory name %q yields no usable project name; set name:", filepath.Base(p.Dir))
		}
		if len(p.Name) > maxProjectName {
			return nil, fmt.Errorf("the directory name %s... is too long for a project name (%d characters at most); set name:", p.Name[:40], maxProjectName)
		}
	}

	if n := top.get("resources"); n != nil {
		r, err := parseResources(n, "resources")
		if err != nil {
			return nil, err
		}
		p.Resources = r
	}

	sn := top.get("services")
	if sn == nil {
		return nil, fmt.Errorf("services: is required")
	}
	services, err := mapping(sn, "services")
	if err != nil {
		return nil, err
	}
	if len(services.pairs) == 0 {
		return nil, fmt.Errorf("line %d: services: is empty", sn.Line)
	}
	// each service's first problem, all at once (PerService); mapping()
	// has refused a name given twice
	var problems []string
	for _, kv := range services.pairs {
		name := kv.key.Value
		if !reServiceName.MatchString(name) {
			problems = append(problems, fmt.Sprintf("line %d: service %q: a service name is letters, digits, _ and -, and does not start with - (a command line would read it as a flag)", kv.key.Line, name))
			continue
		}
		svc, err := parseService(name, kv.value, p)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		p.Services = append(p.Services, svc)
	}
	if err := PerService(problems); err != nil {
		return nil, err
	}
	// A unit's file name is <project>-<service>.service, and a file name is
	// 255 bytes at most (up writes it through a temp file of its own name).
	for _, svc := range p.Services {
		if u := p.ServiceUnit(svc); len(u) > 255 {
			return nil, fmt.Errorf("service %s: its unit name %s... is %d bytes, and a unit file name is 255 at most: shorten the service's name or the project's", svc.Name, u[:40], len(u))
		}
	}
	if err := validateGraph(p); err != nil {
		return nil, err
	}
	for _, svc := range p.Services {
		own := ownVariables(svc)
		noted := map[string]bool{}
		for _, v := range filled[svc.Name] {
			if own[v] && !noted[v] {
				noted[v] = true
				from := "the .env beside the yaml"
				if _, set := vars[v]; !set {
					from = "the default the yaml gives it"
				}
				p.Notes = append(p.Notes, fmt.Sprintf("service %s: $%s was filled in from %s when rendering, though the service's own environment sets %s too; to read its own when it runs, write $$%s inside a shell line ([sh, -c, '...']), where the shell expands it", svc.Name, v, from, v, v))
			}
		}
		p.Notes = append(p.Notes, specifierNotes(svc)...)
		if sc := svc.Schedule; sc != nil && sc.Never {
			p.Notes = append(p.Notes, fmt.Sprintf("service %s: schedule: %q has no next run (a date that has passed, or one that never comes), so its timer will not run it; the run verb runs it now", svc.Name, sc.Calendar))
		}
	}
	return p, nil
}

// PerService words several services' problems as one error, in line order:
// a ported file takes one round per problem a service has, not one per
// problem in the file. One problem is returned as it is.
func PerService(problems []string) error {
	switch len(problems) {
	case 0:
		return nil
	case 1:
		return errors.New(problems[0])
	}
	sort.SliceStable(problems, func(i, j int) bool { return lineOf(problems[i]) < lineOf(problems[j]) })
	return fmt.Errorf("%d services have a problem:\n  %s", len(problems), strings.Join(problems, "\n  "))
}

// lineOf is the line a "line N: ..." message names; 0 for one that names
// none, which sorts first.
func lineOf(msg string) int {
	var n int
	fmt.Sscanf(msg, "line %d:", &n)
	return n
}

// ServiceNameOK reports whether name may name a service.
func ServiceNameOK(name string) bool { return reServiceName.MatchString(name) }

// projectName is the one rule for a project name, wherever it comes from.
func projectName(s, where string) error {
	if !reProjectName.MatchString(s) {
		return fmt.Errorf("%s %q: a project name may only contain letters, digits, _ and -, and does not start with -", where, s)
	}
	if n := len(EscapeName(s)); n > maxProjectName {
		return fmt.Errorf("%s %s...: a project name is %d characters at most, a - counting 4 (it is %d): a unit name stops at 255 bytes, and a one-off's, build-NAME-PID-STEP.service, adds 27", where, s[:40], maxProjectName, n)
	}
	return nil
}

// maxProjectName keeps every unit name under systemd's 255 bytes, a
// one-off's too, which systemd would refuse with a bare line. It bounds
// the name as units spell it (EscapeName).
const maxProjectName = 200

// Service finds a service by name; nil when there is none.
func (p *Project) Service(name string) *Service {
	for _, s := range p.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Lookup is Service with the one error every verb gives for an unknown name.
func (p *Project) Lookup(name string) (*Service, error) {
	if s := p.Service(name); s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("no service %q in project %s (services: %s)", name, p.Name, strings.Join(p.ServiceNames(), ", "))
}

// ServiceNames lists every service the file declares, in file order.
func (p *Project) ServiceNames() []string {
	names := make([]string, 0, len(p.Services))
	for _, s := range p.Services {
		names = append(names, s.Name)
	}
	return names
}

func sanitizeName(base string) string {
	return strings.Trim(reBadName.ReplaceAllString(base, "_"), "_")
}

// topKeys are the top-level keys of compose's this tool does not take. No
// version key yet: the schema's promise (README) is that a key that has to
// change its meaning brings an optional version: with it, and a file
// without one keeps today's meaning.
var topKeys = map[string]string{
	"version":  "there is no version key; a file without one keeps meaning what it means today (drop the line)",
	"volumes":  "no volumes: a service uses this host's filesystem",
	"networks": composeKeys["networks"],
	"configs":  composeKeys["configs"],
	"secrets":  composeKeys["secrets"],
}

// allUnknownKeys reports every unknown key at the top level and directly in
// each service at once, so a ported file takes one round, not one per key.
func allUnknownKeys(top *mapNode) error {
	type finding struct {
		line int
		text string
	}
	var found []finding
	var known []string // "where: the keys", once for each place a key was unknown
	check := func(m *mapNode, ctx, where string, allowed []string, answer func(string, *yaml.Node) string) {
		for _, kv := range m.pairs {
			k := kv.key.Value
			if slices.Contains(allowed, k) {
				continue
			}
			if why := answer(k, kv.value); why != "" {
				found = append(found, finding{kv.key.Line, fmt.Sprintf("line %d: %s: %q: %s", kv.key.Line, ctx, k, why)})
				continue
			}
			found = append(found, finding{kv.key.Line, fmt.Sprintf("line %d: %s: unknown key %q", kv.key.Line, ctx, k)})
			if !slices.ContainsFunc(known, func(s string) bool { return strings.HasPrefix(s, where+":") }) {
				known = append(known, where+": "+knownList(allowed))
			}
		}
	}
	check(top, "top level", "known at the top level", Spec.names(), func(k string, _ *yaml.Node) string { return topKeys[k] })
	// services: as the parser reads it, through an alias or a merge key; a
	// map it refuses is left to the parser to say why
	if sn := top.get("services"); sn != nil {
		if services, err := mapping(sn, "services"); err == nil {
			for _, kv := range services.pairs {
				if m, err := mapping(kv.value, "service "+kv.key.Value); err == nil {
					check(withoutExtensions(m), "service "+kv.key.Value, "known in a service", ServiceKeys, composeAnswer)
				}
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].line < found[j].line })
	switch len(found) {
	case 0:
		return nil
	case 1:
		if len(known) == 1 {
			_, keys, _ := strings.Cut(known[0], ": ")
			return fmt.Errorf("%s (known: %s)", found[0].text, keys)
		}
		return errors.New(found[0].text)
	}
	lines := make([]string, 0, len(found)+len(known))
	for _, f := range found {
		lines = append(lines, f.text)
	}
	lines = append(lines, known...)
	return fmt.Errorf("%d keys this tool does not take (docs/compose.md):\n  %s", len(found), strings.Join(lines, "\n  "))
}
