// Package render turns a validated Project into unit files.
//
// Every directive in a rendered unit has one source. A translated key owns
// the directives it writes, and a pass-through of the same directive is
// refused, naming both. A few directives are defaults the pass-through may
// override (Description, SyslogIdentifier, timer accuracy). systemd's
// repeatable directives are concatenated. What the renderer cannot spell
// safely in systemd's escaping, it refuses.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/probe"
)

// Rendered is one unit file.
type Rendered struct {
	Name string // unit file name, e.g. proj-api.service
	Text string
}

// RenderOptions are what a render needs from outside the project.
type RenderOptions struct {
	Exe        string   // the healthcheck probe's program, as the units name it
	Self       string   // this program, which up copies to Exe
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
	probePath, err := ProbePath(exe)
	if err != nil {
		return RenderOptions{}, err
	}
	return RenderOptions{Exe: probePath, Self: exe, SearchPath: search}, nil
}

// ProbePath is where the healthcheck probe runs from: a copy of this
// program named by its content, in the user's data directory. The program's
// own path would break every probe when the program moves or is removed (a
// restart loop), and an up from another copy of the same build would read
// every healthchecked unit as changed. A new build is a new path, so its up
// restarts what it probes.
func ProbePath(exe string) (string, error) {
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "systemd-compose", "probe-"+hex.EncodeToString(h.Sum(nil))[:16]), nil
}

// Render produces every unit file of the project.
func Render(p *config.Project, opt RenderOptions) ([]Rendered, error) {
	sl, err := renderSlice(p)
	if err != nil {
		return nil, err
	}
	out := []Rendered{sl}
	var timers, sockets []Rendered
	var problems []string
	for _, s := range p.EnabledServices() {
		svc, tmr, sock, err := renderService(p, s, opt)
		if err != nil {
			problems = append(problems, fmt.Sprintf("service %s: %v", s.Name, err))
			continue
		}
		out = append(out, svc)
		if tmr != nil {
			timers = append(timers, *tmr)
		}
		if sock != nil {
			sockets = append(sockets, *sock)
		}
	}
	if err := config.PerService(problems); err != nil {
		return nil, err
	}
	out = append(out, timers...)
	out = append(out, sockets...)
	tg, err := renderTarget(p)
	if err != nil {
		return nil, err
	}
	out = append(out, tg)
	for i := range out {
		out[i].Text = "# Rendered by systemd-compose from " + p.ConfigPath + "; up rewrites it.\n" +
			"# A local change belongs in a drop-in: systemctl --user edit " + out[i].Name + "\n" + out[i].Text
	}
	return out, nil
}

// renderSlice renders the slice whether or not the project caps it. Every
// service names it, so it is always part of the project: rendered, it is
// registered and unregistered with the rest, and dropping resources: is a
// change to its text, never an orphan whose retirement stops every service.
func renderSlice(p *config.Project) (Rendered, error) {
	u := newUnit()
	u.setDefault("Unit", "Description", projectDescription(p))
	if p.Resources != nil {
		addResources(u, "Slice", p.Resources)
	}
	return finish(u, p, "", p.SliceName())
}

// projectDescription is the slice's and the target's Description=.
func projectDescription(p *config.Project) string { return p.Name + " (systemd-compose project)" }

func renderTarget(p *config.Project) (Rendered, error) {
	u := newUnit()
	u.setDefault("Unit", "Description", projectDescription(p))
	for _, s := range p.EnabledServices() { // the boot set: no inactive profile
		for _, n := range p.UnitsOf(s) { // for a scheduled job: the timer, never the job
			u.add("Unit", "Wants", n)
		}
	}
	u.own("Install", "WantedBy", "default.target", "")
	return finish(u, p, "", p.TargetName())
}

// finish writes the marker section and the unit's text.
func finish(u *unitFile, p *config.Project, service, name string) (Rendered, error) {
	marker(u, p, service)
	text, err := u.text()
	return Rendered{name, text}, err
}

func renderService(p *config.Project, s *config.Service, opt RenderOptions) (svc Rendered, timer, socket *Rendered, err error) {
	u := newUnit()
	u.setDefault("Unit", "Description", p.Name+": "+s.Name)
	u.add("Unit", "PartOf", p.TargetName())
	if len(s.Listen) > 0 {
		// Requires= on the socket is systemd's documented edge for a
		// socket-activated service: the socket comes along whenever anything
		// starts the service, a dependent included, and a restart of the
		// socket takes the service with it. Without it, restarting a changed
		// socket while the service runs is refused ("already active") and
		// leaves the socket down.
		u.add("Unit", "Requires", p.SocketUnit(s))
	}
	for _, d := range s.DependsOn {
		dep := p.UnitName(d.Service, ".service")
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
	case s.Schedule != nil: // a scheduled job runs to its end
		u.own("Service", "Type", "oneshot", "schedule")
		u.oneshot = true
	case s.Oneshot:
		u.own("Service", "Type", "oneshot", "oneshot")
		u.own("Service", "RemainAfterExit", "yes", "oneshot")
		u.oneshot = true
	}
	switch st, err := os.Stat(s.WorkingDir); {
	case os.IsNotExist(err):
		return Rendered{}, nil, nil, fmt.Errorf("working_dir %s does not exist (a relative one is relative to the yaml)", s.WorkingDir)
	case err != nil:
		return Rendered{}, nil, nil, fmt.Errorf("working_dir: %v", err)
	case !st.IsDir():
		return Rendered{}, nil, nil, fmt.Errorf("working_dir %s is a file, not a directory", s.WorkingDir)
	}
	u.own("Service", "WorkingDirectory", config.UnitPath(s.WorkingDir), "working_dir")
	for _, kv := range s.Environment {
		u.add("Service", "Environment", envAssignment(kv))
	}
	if err := config.CheckEnvFiles(s.EnvFiles); err != nil {
		return Rendered{}, nil, nil, err
	}
	for _, ef := range s.EnvFiles {
		if ef.Required {
			u.add("Service", "EnvironmentFile", config.UnitPath(ef.Path))
		} else {
			u.add("Service", "EnvironmentFile", "-"+config.UnitPath(ef.Path))
		}
	}
	if err := envOverride(s, p.Dir); err != nil {
		return Rendered{}, nil, nil, err
	}
	made := "" // a program the build makes need not exist yet, for command: or the healthcheck
	if s.Build != nil {
		made = s.Build.Creates
	}
	if !s.Command.Empty() || !s.Entrypoint.Empty() {
		line, err := ExecLine(s.Entrypoint, s.Command, s.WorkingDir, opt.SearchPath, made)
		if err != nil {
			return Rendered{}, nil, nil, fmt.Errorf("command: %w", err)
		}
		owner := "command"
		if !s.Entrypoint.Empty() {
			owner = "entrypoint"
		}
		u.own("Service", "ExecStart", line, owner)
	}
	if h := s.Healthcheck; h != nil {
		test, err := resolveArgv(h.Test, s.WorkingDir, opt.SearchPath, made)
		if err != nil {
			return Rendered{}, nil, nil, fmt.Errorf("healthcheck: test: %w", err)
		}
		if opt.Exe == "" {
			return Rendered{}, nil, nil, fmt.Errorf("healthcheck: the probe needs this program's path (RenderOptions.Exe)")
		}
		u.add("Service", "ExecStartPost", quoteWord(config.UnitPath(opt.Exe))+" "+ExecLiteral(joinWords(probe.Args(h, test)))) // the program's path as it is
		u.own("Service", "TimeoutStartSec", strconv.Itoa(h.StartTimeout())+"s", "healthcheck")
	}
	if r := s.Restart; r != nil {
		u.own("Service", "Restart", r.Policy, "restart")
		if r.Delay != "" {
			u.own("Service", "RestartSec", r.Delay, "restart: delay")
		}
	}
	u.own("Service", "Slice", p.SliceName(), "")
	u.setDefault("Service", "SyslogIdentifier", p.LogIdentifier(s))
	if s.Resources != nil {
		addResources(u, "Service", s.Resources)
	}

	var t *unitFile
	if sc := s.Schedule; sc != nil {
		t = newUnit()
		t.setDefault("Unit", "Description", p.Name+": "+s.Name+" (timer)")
		t.add("Unit", "PartOf", p.TargetName())
		t.own("Timer", "OnCalendar", sc.Calendar, "schedule")
		if sc.Accuracy == "" {
			t.setDefault("Timer", "AccuracySec", config.Default("schedule.accuracy"))
		} else {
			t.own("Timer", "AccuracySec", sc.Accuracy, "schedule: accuracy") // a unit: one collides, as for persistent
		}
		t.own("Timer", "Persistent", yesno(sc.Persistent), "schedule: persistent")
		if sc.RandomizedDelay != "" {
			t.own("Timer", "RandomizedDelaySec", sc.RandomizedDelay, "schedule: randomized_delay")
		}
	}

	var k *unitFile
	if len(s.Listen) > 0 {
		k = newUnit()
		k.setDefault("Unit", "Description", p.Name+": "+s.Name+" (socket)")
		k.add("Unit", "PartOf", p.TargetName())
		for _, a := range s.Listen {
			k.add("Socket", "ListenStream", a)
		}
	}

	// Pass-through, last: Unit and Service into the service file, Timer and
	// Socket into theirs. A oneshot takes several ExecStart= lines, as
	// systemd allows; any other service takes one.
	u.oneshot = u.oneshot || s.Unit.Has("Service", "Type", "oneshot")
	for _, sec := range s.Unit.Sections {
		dst := u
		switch sec.Name {
		case "Timer":
			dst = t
		case "Socket":
			dst = k
		}
		for _, key := range sec.Keys {
			if err := dst.merge(sec.Name, key); err != nil {
				return Rendered{}, nil, nil, err
			}
		}
	}

	marker(u, p, s.Name)
	if len(s.EnvFiles) > 0 {
		u.own(MarkerSection, TriggersKey, envFileHash(s.EnvFiles), "")
	}
	text, err := u.text()
	if err != nil {
		return Rendered{}, nil, nil, err
	}
	svc = Rendered{p.ServiceUnit(s), text}
	if t != nil {
		r, err := finish(t, p, s.Name, p.TimerUnit(s))
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		timer = &r
	}
	if k != nil {
		r, err := finish(k, p, s.Name, p.SocketUnit(s))
		if err != nil {
			return Rendered{}, nil, nil, err
		}
		socket = &r
	}
	return svc, timer, socket, nil
}

func addResources(u *unitFile, section string, r *config.Resources) {
	for _, l := range Limits(r) {
		u.own(section, l.Directive, l.Value, "resources: "+l.Key)
	}
}

// Limit is one directive a resources: key writes.
type Limit struct{ Key, Directive, Value string }

// Limits are the directives r writes, in order.
func Limits(r *config.Resources) []Limit {
	var out []Limit
	if r.Memory != "" {
		out = append(out, Limit{"memory", "MemoryMax", r.Memory})
	}
	if r.CPUs > 0 {
		out = append(out, Limit{"cpus", "CPUQuota", CPUQuota(r.CPUs)})
	}
	if r.PIDs > 0 {
		out = append(out, Limit{"pids", "TasksMax", strconv.Itoa(r.PIDs)})
	}
	return out
}

// CPUQuota is CPUs as systemd's CPUQuota= takes them: a percent with two
// decimals at most, from whole hundredths, since cpus*100 in floating point
// is 28.999999999999996 for 0.29, which systemd refuses.
func CPUQuota(cpus float64) string {
	q := int64(math.Round(cpus * 10000))
	s := strconv.FormatInt(q/100, 10)
	if q%100 != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%02d", q%100), "0")
	}
	return s + "%"
}

// envAssignment spells KEY=value for Environment=. The value's % are
// systemd's (checked by specifiers at load), and $ is literal there.
func envAssignment(kv config.KV) string {
	return quoteWord(kv.Key + "=" + kv.Value)
}

// envFileHash makes an edit to an env_file visible as a text change in the
// unit, so the ordinary differ restarts the service. Contents only: the paths
// already appear in EnvironmentFile= lines. A required file that is missing
// has already been refused; an optional one hashes as absent.
func envFileHash(files []config.EnvFile) string {
	h := sha256.New()
	for _, ef := range files {
		if st, err := os.Stat(ef.Path); err == nil && !st.Mode().IsRegular() {
			h.Write([]byte("absent\x00")) // a FIFO's read would wait for ever
			continue
		}
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

// envOverride refuses what systemd would quietly do with the env files:
// drop a line it cannot read (export KEY=..., a bad name), and let a file
// win over environment: (EnvironmentFile= beats Environment= whatever the
// order, where compose lets environment: win). A key is compared with the
// value of the last file that sets it, the one systemd gives the service;
// the same value changes nothing and passes. Values are never printed.
func envOverride(s *config.Service, dir string) error {
	rel := func(path string) string {
		if r, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		return path
	}
	winner := map[string]string{} // key -> the file whose value the service gets
	values := map[string]string{}
	for _, ef := range s.EnvFiles {
		vars, dropped, differs, err := config.ReadEnvFile(ef.Path)
		if err != nil {
			continue // absent (optional) or unreadable: systemd says so itself
		}
		if len(dropped) > 0 {
			if strings.Contains(dropped[0], "never closed") || strings.Contains(dropped[0], "backslash") {
				return fmt.Errorf("env_file %s: %s", rel(ef.Path), dropped[0])
			}
			return fmt.Errorf("env_file %s: %s: systemd drops this line and says so only in its log: it takes KEY=value, with no export and a name of letters, digits and _", rel(ef.Path), dropped[0])
		}
		if len(differs) > 0 {
			return fmt.Errorf("env_file %s: %s: systemd keeps what follows a # (or a closing quote) in the value, where compose's .env drops it as a comment: put the comment on a line of its own, or quote the whole value if it belongs to it", rel(ef.Path), differs[0])
		}
		for k, v := range vars {
			winner[k], values[k] = ef.Path, v
		}
	}
	for _, kv := range s.Environment {
		if f, set := winner[kv.Key]; set && values[kv.Key] != strings.ReplaceAll(kv.Value, "%%", "%") {
			return fmt.Errorf("environment: %s would lose to env_file %s, which sets %s too: systemd gives a service the value of the last env file that sets a key, over Environment=, the opposite of compose. Drop %s from environment:, or set it there to what %s sets", kv.Key, rel(f), kv.Key, kv.Key, rel(f))
		}
	}
	return nil
}
