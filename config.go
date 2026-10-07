// config.go — the schema: load a systemd-compose.yaml, refuse what the
// design refuses, and hand the renderer a validated model.
//
// Names in the high-level keys are service names from this file; systemd
// unit names appear only inside the `unit:` pass-through. Unknown keys are a
// hard error with a line number: a misspelled key that is silently ignored
// runs the service with the wrong settings and no error.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const ConfigFileName = "systemd-compose.yaml"

// RenderDirName is the directory in the project that holds the rendered
// units. A link into one is how a registered unit names the project it
// belongs to, so the name has one owner here.
const RenderDirName = ".systemd-compose"

// Every environment override the tool reads is SYSTEMD_COMPOSE_*, and a
// new one joins overrideVars in config_test.go: ci/live unsets them by
// that prefix, and the tests set them to junk and clear them per test.

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
}

type Service struct {
	Name        string
	Command     Words
	Entrypoint  Words  // ExecStart = entrypoint + command, as compose without an image
	WorkingDir  string // absolute; defaults to the project dir
	Environment []KV
	EnvFiles    []EnvFile
	Restart     *Restart
	DependsOn   []Dependency
	Schedule    *Schedule
	Unit        PassThrough
	OnChange    string // "restart" (default) | "start-only"
	Healthcheck *Healthcheck
	Oneshot     bool
	Build       *Build
	Resources   *Resources
	Listen      []string // ListenStream= addresses of the service's socket
	Profiles    []string // empty: always enabled
}

type KV struct{ Key, Value string }

// Words is a command in compose's two spellings: a string that systemd
// parses (Line), or a list with one word per element (List), which the
// renderer quotes so that spaces, quotes and $ are plain characters.
type Words struct {
	Line string
	List []string
}

func (w Words) Empty() bool { return w.Line == "" && len(w.List) == 0 }

// program is the first word, the one that names what runs.
func (w Words) program() string {
	if len(w.List) > 0 {
		return w.List[0]
	}
	first, _, _ := firstWord(w.Line)
	return first
}

type EnvFile struct {
	Path     string // absolute
	Required bool
}

type Restart struct {
	Policy  string // no | on-failure | always
	Written string // the yaml's own word, for messages: unless-stopped is always
	Delay   string // RestartSec, optional
}

type Dependency struct {
	Service   string
	Condition string // service_started | service_healthy | service_completed_successfully
	Required  bool   // Requires= instead of Wants=; always true for the two conditions above service_started
	Restart   bool   // PartOf=
}

type Schedule struct {
	Calendar        string // OnCalendar
	Accuracy        string // AccuracySec, default 10s
	Persistent      bool   // default true
	RandomizedDelay string // RandomizedDelaySec, optional
}

type Healthcheck struct {
	Test        []string
	Interval    string // default 2s
	Timeout     string // default 5s
	StartPeriod string // default 60s
}

type Build struct {
	Run []string
	// Creates is where up looks for what the build makes: absolute once
	// loaded (the yaml's path is relative to the service's working_dir,
	// where the build runs); "" when the build declares none, which up
	// then skips unless --build.
	Creates string
}

type Resources struct {
	Memory string  // MemoryMax
	CPUs   float64 // CPUQuota = CPUs*100%
	PIDs   int     // TasksMax
}

// PassThrough is the raw `unit:` block: section -> key -> values, in file
// order. The renderer merges it under three rules: a directive a
// translated key owns is refused, a repeatable directive concatenates,
// a default gives way.
type PassThrough struct {
	Sections []PassSection
}

type PassSection struct {
	Name string
	Keys []PassKey
}

type PassKey struct {
	Name   string
	Values []string
	Line   int
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
	reProjectName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	reServiceName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	reMemory      = regexp.MustCompile(`^[0-9]+[KMGT]?$`)
	reBadName     = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	reDirective   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	reProfile     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// Load reads and validates the yaml at path. Options carry -p and
// --profile; the environment and a .env beside the yaml are consulted next.
func Load(path string, o Options) (*Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
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
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(abs), err)
	}
	return p, nil
}

// applyProfiles settles the active profiles and what follows from them:
// a required edge to a disabled service is refused, naming the profile to
// activate, and an optional one is dropped (compose does both), so a
// disabled service is never pulled in by one that is enabled.
func applyProfiles(p *Project, flags []string, dotenv map[string]string) error {
	switch {
	case len(flags) > 0:
		p.Profiles, p.ProfilesFrom = flags, "--profile"
	case os.Getenv(ProfilesVar) != "":
		p.Profiles, p.ProfilesFrom = splitList(os.Getenv(ProfilesVar)), "environment"
	case dotenv[ProfilesVar] != "":
		p.Profiles, p.ProfilesFrom = splitList(dotenv[ProfilesVar]), ".env"
	}
	known := map[string]bool{}
	for _, s := range p.Services {
		for _, n := range s.Profiles {
			known[n] = true
		}
	}
	for _, n := range p.Profiles {
		if n != "*" && !known[n] {
			var names []string
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			where := "this file declares no profiles"
			if len(names) > 0 {
				where = "profiles in this file: " + strings.Join(names, ", ")
			}
			return fmt.Errorf("profile %q (from %s) is no service's; %s", n, p.ProfilesFrom, where)
		}
	}
	for _, s := range p.EnabledServices() {
		var kept []Dependency
		for _, d := range s.DependsOn {
			t := p.Service(d.Service)
			switch {
			case p.Enabled(t):
				kept = append(kept, d)
			case d.Required:
				return fmt.Errorf("service %s: depends_on: %s, which is in profile %s, not active; add --profile %s", s.Name, t.Name, strings.Join(t.Profiles, " or "), t.Profiles[0])
			}
		}
		s.DependsOn = kept
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Enabled says whether a service is in this run: it names no profile, or
// one that is active.
func (p *Project) Enabled(s *Service) bool {
	if len(s.Profiles) == 0 {
		return true
	}
	for _, a := range p.Profiles {
		if a == "*" {
			return true
		}
		for _, n := range s.Profiles {
			if n == a {
				return true
			}
		}
	}
	return false
}

// EnabledServices are the services up renders and starts, in file order.
func (p *Project) EnabledServices() []*Service {
	var out []*Service
	for _, s := range p.Services {
		if p.Enabled(s) {
			out = append(out, s)
		}
	}
	return out
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
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, quoteHint(err, data)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	if err := interpolateTree(&doc, vars); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	top, err := mapping(root, "top level")
	if err != nil {
		return nil, err
	}
	top = withoutExtensions(top)
	// No version key yet. The schema's promise (README): a key that has to
	// change its meaning brings an optional version: with it, and a file
	// without one keeps today's meaning. A habitual compose version: line
	// gets a pointed message.
	if vn := top.get("version"); vn != nil {
		return nil, fmt.Errorf("line %d: version: there is no version key; a file without one keeps meaning what it means today (drop the line)", vn.Line)
	}
	if err := unknownKeys(top, "top level", "name", "resources", "services"); err != nil {
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
	seen := map[string]bool{}
	for _, kv := range services.pairs {
		name := kv.key.Value
		if !reServiceName.MatchString(name) {
			return nil, fmt.Errorf("line %d: service %q: a service name may only contain letters, digits, _ and -", kv.key.Line, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("line %d: service %q declared twice", kv.key.Line, name)
		}
		seen[name] = true
		svc, err := parseService(name, kv.value, p)
		if err != nil {
			return nil, err
		}
		p.Services = append(p.Services, svc)
	}
	if err := validateGraph(p); err != nil {
		return nil, err
	}
	return p, nil
}

// quoteHint explains yaml's "cannot start any token" when the line holds a
// value that starts with %: yaml reserves % at the start of a plain value,
// and a specifier-led path is the likeliest way to write one.
func quoteHint(err error, data []byte) error {
	var line int
	if _, scanErr := fmt.Sscanf(err.Error(), "yaml: line %d:", &line); scanErr != nil || !strings.Contains(err.Error(), "cannot start any token") {
		return err
	}
	lines := strings.Split(string(data), "\n")
	if line < 1 || line > len(lines) {
		return err
	}
	if l := lines[line-1]; strings.Contains(l, ": %") || strings.Contains(l, "- %") {
		return fmt.Errorf("%v (a value that starts with %% must be quoted in yaml: \"%%h/...\")", err)
	}
	return err
}

// projectName is the one rule for a project name, wherever it comes from.
func projectName(s, where string) error {
	if !reProjectName.MatchString(s) {
		return fmt.Errorf("%s %q: a project name may only contain letters, digits and _ (no dash: - is systemd's slice separator and makes <project>-<service> ambiguous)", where, s)
	}
	return nil
}

// literalPath is the one rule for a path the tool reads itself (working_dir,
// env_file, build: creates): a % there would never mean what a specifier
// means; for the first two, systemd would also expand it.
func literalPath(s, key, ctx string, line int) error {
	if strings.Contains(s, "%") {
		return fmt.Errorf("line %d: %s: %s: no %% here: the tool reads this path itself (to check it, hash it or build in it), so it must be literal", line, ctx, key)
	}
	return nil
}

// specifiers is the one rule for % in a value systemd expands: a specifier
// of the 248 floor passes through for systemd to expand, %% is a literal
// percent, and anything else is refused here, because systemd drops an
// Environment= or ListenStream= line with a bad specifier and says so only
// in its log; systemd-analyze verify says nothing.
func specifiers(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+1 == len(s) {
			return fmt.Errorf("%q ends in a %% that starts no specifier; write %%%% for a literal percent", s)
		}
		i++
		switch c := s[i]; {
		case c == '%' || strings.IndexByte("aAbBCEfgGhHiIjJlLmMnNopPsStTuUvVwW", c) >= 0:
		case strings.IndexByte("dqyY", c) >= 0:
			return fmt.Errorf("%%%c in %q needs systemd 251; this tool supports 248 and later", c, s)
		case strings.IndexByte("crR", c) >= 0:
			return fmt.Errorf("%%%c in %q is a deprecated systemd specifier", c, s)
		default:
			return fmt.Errorf("%%%c in %q is not a systemd specifier; write %%%% for a literal percent", c, s)
		}
	}
	return nil
}

// usesSpecifier reports a % that is not the literal %%.
func usesSpecifier(s string) bool {
	return strings.Contains(strings.ReplaceAll(s, "%%", ""), "%")
}

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

var serviceKeys = []string{"command", "working_dir", "environment", "env_file", "restart",
	"depends_on", "schedule", "unit", "on_change", "healthcheck", "oneshot", "build", "resources", "listen", "entrypoint", "profiles"}

func parseService(name string, node *yaml.Node, p *Project) (*Service, error) {
	ctx := "service " + name
	m, err := mapping(node, ctx)
	if err != nil {
		return nil, err
	}
	m = withoutExtensions(m)
	if err := unknownKeys(m, ctx, serviceKeys...); err != nil {
		return nil, err
	}
	svc := &Service{Name: name, WorkingDir: p.Dir, OnChange: "restart"}

	// The raw form first: `unit: Service: ExecStart:` replaces `command:`
	// for the cases the refusals below cannot spell (a shell, a specifier,
	// a path with a space). Then it is the author's ExecStart, verified by
	// systemd-analyze but not resolved or refused here.
	if n := m.get("unit"); n != nil {
		sched, listen := m.get("schedule") != nil, m.get("listen") != nil
		if svc.Unit, err = parsePassThrough(n, ctx, sched, listen); err != nil {
			return nil, err
		}
	}
	rawExec := svc.Unit.hasKey("Service", "ExecStart")
	progLine := 0 // where the program word is written: entrypoint's, else command's
	for _, key := range []string{"entrypoint", "command"} {
		n := m.get(key)
		if n == nil {
			continue
		}
		if rawExec {
			return nil, fmt.Errorf("line %d: %s: %s: and unit: Service: ExecStart: both write ExecStart=; keep one", n.Line, ctx, key)
		}
		w, err := parseWords(n, ctx+": "+key)
		if err != nil {
			return nil, err
		}
		if progLine == 0 {
			progLine = n.Line
		}
		if key == "command" {
			svc.Command = w
		} else {
			svc.Entrypoint = w
		}
	}
	prog := svc.Entrypoint
	if prog.Empty() {
		prog = svc.Command
	}
	if prog.Empty() && !rawExec {
		return nil, fmt.Errorf("line %d: %s: command: is required (or entrypoint:, or unit: Service: ExecStart: for the raw form)", node.Line, ctx)
	}
	if !prog.Empty() {
		if err := refuseProgram(prog.program(), ctx, progLine); err != nil {
			return nil, err
		}
	}

	if n := m.get("working_dir"); n != nil {
		s, err := scalar(n, ctx+": working_dir")
		if err != nil {
			return nil, err
		}
		if err := literalPath(s, "working_dir", ctx, n.Line); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(p.Dir, s)
		}
		svc.WorkingDir = filepath.Clean(s)
	}

	if n := m.get("environment"); n != nil {
		if svc.Environment, err = parseEnvironment(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("env_file"); n != nil {
		if svc.EnvFiles, err = parseEnvFiles(n, ctx, p.Dir); err != nil {
			return nil, err
		}
	}
	if n := m.get("restart"); n != nil {
		if svc.Restart, err = parseRestart(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("depends_on"); n != nil {
		if svc.DependsOn, err = parseDependsOn(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("schedule"); n != nil {
		if svc.Schedule, err = parseSchedule(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("on_change"); n != nil {
		s, err := scalar(n, ctx+": on_change")
		if err != nil {
			return nil, err
		}
		if s != "restart" && s != "start-only" {
			return nil, fmt.Errorf("line %d: %s: on_change: %q; use restart or start-only", n.Line, ctx, s)
		}
		svc.OnChange = s
	}
	if n := m.get("healthcheck"); n != nil {
		if svc.Healthcheck, err = parseHealthcheck(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("oneshot"); n != nil {
		if err := n.Decode(&svc.Oneshot); err != nil {
			return nil, fmt.Errorf("line %d: %s: oneshot: %v", n.Line, ctx, err)
		}
	}
	if n := m.get("build"); n != nil {
		if svc.Build, err = parseBuild(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("resources"); n != nil {
		if svc.Resources, err = parseResources(n, ctx+": resources"); err != nil {
			return nil, err
		}
	}
	if n := m.get("listen"); n != nil {
		if svc.Listen, err = parseListen(n, ctx, p.Dir); err != nil {
			return nil, err
		}
	}
	if n := m.get("profiles"); n != nil {
		if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
			return nil, fmt.Errorf("line %d: %s: profiles: is a non-empty list of names", n.Line, ctx)
		}
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": profiles")
			if err != nil {
				return nil, err
			}
			if !reProfile.MatchString(s) {
				return nil, fmt.Errorf("line %d: %s: profiles: %q: a profile name is letters, digits, _ . and -, starting with a letter or digit", item.Line, ctx, s)
			}
			svc.Profiles = append(svc.Profiles, s)
		}
	}

	// Cross-key rules within one service.
	if svc.OnChange == "start-only" {
		for _, d := range svc.DependsOn {
			if d.Restart {
				return nil, fmt.Errorf("line %d: %s: on_change: start-only contradicts depends_on: %s: restart: true (PartOf= would restart it anyway); drop one", node.Line, ctx, d.Service)
			}
		}
	}
	if svc.Schedule != nil && svc.Restart != nil && svc.Restart.Policy == "always" {
		return nil, fmt.Errorf("line %d: %s: schedule: with restart: %s would restart a finished job forever; drop one", node.Line, ctx, svc.Restart.Written)
	}
	if svc.Build != nil { // after every key: creates: needs the final working_dir
		if c := svc.Build.Creates; c != "" && !filepath.IsAbs(c) {
			svc.Build.Creates = filepath.Join(svc.WorkingDir, c)
		}
		for _, kv := range svc.Environment {
			if usesSpecifier(kv.Value) {
				return nil, fmt.Errorf("line %d: %s: build: runs in the service's environment through systemd-run, which does not expand specifiers, and environment: %s uses one", node.Line, ctx, kv.Key)
			}
		}
	}
	if svc.Schedule != nil && svc.Healthcheck != nil {
		return nil, fmt.Errorf("line %d: %s: healthcheck: on a scheduled job has nothing to gate; drop one", node.Line, ctx)
	}
	if len(svc.Listen) > 0 && (svc.Schedule != nil || svc.Oneshot) {
		return nil, fmt.Errorf("line %d: %s: listen: is for a long-running service that accepts its sockets; a scheduled job or a oneshot has none to accept", node.Line, ctx)
	}
	return svc, nil
}

// parseListen reads listen:, one address or a list, each a ListenStream=
// line of the service's socket: a port, host:port, [v6]:port, @abstract or
// a path. A relative path is relative to the yaml; systemd-analyze verify
// judges the rest, so there is no second parser of addresses here.
func parseListen(n *yaml.Node, ctx, dir string) ([]string, error) {
	var items []*yaml.Node
	switch n.Kind {
	case yaml.ScalarNode:
		items = []*yaml.Node{n}
	case yaml.SequenceNode:
		items = n.Content
	default:
		return nil, fmt.Errorf("line %d: %s: listen: is an address or a list of addresses", n.Line, ctx)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("line %d: %s: listen: is empty", n.Line, ctx)
	}
	var out []string
	for _, item := range items {
		a, err := scalar(item, ctx+": listen")
		if err != nil {
			return nil, err
		}
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, fmt.Errorf("line %d: %s: listen: empty address", item.Line, ctx)
		}
		if err := specifiers(a); err != nil {
			return nil, fmt.Errorf("line %d: %s: listen: %v", item.Line, ctx, err)
		}
		if strings.Contains(a, "/") && !strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "%") && !strings.HasPrefix(a, "@") {
			a = filepath.Join(dir, a)
		}
		out = append(out, a)
	}
	return out, nil
}

// refuseCommand: systemd parses the command itself, so the characters that
// change its meaning are refused rather than
// escaped. Raw ExecStart= through the pass-through is the opt-in.
func refuseCommand(cmd, ctx string, line int) error {
	first, _, err := firstWord(cmd)
	if err != nil {
		return fmt.Errorf("line %d: %s: %v", line, ctx, err)
	}
	if err := refuseProgram(first, ctx, line); err != nil {
		return err
	}
	return refuseLine(cmd, ctx, line)
}

// refuseProgram checks the word that names what runs: systemd reads a
// leading - @ : + ! as a prefix, not as part of the path.
func refuseProgram(first, ctx string, line int) error {
	if first == "" {
		return fmt.Errorf("line %d: %s: no command word", line, ctx)
	}
	if strings.ContainsAny(first[:1], "-@:+!") {
		return fmt.Errorf("line %d: %s: first word %q starts with a character systemd treats as a prefix (- @ : + !); rename the program or use unit: Service: ExecStart:", line, ctx, first)
	}
	return nil
}

// refuseLine checks a string command wherever it stands.
func refuseLine(cmd, ctx string, line int) error {
	for _, w := range strings.Fields(cmd) {
		if w == ";" {
			return fmt.Errorf("line %d: %s: a bare ; separates ExecStart commands in systemd; one command per service, or use unit: Service: ExecStart:", line, ctx)
		}
	}
	if err := specifiers(cmd); err != nil {
		return fmt.Errorf("line %d: %s: %v", line, ctx, err)
	}
	return nil
}

// parseWords reads command: or entrypoint:, a string or a list of words.
func parseWords(n *yaml.Node, ctx string) (Words, error) {
	if n.Kind == yaml.SequenceNode {
		if len(n.Content) == 0 {
			return Words{}, fmt.Errorf("line %d: %s: is empty", n.Line, ctx)
		}
		var w Words
		for _, item := range n.Content {
			s, err := scalar(item, ctx)
			if err != nil {
				return Words{}, err
			}
			if err := specifiers(s); err != nil {
				return Words{}, fmt.Errorf("line %d: %s: %v", item.Line, ctx, err)
			}
			w.List = append(w.List, s)
		}
		return w, nil
	}
	s, err := scalar(n, ctx)
	if err != nil {
		return Words{}, err
	}
	if strings.TrimSpace(s) == "" {
		return Words{}, fmt.Errorf("line %d: %s: is empty", n.Line, ctx)
	}
	if _, _, err := firstWord(s); err != nil {
		return Words{}, fmt.Errorf("line %d: %s: %v", n.Line, ctx, err)
	}
	if err := refuseLine(s, ctx, n.Line); err != nil {
		return Words{}, err
	}
	return Words{Line: s}, nil
}

// firstWord returns the first word of a command under systemd's quoting:
// a leading "..." or '...' is one word (quoted=true), otherwise up to
// whitespace. An unclosed quote is an error, never a guess.
func firstWord(cmd string) (word string, quoted bool, err error) {
	cmd = strings.TrimLeft(cmd, " \t")
	if cmd == "" {
		return "", false, nil
	}
	if q := cmd[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(cmd[1:], q)
		if end < 0 {
			return "", true, fmt.Errorf("unclosed %c quote in %q", q, cmd)
		}
		return cmd[1 : end+1], true, nil
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i], false, nil
	}
	return cmd, false, nil
}

func parseEnvironment(n *yaml.Node, ctx string) ([]KV, error) {
	var out []KV
	add := func(k, v string, line int) error {
		if !isVarName(k) { // the same rule interpolation uses for ${KEY}
			return fmt.Errorf("line %d: %s: environment: %q is not a valid variable name", line, ctx, k)
		}
		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("line %d: %s: environment: %s contains a newline", line, ctx, k)
		}
		if err := specifiers(v); err != nil {
			return fmt.Errorf("line %d: %s: environment: %s: %v", line, ctx, k, err)
		}
		out = append(out, KV{k, v})
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		m, _ := mapping(n, ctx+": environment")
		for _, kv := range m.pairs {
			v, err := scalar(kv.value, ctx+": environment: "+kv.key.Value)
			if err != nil {
				return nil, err
			}
			if err := add(kv.key.Value, v, kv.key.Line); err != nil {
				return nil, err
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": environment")
			if err != nil {
				return nil, err
			}
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				return nil, fmt.Errorf("line %d: %s: environment: %q has no value; a bare name would capture the shell's variable, which is refused (reserved)", item.Line, ctx, s)
			}
			if err := add(k, v, item.Line); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: environment: must be a map or a list of KEY=value", n.Line, ctx)
	}
	return out, nil
}

func parseEnvFiles(n *yaml.Node, ctx, dir string) ([]EnvFile, error) {
	one := func(item *yaml.Node) (EnvFile, error) {
		ef := EnvFile{Required: true}
		switch item.Kind {
		case yaml.ScalarNode:
			ef.Path = item.Value
		case yaml.MappingNode:
			m, err := mapping(item, ctx+": env_file")
			if err != nil {
				return ef, err
			}
			if err := unknownKeys(m, ctx+": env_file", "path", "required"); err != nil {
				return ef, err
			}
			pn := m.get("path")
			if pn == nil {
				return ef, fmt.Errorf("line %d: %s: env_file: path: is required", item.Line, ctx)
			}
			if ef.Path, err = scalar(pn, ctx+": env_file: path"); err != nil {
				return ef, err
			}
			if rn := m.get("required"); rn != nil {
				if err := rn.Decode(&ef.Required); err != nil {
					return ef, fmt.Errorf("line %d: %s: env_file: required: %v", rn.Line, ctx, err)
				}
			}
		default:
			return ef, fmt.Errorf("line %d: %s: env_file: entries are a path or {path, required}", item.Line, ctx)
		}
		if ef.Path == "" {
			return ef, fmt.Errorf("line %d: %s: env_file: empty path", item.Line, ctx)
		}
		if err := literalPath(ef.Path, "env_file", ctx, item.Line); err != nil {
			return ef, err
		}
		if !filepath.IsAbs(ef.Path) {
			ef.Path = filepath.Join(dir, ef.Path)
		}
		ef.Path = filepath.Clean(ef.Path)
		return ef, nil
	}
	var out []EnvFile
	if n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			ef, err := one(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ef)
		}
		return out, nil
	}
	ef, err := one(n)
	if err != nil {
		return nil, err
	}
	return []EnvFile{ef}, nil
}

func parseRestart(n *yaml.Node, ctx string) (*Restart, error) {
	r := &Restart{}
	switch n.Kind {
	case yaml.ScalarNode:
		r.Policy = n.Value
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": restart")
		if err != nil {
			return nil, err
		}
		if err := unknownKeys(m, ctx+": restart", "policy", "delay"); err != nil {
			return nil, err
		}
		pn := m.get("policy")
		if pn == nil {
			return nil, fmt.Errorf("line %d: %s: restart: policy: is required in the map form", n.Line, ctx)
		}
		if r.Policy, err = scalar(pn, ctx+": restart: policy"); err != nil {
			return nil, err
		}
		if dn := m.get("delay"); dn != nil {
			if r.Delay, err = duration(dn, ctx+": restart: delay"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: restart: is a policy or {policy, delay}", n.Line, ctx)
	}
	r.Written = r.Policy
	switch {
	case r.Policy == "no" || r.Policy == "on-failure" || r.Policy == "always":
	case r.Policy == "unless-stopped":
		// compose's habit, and systemd's always already is it: a unit
		// stopped by hand is never restarted. (At boot the target starts it
		// again, where docker would leave it stopped.)
		r.Policy = "always"
	case strings.HasPrefix(r.Policy, "on-failure:"):
		return nil, fmt.Errorf("line %d: %s: restart: %q has no faithful systemd form: docker counts automatic restarts and forgets them on a manual start, systemd's start limit counts every start, manual ones included; write restart: on-failure and set unit: Unit: StartLimitBurst: and StartLimitIntervalSec: yourself", n.Line, ctx, r.Policy)
	default:
		return nil, fmt.Errorf("line %d: %s: restart: %q; use no, on-failure, always or unless-stopped", n.Line, ctx, r.Policy)
	}
	return r, nil
}

func parseDependsOn(n *yaml.Node, ctx string) ([]Dependency, error) {
	var out []Dependency
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": depends_on")
			if err != nil {
				return nil, err
			}
			out = append(out, Dependency{Service: s, Condition: "service_started"})
		}
	case yaml.MappingNode:
		m, _ := mapping(n, ctx+": depends_on")
		for _, kv := range m.pairs {
			d := Dependency{Service: kv.key.Value, Condition: "service_started"}
			if kv.value.Kind == yaml.MappingNode {
				dm, err := mapping(kv.value, ctx+": depends_on: "+d.Service)
				if err != nil {
					return nil, err
				}
				if err := unknownKeys(dm, ctx+": depends_on: "+d.Service, "condition", "required", "restart"); err != nil {
					return nil, err
				}
				if cn := dm.get("condition"); cn != nil {
					if d.Condition, err = scalar(cn, ctx+": depends_on: condition"); err != nil {
						return nil, err
					}
				}
				explicitRequired := false
				if rn := dm.get("required"); rn != nil {
					if err := rn.Decode(&d.Required); err != nil {
						return nil, fmt.Errorf("line %d: %s: depends_on: required: %v", rn.Line, ctx, err)
					}
					explicitRequired = true
				}
				if d.Condition != "service_started" && d.Condition != "" {
					if explicitRequired && !d.Required {
						return nil, fmt.Errorf("line %d: %s: depends_on: %s: condition %s with required: false would not enforce the condition (a Wants= dependency starts even when %s fails); drop required:", kv.key.Line, ctx, d.Service, d.Condition, d.Service)
					}
					d.Required = true
				}
				if rn := dm.get("restart"); rn != nil {
					if err := rn.Decode(&d.Restart); err != nil {
						return nil, fmt.Errorf("line %d: %s: depends_on: restart: %v", rn.Line, ctx, err)
					}
				}
			} else if !(kv.value.Kind == yaml.ScalarNode && kv.value.Tag == "!!null") {
				return nil, fmt.Errorf("line %d: %s: depends_on: %s: is a map of {condition, required, restart} or empty", kv.value.Line, ctx, d.Service)
			}
			switch d.Condition {
			case "service_started", "service_healthy", "service_completed_successfully":
			default:
				return nil, fmt.Errorf("line %d: %s: depends_on: %s: condition %q; use service_started, service_healthy or service_completed_successfully", kv.key.Line, ctx, d.Service, d.Condition)
			}
			out = append(out, d)
		}
	default:
		return nil, fmt.Errorf("line %d: %s: depends_on: is a list of services or a map", n.Line, ctx)
	}
	return out, nil
}

func parseSchedule(n *yaml.Node, ctx string) (*Schedule, error) {
	s := &Schedule{Accuracy: "10s", Persistent: true}
	switch n.Kind {
	case yaml.ScalarNode:
		s.Calendar = n.Value
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": schedule")
		if err != nil {
			return nil, err
		}
		if err := unknownKeys(m, ctx+": schedule", "calendar", "accuracy", "persistent", "randomized_delay"); err != nil {
			return nil, err
		}
		cn := m.get("calendar")
		if cn == nil {
			return nil, fmt.Errorf("line %d: %s: schedule: calendar: is required in the map form", n.Line, ctx)
		}
		if s.Calendar, err = scalar(cn, ctx+": schedule: calendar"); err != nil {
			return nil, err
		}
		if an := m.get("accuracy"); an != nil {
			if s.Accuracy, err = duration(an, ctx+": schedule: accuracy"); err != nil {
				return nil, err
			}
		}
		if pn := m.get("persistent"); pn != nil {
			if err := pn.Decode(&s.Persistent); err != nil {
				return nil, fmt.Errorf("line %d: %s: schedule: persistent: %v", pn.Line, ctx, err)
			}
		}
		if rn := m.get("randomized_delay"); rn != nil {
			if s.RandomizedDelay, err = duration(rn, ctx+": schedule: randomized_delay"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: schedule: is an OnCalendar string or {calendar, accuracy, persistent, randomized_delay}", n.Line, ctx)
	}
	if strings.TrimSpace(s.Calendar) == "" {
		return nil, fmt.Errorf("line %d: %s: schedule: calendar is empty", n.Line, ctx)
	}
	if err := specifiers(s.Calendar); err != nil {
		return nil, fmt.Errorf("line %d: %s: schedule: %v", n.Line, ctx, err)
	}
	return s, nil
}

func parseHealthcheck(n *yaml.Node, ctx string) (*Healthcheck, error) {
	h := &Healthcheck{Interval: "2s", Timeout: "5s", StartPeriod: "60s"}
	m, err := mapping(n, ctx+": healthcheck")
	if err != nil {
		return nil, err
	}
	if err := unknownKeys(m, ctx+": healthcheck", "test", "interval", "timeout", "start_period"); err != nil {
		return nil, err
	}
	tn := m.get("test")
	if tn == nil || tn.Kind != yaml.SequenceNode || len(tn.Content) == 0 {
		return nil, fmt.Errorf("line %d: %s: healthcheck: test: is a non-empty list of words (argv)", n.Line, ctx)
	}
	for _, item := range tn.Content {
		s, err := scalar(item, ctx+": healthcheck: test")
		if err != nil {
			return nil, err
		}
		h.Test = append(h.Test, s)
	}
	if err := refuseCommand(strings.Join(h.Test, " "), ctx+": healthcheck: test", tn.Line); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"interval", &h.Interval}, {"timeout", &h.Timeout}, {"start_period", &h.StartPeriod}} {
		if dn := m.get(f.key); dn != nil {
			if *f.dst, err = duration(dn, ctx+": healthcheck: "+f.key); err != nil {
				return nil, err
			}
		}
	}
	return h, nil
}

func parseBuild(n *yaml.Node, ctx string) (*Build, error) {
	b := &Build{}
	m, err := mapping(n, ctx+": build")
	if err != nil {
		return nil, err
	}
	if err := unknownKeys(m, ctx+": build", "run", "creates"); err != nil {
		return nil, err
	}
	rn := m.get("run")
	if rn == nil || rn.Kind != yaml.SequenceNode || len(rn.Content) == 0 {
		return nil, fmt.Errorf("line %d: %s: build: run: is a non-empty list of commands", n.Line, ctx)
	}
	for _, item := range rn.Content {
		s, err := scalar(item, ctx+": build: run")
		if err != nil {
			return nil, err
		}
		if err := refuseCommand(s, ctx+": build: run", item.Line); err != nil {
			return nil, err
		}
		if strings.Contains(s, "%") {
			return nil, fmt.Errorf("line %d: %s: build: run: no %% here: build steps run through systemd-run, which does not expand specifiers", item.Line, ctx)
		}
		b.Run = append(b.Run, s)
	}
	if cn := m.get("creates"); cn != nil {
		if b.Creates, err = scalar(cn, ctx+": build: creates"); err != nil {
			return nil, err
		}
		if err := literalPath(b.Creates, "build: creates", ctx, cn.Line); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func parseResources(n *yaml.Node, ctx string) (*Resources, error) {
	r := &Resources{}
	m, err := mapping(n, ctx)
	if err != nil {
		return nil, err
	}
	if err := unknownKeys(m, ctx, "memory", "cpus", "pids"); err != nil {
		return nil, err
	}
	if mn := m.get("memory"); mn != nil {
		s, err := scalar(mn, ctx+": memory")
		if err != nil {
			return nil, err
		}
		if !reMemory.MatchString(s) {
			return nil, fmt.Errorf("line %d: %s: memory: %q; use bytes with an optional K, M, G or T suffix, e.g. 512M", mn.Line, ctx, s)
		}
		r.Memory = s
	}
	if cn := m.get("cpus"); cn != nil {
		s, err := scalar(cn, ctx+": cpus")
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 {
			return nil, fmt.Errorf("line %d: %s: cpus: %q; use a positive number of CPUs, e.g. 0.5 or 2", cn.Line, ctx, s)
		}
		r.CPUs = f
	}
	if pn := m.get("pids"); pn != nil {
		s, err := scalar(pn, ctx+": pids")
		if err != nil {
			return nil, err
		}
		i, err := strconv.Atoi(s)
		if err != nil || i <= 0 {
			return nil, fmt.Errorf("line %d: %s: pids: %q; use a positive integer", pn.Line, ctx, s)
		}
		r.PIDs = i
	}
	if r.Memory == "" && r.CPUs == 0 && r.PIDs == 0 {
		return nil, fmt.Errorf("line %d: %s: is empty; set memory, cpus or pids", n.Line, ctx)
	}
	return r, nil
}

// parsePassThrough reads `unit:`: section -> key -> value or list of values.
// Sections are limited to what a service can carry; [Install] is refused
// because boot enablement belongs to the project target alone, and io
// controllers are refused because the user manager is not delegated them.
func parsePassThrough(n *yaml.Node, ctx string, scheduled, listens bool) (PassThrough, error) {
	var pt PassThrough
	m, err := mapping(n, ctx+": unit")
	if err != nil {
		return pt, err
	}
	for _, sec := range m.pairs {
		sname := sec.key.Value
		switch sname {
		case "Unit", "Service":
		case "Timer":
			if !scheduled {
				return pt, fmt.Errorf("line %d: %s: unit: [Timer] only applies to a service with schedule:", sec.key.Line, ctx)
			}
		case "Socket":
			if !listens {
				return pt, fmt.Errorf("line %d: %s: unit: [Socket] only applies to a service with listen:", sec.key.Line, ctx)
			}
		case "Install":
			return pt, fmt.Errorf("line %d: %s: unit: [Install] is refused: boot enablement is declared once, on the project target; services are pulled in by it", sec.key.Line, ctx)
		default:
			return pt, fmt.Errorf("line %d: %s: unit: section %q; a service carries Unit, Service, Timer (with schedule:) and Socket (with listen:)", sec.key.Line, ctx, sname)
		}
		km, err := mapping(sec.value, ctx+": unit: "+sname)
		if err != nil {
			return pt, err
		}
		ps := PassSection{Name: sname}
		for _, kv := range km.pairs {
			key := kv.key.Value
			if sname == "Service" && strings.HasPrefix(key, "IO") {
				return pt, fmt.Errorf("line %d: %s: unit: Service: %s: the io cgroup controller is not delegated to the user manager, so this directive would render fine and do nothing", kv.key.Line, ctx, key)
			}
			if !reDirective.MatchString(key) {
				return pt, fmt.Errorf("line %d: %s: unit: %s: %q is not a directive name", kv.key.Line, ctx, sname, key)
			}
			pk := PassKey{Name: key, Line: kv.key.Line}
			switch kv.value.Kind {
			case yaml.ScalarNode:
				pk.Values = []string{kv.value.Value}
			case yaml.SequenceNode:
				for _, item := range kv.value.Content {
					s, err := scalar(item, ctx+": unit: "+sname+": "+key)
					if err != nil {
						return pt, err
					}
					pk.Values = append(pk.Values, s)
				}
			default:
				return pt, fmt.Errorf("line %d: %s: unit: %s: %s: a value or a list of values", kv.value.Line, ctx, sname, key)
			}
			ps.Keys = append(ps.Keys, pk)
		}
		pt.Sections = append(pt.Sections, ps)
	}
	return pt, nil
}

// validateGraph checks depends_on against the declared services.
func validateGraph(p *Project) error {
	byName := map[string]*Service{}
	for _, s := range p.Services {
		byName[s.Name] = s
	}
	for _, s := range p.Services {
		for _, d := range s.DependsOn {
			t, ok := byName[d.Service]
			if !ok {
				return fmt.Errorf("service %s: depends_on: %q is not a service in this file (a dependency on an outside unit goes through unit: Unit: After:)", s.Name, d.Service)
			}
			if t == s {
				return fmt.Errorf("service %s: depends_on: itself", s.Name)
			}
			if t.Schedule != nil {
				return fmt.Errorf("service %s: depends_on: %s runs on its timer, not on a dependent's start; a scheduled job cannot be a dependency", s.Name, d.Service)
			}
			switch d.Condition {
			case "service_healthy":
				if t.Healthcheck == nil && !t.Unit.has("Service", "Type", "notify") {
					return fmt.Errorf("service %s: depends_on: %s: condition service_healthy, but %s has no healthcheck: (or Type=notify); it would be service_started in disguise", s.Name, d.Service, d.Service)
				}
			case "service_completed_successfully":
				if !t.Oneshot {
					return fmt.Errorf("service %s: depends_on: %s: condition service_completed_successfully needs %s to declare oneshot: true", s.Name, d.Service, d.Service)
				}
			}
		}
	}
	// Cycles: a plain DFS over depends_on.
	state := map[string]int{}
	var visit func(s *Service, path []string) error
	visit = func(s *Service, path []string) error {
		switch state[s.Name] {
		case 1:
			return fmt.Errorf("depends_on cycle: %s -> %s", strings.Join(path, " -> "), s.Name)
		case 2:
			return nil
		}
		state[s.Name] = 1
		for _, d := range s.DependsOn {
			if err := visit(byName[d.Service], append(path, s.Name)); err != nil {
				return err
			}
		}
		state[s.Name] = 2
		return nil
	}
	for _, s := range p.Services {
		if err := visit(s, nil); err != nil {
			return err
		}
	}
	return nil
}

func (pt PassThrough) hasKey(section, key string) bool {
	for _, s := range pt.Sections {
		if s.Name != section {
			continue
		}
		for _, k := range s.Keys {
			if k.Name == key {
				return true
			}
		}
	}
	return false
}

func (pt PassThrough) has(section, key, value string) bool {
	for _, s := range pt.Sections {
		if s.Name != section {
			continue
		}
		for _, k := range s.Keys {
			if k.Name == key {
				for _, v := range k.Values {
					if v == value {
						return true
					}
				}
			}
		}
	}
	return false
}

// ---- yaml node helpers ----------------------------------------------------

type kvNode struct{ key, value *yaml.Node }

type mapNode struct {
	pairs []kvNode
	line  int
}

func (m *mapNode) get(key string) *yaml.Node {
	for _, kv := range m.pairs {
		if kv.key.Value == key {
			return kv.value
		}
	}
	return nil
}

func mapping(n *yaml.Node, ctx string) (*mapNode, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: %s: expected a map", n.Line, ctx)
	}
	m := &mapNode{line: n.Line}
	seen := map[string]int{}
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: %s: keys must be plain words", k.Line, ctx)
		}
		if k.Tag == "!!merge" {
			merges = append(merges, n.Content[i+1])
			continue
		}
		if first, dup := seen[k.Value]; dup {
			return nil, fmt.Errorf("line %d: %s: key %q already given on line %d; a second one would silently win or lose", k.Line, ctx, k.Value, first)
		}
		seen[k.Value] = k.Line
		m.pairs = append(m.pairs, kvNode{k, n.Content[i+1]})
	}
	// yaml merge keys, compose's way to reuse a block: `<<: *base` or
	// `<<: [*a, *b]` add the keys this map does not give itself, an earlier
	// source winning over a later one.
	for _, v := range merges {
		if v.Kind == yaml.AliasNode {
			v = v.Alias
		}
		sources := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			sources = v.Content
		}
		for _, src := range sources {
			// The shape is checked here, so what mapping() refuses inside a
			// merged block is reported as itself: a duplicate key in an
			// anchor must not read as "<< merges a map".
			shape := src
			if shape.Kind == yaml.AliasNode && shape.Alias != nil {
				shape = shape.Alias
			}
			if shape.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("line %d: %s: << merges a map or a list of maps", src.Line, ctx)
			}
			sm, err := mapping(shape, ctx+": <<")
			if err != nil {
				return nil, err
			}
			for _, kv := range sm.pairs {
				if _, given := seen[kv.key.Value]; !given {
					seen[kv.key.Value] = kv.key.Line
					m.pairs = append(m.pairs, kv)
				}
			}
		}
	}
	return m, nil
}

// withoutExtensions drops compose's x- keys from a top-level or service map:
// they hold blocks for anchors to reuse and mean nothing themselves.
func withoutExtensions(m *mapNode) *mapNode {
	out := &mapNode{line: m.line}
	for _, kv := range m.pairs {
		if !strings.HasPrefix(kv.key.Value, "x-") {
			out.pairs = append(out.pairs, kv)
		}
	}
	return out
}

func unknownKeys(m *mapNode, ctx string, allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for _, kv := range m.pairs {
		if !ok[kv.key.Value] {
			sorted := append([]string(nil), allowed...)
			sort.Strings(sorted)
			return fmt.Errorf("line %d: %s: unknown key %q (known: %s)", kv.key.Line, ctx, kv.key.Value, strings.Join(sorted, ", "))
		}
	}
	return nil
}

func scalar(n *yaml.Node, ctx string) (string, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: %s: expected a single value", n.Line, ctx)
	}
	return n.Value, nil
}

func duration(n *yaml.Node, ctx string) (string, error) {
	s, err := scalar(n, ctx)
	if err != nil {
		return "", err
	}
	if _, err := seconds(s); err != nil {
		return "", fmt.Errorf("line %d: %s: %v", n.Line, ctx, err)
	}
	return s, nil
}
