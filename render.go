// render.go — the validated model becomes unit text.
//
// Every directive in a rendered unit has exactly one source. A translated
// key OWNS the directives it writes (a pass-through of the same directive is
// refused, naming both); a handful of directives are DEFAULTS the
// pass-through may override (Description, SyslogIdentifier, timer accuracy);
// systemd's repeatable directives CONCAT. The renderer never guesses at
// systemd's escaping: what it cannot spell safely it refuses.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MarkerSection = "X-SystemdCompose"

type Rendered struct {
	Name string // unit file name, e.g. proj-api.service
	Text string
}

type RenderOptions struct {
	Exe        string   // absolute path of this program, for the healthcheck probe
	SearchPath []string // where a bare command word is looked up
}

// DefaultRenderOptions resolves the program path and the search path once.
func DefaultRenderOptions() (RenderOptions, error) {
	exe, err := os.Executable()
	if err != nil {
		return RenderOptions{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return RenderOptions{}, err
	}
	var search []string
	if home, err := os.UserHomeDir(); err == nil {
		search = append(search, filepath.Join(home, ".local", "bin"))
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" {
			search = append(search, d)
		}
	}
	return RenderOptions{Exe: exe, SearchPath: search}, nil
}

// Unit names -----------------------------------------------------------------

func (p *Project) TargetName() string { return p.Name + ".target" }
func (p *Project) SliceName() string  { return p.Name + ".slice" }

// unitName is the one spelling of <project>-<service><suffix>.
func (p *Project) unitName(service, suffix string) string { return p.Name + "-" + service + suffix }

func (p *Project) ServiceUnit(s *Service) string { return p.unitName(s.Name, ".service") }
func (p *Project) TimerUnit(s *Service) string   { return p.unitName(s.Name, ".timer") }
func (p *Project) SocketUnit(s *Service) string  { return p.unitName(s.Name, ".socket") }

// UnitOf is the unit that stands for a service wherever one unit must: a
// scheduled job is represented by its timer (start/stop arm it, restart
// re-arms it), every other service by itself. The job's own service unit
// runs only when the timer fires.
func (p *Project) UnitOf(s *Service) string {
	if s.Schedule != nil {
		return p.TimerUnit(s)
	}
	return p.ServiceUnit(s)
}

// UnitsOf is every unit a service brings up: what the target wants, up
// starts and start/stop act on. A listening service is its socket and
// itself: started together, so up is eager as compose's is, and stopped
// together, since a socket left listening would start the service again at
// the next connection. restart takes UnitOf alone and keeps the socket.
func (p *Project) UnitsOf(s *Service) []string {
	if len(s.Listen) > 0 {
		return []string{p.SocketUnit(s), p.ServiceUnit(s)}
	}
	return []string{p.UnitOf(s)}
}

// UnitNames lists every unit this project renders, in render order: exactly
// the names Render emits. up and down take it as the declared set, so a
// unit rendered but missing here would be linked and retired in one up.
func (p *Project) UnitNames() []string {
	names := []string{p.SliceName()}
	for _, s := range p.Services {
		names = append(names, p.ServiceUnit(s))
	}
	for _, s := range p.Services {
		if s.Schedule != nil {
			names = append(names, p.TimerUnit(s))
		}
	}
	for _, s := range p.Services {
		if len(s.Listen) > 0 {
			names = append(names, p.SocketUnit(s))
		}
	}
	return append(names, p.TargetName())
}

// Render produces every unit file of the project.
func Render(p *Project, opt RenderOptions) ([]Rendered, error) {
	sl, err := renderSlice(p)
	if err != nil {
		return nil, err
	}
	out := []Rendered{sl}
	var timers, sockets []Rendered
	for _, s := range p.Services {
		svc, tmr, sock, err := renderService(p, s, opt)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", s.Name, err)
		}
		out = append(out, svc)
		if tmr != nil {
			timers = append(timers, *tmr)
		}
		if sock != nil {
			sockets = append(sockets, *sock)
		}
	}
	out = append(out, timers...)
	out = append(out, sockets...)
	tg, err := renderTarget(p)
	if err != nil {
		return nil, err
	}
	out = append(out, tg)
	return out, nil
}

// renderSlice renders the slice whether or not the project caps it. Every
// service names it, so it is always part of the project: rendered, it is
// registered and unregistered with the rest, and dropping resources: is a
// change to its text, never an orphan whose retirement stops every service.
func renderSlice(p *Project) (Rendered, error) {
	u := newUnit()
	u.setDefault("Unit", "Description", p.Name+" (systemd-compose project)")
	u.own("Unit", "SourcePath", p.ConfigPath, "")
	if p.Resources != nil {
		addResources(u, "Slice", p.Resources)
	}
	marker(u, p, "")
	text, err := u.text()
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{p.SliceName(), text}, nil
}

func renderTarget(p *Project) (Rendered, error) {
	u := newUnit()
	u.setDefault("Unit", "Description", p.Name+" (systemd-compose project)")
	u.own("Unit", "SourcePath", p.ConfigPath, "")
	for _, s := range p.Services {
		for _, n := range p.UnitsOf(s) { // for a scheduled job: the timer, never the job
			u.add("Unit", "Wants", n)
		}
	}
	u.own("Install", "WantedBy", "default.target", "")
	marker(u, p, "")
	text, err := u.text()
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{p.TargetName(), text}, nil
}

func renderService(p *Project, s *Service, opt RenderOptions) (svc Rendered, timer, socket *Rendered, err error) {
	u := newUnit()
	u.setDefault("Unit", "Description", p.Name+": "+s.Name)
	u.own("Unit", "SourcePath", p.ConfigPath, "")
	u.add("Unit", "PartOf", p.TargetName())
	if len(s.Listen) > 0 {
		// systemd's documented edge (and podman's): the socket comes along
		// whenever anything starts the service, a dependent included, and a
		// restart of the socket takes the service with it. Without
		// an edge: restarting a changed socket while the service runs is
		// refused ("already active") and leaves the socket down.
		u.add("Unit", "Requires", p.SocketUnit(s))
	}
	for _, d := range s.DependsOn {
		dep := p.unitName(d.Service, ".service")
		u.add("Unit", "After", dep)
		if d.Required {
			u.add("Unit", "Requires", dep)
		} else {
			u.add("Unit", "Wants", dep)
		}
		if d.Restart {
			u.add("Unit", "PartOf", dep)
		}
	}

	switch {
	case s.Schedule != nil:
		u.own("Service", "Type", "oneshot", "schedule")
	case s.Oneshot:
		u.own("Service", "Type", "oneshot", "oneshot")
		u.own("Service", "RemainAfterExit", "yes", "oneshot")
	}
	if st, err := os.Stat(s.WorkingDir); err != nil || !st.IsDir() {
		return Rendered{}, nil, nil, fmt.Errorf("working_dir %s: not a directory", s.WorkingDir)
	}
	u.own("Service", "WorkingDirectory", s.WorkingDir, "working_dir")
	for _, kv := range s.Environment {
		u.add("Service", "Environment", envAssignment(kv))
	}
	for _, ef := range s.EnvFiles {
		if ef.Required {
			if _, err := os.Stat(ef.Path); err != nil {
				return Rendered{}, nil, nil, fmt.Errorf("env_file %s: %v (mark it {path, required: false} if it may be absent)", ef.Path, err)
			}
			u.add("Service", "EnvironmentFile", ef.Path)
		} else {
			u.add("Service", "EnvironmentFile", "-"+ef.Path)
		}
	}
	if s.Command != "" {
		cmd, err := resolveCommand(s.Command, s.WorkingDir, opt.SearchPath)
		if err != nil {
			return Rendered{}, nil, nil, fmt.Errorf("command: %w", err)
		}
		u.own("Service", "ExecStart", execLiteral(cmd), "command")
	}
	if h := s.Healthcheck; h != nil {
		test, err := resolveArgv(h.Test, s.WorkingDir, opt.SearchPath)
		if err != nil {
			return Rendered{}, nil, nil, fmt.Errorf("healthcheck: test: %w", err)
		}
		if opt.Exe == "" {
			return Rendered{}, nil, nil, fmt.Errorf("healthcheck: the probe needs this program's path (RenderOptions.Exe)")
		}
		words := []string{opt.Exe, "probe", "--interval", h.Interval, "--timeout", h.Timeout, "--start-period", h.StartPeriod, "--"}
		words = append(words, test...)
		u.add("Service", "ExecStartPost", execLiteral(joinWords(words)))
		sp, err := seconds(h.StartPeriod)
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		to, err := seconds(h.Timeout)
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		u.own("Service", "TimeoutStartSec", strconv.Itoa(sp+to+5)+"s", "healthcheck")
	}
	if r := s.Restart; r != nil {
		u.own("Service", "Restart", r.Policy, "restart")
		if r.Delay != "" {
			u.own("Service", "RestartSec", r.Delay, "restart: delay")
		}
	}
	u.own("Service", "Slice", p.SliceName(), "")
	u.setDefault("Service", "SyslogIdentifier", p.Name+"-"+s.Name)
	if s.Resources != nil {
		addResources(u, "Service", s.Resources)
	}

	var t *unitFile
	if sc := s.Schedule; sc != nil {
		t = newUnit()
		t.setDefault("Unit", "Description", p.Name+": "+s.Name+" (timer)")
		t.own("Unit", "SourcePath", p.ConfigPath, "")
		t.add("Unit", "PartOf", p.TargetName())
		t.own("Timer", "OnCalendar", sc.Calendar, "schedule")
		t.setDefault("Timer", "AccuracySec", sc.Accuracy)
		t.own("Timer", "Persistent", yesno(sc.Persistent), "schedule: persistent")
		if sc.RandomizedDelay != "" {
			t.own("Timer", "RandomizedDelaySec", sc.RandomizedDelay, "schedule: randomized_delay")
		}
	}

	var k *unitFile
	if len(s.Listen) > 0 {
		k = newUnit()
		k.setDefault("Unit", "Description", p.Name+": "+s.Name+" (socket)")
		k.own("Unit", "SourcePath", p.ConfigPath, "")
		k.add("Unit", "PartOf", p.TargetName())
		for _, a := range s.Listen {
			k.add("Socket", "ListenStream", a)
		}
	}

	// Pass-through, last: Unit and Service into the service file, Timer and
	// Socket into theirs.
	for _, sec := range s.Unit.Sections {
		dst := u
		switch sec.Name {
		case "Timer":
			dst = t
		case "Socket":
			dst = k
		}
		for _, k := range sec.Keys {
			if err := dst.merge(sec.Name, k); err != nil {
				return Rendered{}, nil, nil, err
			}
		}
	}

	marker(u, p, s.Name)
	if len(s.EnvFiles) > 0 {
		u.own(MarkerSection, "RestartTriggers", envFileHash(s.EnvFiles), "")
	}
	text, err := u.text()
	if err != nil {
		return Rendered{}, nil, nil, err
	}
	svc = Rendered{p.ServiceUnit(s), text}
	if t != nil {
		marker(t, p, s.Name)
		ttext, err := t.text()
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		timer = &Rendered{p.TimerUnit(s), ttext}
	}
	if k != nil {
		marker(k, p, s.Name)
		ktext, err := k.text()
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		socket = &Rendered{p.SocketUnit(s), ktext}
	}
	return svc, timer, socket, nil
}

func addResources(u *unitFile, section string, r *Resources) {
	if r.Memory != "" {
		u.own(section, "MemoryMax", r.Memory, "resources: memory")
	}
	if r.CPUs > 0 {
		u.own(section, "CPUQuota", strconv.FormatFloat(r.CPUs*100, 'f', -1, 64)+"%", "resources: cpus")
	}
	if r.PIDs > 0 {
		u.own(section, "TasksMax", strconv.Itoa(r.PIDs), "resources: pids")
	}
}

func marker(u *unitFile, p *Project, service string) {
	u.own(MarkerSection, "Project", p.Name, "")
	if service != "" {
		u.own(MarkerSection, "Service", service, "")
	}
	u.own(MarkerSection, "Config", p.ConfigPath, "")
}

// envAssignment spells KEY=value for Environment=. The value's % are
// systemd's (checked by specifiers at load), and $ is literal there.
func envAssignment(kv KV) string {
	return quoteWord(kv.Key + "=" + kv.Value)
}

// envFileHash makes an edit to an env_file visible as a text change in the
// unit, so the ordinary differ restarts the service. Contents only: the paths
// already appear in EnvironmentFile= lines. A required file that is missing
// has already been refused; an optional one hashes as absent.
func envFileHash(files []EnvFile) string {
	h := sha256.New()
	for _, ef := range files {
		data, err := os.ReadFile(ef.Path)
		if err != nil {
			h.Write([]byte("absent\x00"))
			continue
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ---- command resolution ---------------------------------------------------

// resolveCommand replaces the first word of cmd with an absolute path found
// on the search path (or relative to workDir when it contains a slash) and
// refuses when nothing is found: the unit must not depend on the manager's
// PATH, which is not the shell's.
func resolveCommand(cmd, workDir string, search []string) (string, error) {
	first, quoted, err := firstWord(cmd)
	if err != nil {
		return "", err
	}
	abs, err := resolveWord(first, workDir, search)
	if err != nil {
		return "", err
	}
	rest := strings.TrimLeft(cmd, " \t")
	if quoted {
		rest = rest[len(first)+2:]
	} else {
		rest = rest[len(first):]
	}
	return abs + rest, nil
}

func resolveArgv(argv []string, workDir string, search []string) ([]string, error) {
	abs, err := resolveWord(argv[0], workDir, search)
	if err != nil {
		return nil, err
	}
	out := append([]string{abs}, argv[1:]...)
	return out, nil
}

func resolveWord(word, workDir string, search []string) (string, error) {
	if word == "" {
		return "", fmt.Errorf("empty command")
	}
	if strings.ContainsAny(word, " \t\"'\\") {
		return "", fmt.Errorf("%q: a program path with whitespace or quotes needs systemd quoting, which is refused here; use unit: Service: ExecStart:", word)
	}
	if strings.Contains(word, "%") {
		// systemd expands it at load; systemd-analyze verify --user, which
		// up runs on the render, refuses a path that does not exist then.
		return word, nil
	}
	if strings.Contains(word, "/") {
		p := word
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		p = filepath.Clean(p)
		if !executable(p) {
			return "", fmt.Errorf("%q: not an executable file (looked at %s)", word, p)
		}
		return p, nil
	}
	for _, dir := range search {
		p := filepath.Join(dir, word)
		if executable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%q: not found on the search path (~/.local/bin and your PATH); the unit will run under systemd's PATH, so name the program by its full path", word)
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// ---- systemd quoting ------------------------------------------------------

// quoteWord spells one word the way a unit file reads it back. Plain words
// pass through; anything else is double-quoted with C-style escapes.
func quoteWord(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\"';\\") {
		return w
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range w {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// execLiteral writes the $ that interpolation left (from $$) so systemd
// keeps it: an Exec line expands $VAR from the unit's environment at run
// time, and $$ is its literal dollar. Environment= needs no such care.
func execLiteral(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func joinWords(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = quoteWord(w)
	}
	return strings.Join(q, " ")
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// seconds parses a systemd time span ("5s", "2min", "1h 30min", "1h30min",
// "1.5s") into whole seconds, rounded up. It is the one grammar for time
// spans in this program; the schema validates by calling it.
func seconds(span string) (int, error) {
	bad := fmt.Errorf("%q is not a systemd time span (e.g. 5s, 2min, 1h 30min)", span)
	s := strings.ReplaceAll(span, " ", "")
	if s == "" {
		return 0, bad
	}
	total := 0.0
	for s != "" {
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, bad
		}
		n, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, bad
		}
		s = s[i:]
		j := 0
		for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			j++
		}
		unit := s[:j]
		s = s[j:]
		mult := 1.0
		switch unit {
		case "", "s", "sec", "second", "seconds":
		case "ms", "msec":
			mult = 0.001
		case "us", "usec":
			mult = 0.000001
		case "m", "min", "minute", "minutes":
			mult = 60
		case "h", "hr", "hour", "hours":
			mult = 3600
		case "d", "day", "days":
			mult = 86400
		case "w", "week", "weeks":
			mult = 604800
		default:
			return 0, bad
		}
		total += n * mult
	}
	if total != float64(int(total)) {
		return int(total) + 1, nil
	}
	return int(total), nil
}

// ---- the unit emitter -----------------------------------------------------

type entry struct {
	key, value string
	owner      string // "" for a default, else the translated key that owns it
	isDefault  bool
	repeat     bool // written by add(): one of several lines, not a single fact
}

type section struct {
	name    string
	entries []entry
}

type unitFile struct {
	sections []*section
}

func newUnit() *unitFile { return &unitFile{} }

func (u *unitFile) section(name string) *section {
	for _, s := range u.sections {
		if s.name == name {
			return s
		}
	}
	s := &section{name: name}
	u.sections = append(u.sections, s)
	return s
}

// own writes a directive a translated key owns; owner names that key for
// the collision message (empty for structural directives the tool owns).
func (u *unitFile) own(sec, key, value, owner string) {
	if owner == "" {
		owner = "systemd-compose"
	}
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, owner, false, false})
}

// add appends a repeatable directive.
func (u *unitFile) add(sec, key, value string) {
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, "systemd-compose", false, true})
}

// setDefault writes a directive the pass-through may override.
func (u *unitFile) setDefault(sec, key, value string) {
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, "", true, false})
}

// merge applies one pass-through key under the three rules. A directive
// the tool wrote as a single fact refuses first, whether or not systemd
// would accept a second line of it: `schedule:` owns OnCalendar= even
// though timers may list several.
func (u *unitFile) merge(secName string, k PassKey) error {
	s := u.section(secName)
	for _, e := range s.entries {
		if e.key == k.Name && !e.isDefault && !e.repeat {
			return fmt.Errorf("line %d: unit: %s: %s: collides with %s, which already writes %s=; one source per directive", k.Line, secName, k.Name, e.owner, k.Name)
		}
	}
	if repeatable(secName, k.Name) {
		for _, v := range k.Values {
			if k.Name == "Environment" {
				if name := envName(v); name != "" {
					for _, e := range s.entries {
						if e.key == "Environment" && envName(e.value) == name {
							return fmt.Errorf("line %d: unit: Service: Environment: %s is already set by environment:; one source per variable", k.Line, name)
						}
					}
				}
			}
			s.entries = append(s.entries, entry{k.Name, v, "", false, true})
		}
		return nil
	}
	if len(k.Values) != 1 {
		return fmt.Errorf("line %d: unit: %s: %s: takes one value (it is not a repeatable directive)", k.Line, secName, k.Name)
	}
	for i, e := range s.entries {
		if e.key == k.Name && e.isDefault {
			s.entries[i] = entry{k.Name, k.Values[0], "", false, false} // a default gives way, in place
			return nil
		}
	}
	s.entries = append(s.entries, entry{k.Name, k.Values[0], "", false, false})
	return nil
}

// envName is the variable an Environment= assignment sets, quotes stripped.
func envName(assignment string) string {
	a := strings.TrimLeft(assignment, "\"'")
	name, _, ok := strings.Cut(a, "=")
	if !ok {
		return ""
	}
	return name
}

// text is the unit file, after the one check every rendered line must
// pass: no newline in a key or value, or a value would become directives
// of its own and walk past every refusal above.
func (u *unitFile) text() (string, error) {
	for _, s := range u.sections {
		for _, e := range s.entries {
			if strings.ContainsAny(e.key+e.value, "\n\r") {
				return "", fmt.Errorf("[%s] %s: a newline in a value is refused (it would render as further directives)", s.name, e.key)
			}
		}
	}
	return u.String(), nil
}

func (u *unitFile) String() string {
	var b strings.Builder
	for i, s := range u.sections {
		if len(s.entries) == 0 {
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", s.name)
		for _, e := range s.entries {
			fmt.Fprintf(&b, "%s=%s\n", e.key, e.value)
		}
	}
	return b.String()
}

// repeatable says whether systemd accumulates a directive across lines.
// Anything else replaces, so it is single-valued here.
func repeatable(sec, key string) bool {
	if strings.HasPrefix(key, "Condition") || strings.HasPrefix(key, "Assert") {
		return true
	}
	switch sec {
	case "Unit":
		switch key {
		case "After", "Before", "Wants", "Requires", "Requisite", "BindsTo", "PartOf", "Upholds",
			"Conflicts", "OnFailure", "OnSuccess", "PropagatesReloadTo", "ReloadPropagatedFrom",
			"PropagatesStopTo", "StopPropagatedFrom", "JoinsNamespaceOf", "RequiresMountsFor",
			"WantsMountsFor", "Documentation":
			return true
		}
	case "Service":
		switch key {
		case "Environment", "EnvironmentFile", "PassEnvironment", "UnsetEnvironment",
			"ExecStartPre", "ExecStartPost", "ExecCondition", "ExecReload", "ExecStop", "ExecStopPost",
			"ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "ExecPaths", "NoExecPaths",
			"BindPaths", "BindReadOnlyPaths", "TemporaryFileSystem", "ExtensionDirectories",
			"SystemCallFilter", "SystemCallLog", "RestrictAddressFamilies", "RestrictFileSystems",
			"CapabilityBoundingSet", "AmbientCapabilities", "DeviceAllow", "SupplementaryGroups",
			"LogExtraFields", "LogFilterPatterns", "StateDirectory", "CacheDirectory",
			"LogsDirectory", "RuntimeDirectory", "ConfigurationDirectory",
			"SetCredential", "SetCredentialEncrypted", "LoadCredential", "LoadCredentialEncrypted",
			"ImportCredential", "SocketBindAllow", "SocketBindDeny", "RestrictNetworkInterfaces",
			"SuccessExitStatus", "RestartPreventExitStatus", "RestartForceExitStatus":
			return true
		}
	case "Timer":
		switch key {
		case "OnCalendar", "OnActiveSec", "OnBootSec", "OnStartupSec", "OnUnitActiveSec", "OnUnitInactiveSec":
			return true
		}
	case "Socket":
		switch key {
		case "ListenStream", "ListenDatagram", "ListenSequentialPacket", "ListenFIFO", "ListenSpecial",
			"ListenNetlink", "ListenMessageQueue", "ListenUSBFunction", "Symlinks",
			"ExecStartPre", "ExecStartPost", "ExecStopPre", "ExecStopPost":
			return true
		}
	}
	return false
}
